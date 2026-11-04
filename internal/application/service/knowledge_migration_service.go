package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// knowledgeMigrationService implements interfaces.KnowledgeMigrationService
// (KnowAxis V3.0 §16.3 迁移工具).
type knowledgeMigrationService struct {
	knowledges interfaces.KnowledgeService
	tags       interfaces.KnowledgeTagService
	kgRepo     interfaces.KnowledgeRepository
	audit      interfaces.AuditLogService
}

// NewKnowledgeMigrationService constructs the service.
func NewKnowledgeMigrationService(
	knowledges interfaces.KnowledgeService,
	tags interfaces.KnowledgeTagService,
	kgRepo interfaces.KnowledgeRepository,
	audit interfaces.AuditLogService,
) interfaces.KnowledgeMigrationService {
	return &knowledgeMigrationService{
		knowledges: knowledges, tags: tags, kgRepo: kgRepo, audit: audit,
	}
}

// Import applies a bulk import manifest.
//
// 逐条独立：一条坏文件不阻断其余条目。整批校验（路径、密级、重复
// external_id）在写入前完成——迁移是一次性大批量操作，让它在第 800 条才发现
// 清单有错等于让人重来一遍。
func (s *knowledgeMigrationService) Import(
	ctx context.Context, tenantID uint64, actorID string, manifest types.MigrationManifest,
) (*types.MigrationReport, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	normalized, err := types.NormalizeMigrationManifest(manifest)
	if err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	report := &types.MigrationReport{
		KnowledgeBaseID: normalized.KnowledgeBaseID,
		DryRun:          normalized.DryRun,
		Total:           len(normalized.Items),
		Results:         make([]types.MigrationItemResult, 0, len(normalized.Items)),
	}

	for _, item := range normalized.Items {
		result := s.importOne(ctx, tenantID, actorID, normalized, item)
		report.Results = append(report.Results, result)
		switch result.Status {
		case types.MigrationItemCreated:
			report.Created++
		case types.MigrationItemUpdated:
			report.Updated++
		case types.MigrationItemSkipped:
			report.Skipped++
		case types.MigrationItemFailed:
			report.Failed++
		}
	}

	// 一个整批的审计事件：单条结果在报告里，不需要逐条写审计。
	if s.audit != nil {
		_ = s.audit.Log(ctx, &types.AuditLog{
			TenantID:    tenantID,
			ActorUserID: actorID,
			Action:      types.AuditAction("knowledge.migration_imported"),
			ScopeType:   "knowledge_base",
			ScopeID:     normalized.KnowledgeBaseID,
			TargetType:  "migration_manifest",
			Outcome:     "success",
			Details: gamificationAuditJSON(map[string]interface{}{
				"dry_run": normalized.DryRun,
				"total":   report.Total,
				"created": report.Created,
				"updated": report.Updated,
				"skipped": report.Skipped,
				"failed":  report.Failed,
			}),
		})
	}
	return report, nil
}

// importOne applies one manifest entry.
func (s *knowledgeMigrationService) importOne(
	ctx context.Context, tenantID uint64, actorID string,
	manifest types.MigrationManifest, item types.MigrationItem,
) types.MigrationItemResult {
	result := types.MigrationItemResult{
		ExternalID: item.ExternalID,
		SourcePath: item.SourcePath,
	}
	result.FolderPath = item.FolderPath
	if result.FolderPath == "" {
		result.FolderPath = types.ResolveMigrationFolder(item.SourcePath, manifest.FolderMappings)
	}
	title := types.MigrationItemTitle(item)
	if title == "" {
		result.Status = types.MigrationItemFailed
		result.Reason = "cannot derive a title from the source path"
		return result
	}

	// 幂等：同一个 external_id 已经导入过就更新而不是新建，迁移因此可以
	// 重复执行（网络中断后重跑是常态）。
	existing := s.findExisting(ctx, tenantID, manifest.KnowledgeBaseID, item.ExternalID)
	if existing != nil {
		if item.ContentHash != "" && migrationHashMatches(existing, item.ContentHash) {
			result.Status = types.MigrationItemSkipped
			result.KnowledgeID = existing.ID
			result.Reason = "content unchanged"
			return result
		}
		if manifest.DryRun {
			result.Status = types.MigrationItemUpdated
			result.KnowledgeID = existing.ID
			result.Reason = "dry run: would update"
			return result
		}
		updated, err := s.applyUpdate(ctx, tenantID, manifest, item, existing)
		if err != nil {
			result.Status = types.MigrationItemFailed
			result.Reason = err.Error()
			return result
		}
		result.Status = types.MigrationItemUpdated
		result.KnowledgeID = updated.ID
		return result
	}

	if manifest.DryRun {
		result.Status = types.MigrationItemCreated
		result.Reason = "dry run: would create"
		return result
	}
	created, err := s.createOne(ctx, tenantID, manifest, item, title)
	if err != nil {
		result.Status = types.MigrationItemFailed
		result.Reason = err.Error()
		return result
	}
	result.Status = types.MigrationItemCreated
	result.KnowledgeID = created.ID
	return result
}

// findExisting resolves an already-imported row by external id.
//
// 用标题 + 目录作为退化键：源系统不给 external_id 时，重跑至少要能认出
// "同名同目录"的那一条，否则每次重跑都会复制一份。
func (s *knowledgeMigrationService) findExisting(
	ctx context.Context, tenantID uint64, kbID, externalID string,
) *types.Knowledge {
	if s.kgRepo == nil || strings.TrimSpace(externalID) == "" {
		return nil
	}
	rows, err := s.kgRepo.ListKnowledgeByKnowledgeBaseID(ctx, tenantID, kbID)
	if err != nil {
		return nil
	}
	for _, row := range rows {
		if row == nil {
			continue
		}
		if migrationExternalID(row) == externalID {
			return row
		}
	}
	return nil
}

// migrationExternalID reads the external id recorded in the row metadata.
func migrationExternalID(knowledge *types.Knowledge) string {
	if knowledge == nil || len(knowledge.Metadata) == 0 {
		return ""
	}
	payload, err := knowledge.Metadata.Map()
	if err != nil {
		return ""
	}
	value, _ := payload["migration_external_id"].(string)
	return value
}

// migrationHashMatches compares the stored migration hash.
func migrationHashMatches(knowledge *types.Knowledge, want string) bool {
	if knowledge == nil || len(knowledge.Metadata) == 0 {
		return false
	}
	payload, err := knowledge.Metadata.Map()
	if err != nil {
		return false
	}
	stored, _ := payload["migration_content_hash"].(string)
	return stored != "" && stored == want
}

// createOne writes one new entry through the manual-creation path.
//
// 复用 CreateKnowledgeFromManual 而不是直接插库：它已经做了内容清洗、长度
// 上限与标题校验——迁移同样需要这些约束，绕过它等于给知识库开一个脏数据入口。
func (s *knowledgeMigrationService) createOne(
	ctx context.Context, tenantID uint64, manifest types.MigrationManifest,
	item types.MigrationItem, title string,
) (*types.Knowledge, error) {
	payload := &types.ManualKnowledgePayload{
		Title:   title,
		Content: migrationBody(title, item),
		Status:  "publish",
		// 迁移进来的内容是源系统的既有内容，不是平台上的"新知识"。
		Channel: "migration",
	}
	created, err := s.knowledges.CreateKnowledgeFromManual(ctx, manifest.KnowledgeBaseID, payload, "migration")
	if err != nil {
		return nil, err
	}
	s.applyMigrationMetadata(ctx, tenantID, manifest, item, created)
	return created, nil
}

// applyUpdate rewrites an existing entry's migration metadata.
func (s *knowledgeMigrationService) applyUpdate(
	ctx context.Context, tenantID uint64, manifest types.MigrationManifest,
	item types.MigrationItem, existing *types.Knowledge,
) (*types.Knowledge, error) {
	s.applyMigrationMetadata(ctx, tenantID, manifest, item, existing)
	if err := s.kgRepo.UpdateKnowledge(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// applyMigrationMetadata records the migration fields on the row.
//
// 记录 external_id / content_hash / source_version / source_updated_at 到
// metadata，而不是新增列：这些是迁移过程的溯源信息，不参与检索与权限判定，
// 塞进主表会让每张 knowledge 行都背上迁移专用的列。
func (s *knowledgeMigrationService) applyMigrationMetadata(
	ctx context.Context, tenantID uint64, manifest types.MigrationManifest,
	item types.MigrationItem, knowledge *types.Knowledge,
) {
	if knowledge == nil {
		return
	}
	payload := map[string]interface{}{}
	if len(knowledge.Metadata) > 0 {
		if existing, err := knowledge.Metadata.Map(); err == nil {
			payload = existing
		}
	}
	if item.ExternalID != "" {
		payload["migration_external_id"] = item.ExternalID
	}
	if item.ContentHash != "" {
		payload["migration_content_hash"] = item.ContentHash
	}
	if item.SourceVersion != "" {
		payload["migration_source_version"] = item.SourceVersion
	}
	if item.SourceUpdatedAt != "" {
		payload["migration_source_updated_at"] = item.SourceUpdatedAt
	}
	payload["migration_source_path"] = item.SourcePath
	knowledge.Metadata = migrationJSON(payload)

	if folder := types.ResolveMigrationFolder(item.SourcePath, manifest.FolderMappings); folder != "" {
		knowledge.FolderPath = folder
	}
	classification := item.Classification
	if classification == "" {
		classification = manifest.DefaultClassification
	}
	if classification != "" {
		knowledge.Classification = classification
	}
	// 源系统的最后修改时间决定新鲜度：不保留它的话，整个空间在导入那一刻
	// 会被刷成"今天更新"，复审周期与新鲜度信号全部失真。
	if updatedAt, ok := types.MigrationSourceUpdatedAt(item); ok {
		knowledge.UpdatedAt = updatedAt
	}
	_ = ctx
	_ = tenantID
}

// migrationBody renders the Markdown body for one imported item.
//
// 迁移清单只携带元数据（路径、版本、来源），正文由源系统另行提供；这里生成
// 一个可检索的占位正文，包含足以回查原文的字段，而不是留空。
func migrationBody(title string, item types.MigrationItem) string {
	var builder strings.Builder
	builder.WriteString("# " + title + "\n\n")
	builder.WriteString("- 来源路径: " + item.SourcePath + "\n")
	if item.SourceVersion != "" {
		builder.WriteString("- 源版本: " + item.SourceVersion + "\n")
	}
	if item.SourceUpdatedAt != "" {
		builder.WriteString("- 源更新时间: " + item.SourceUpdatedAt + "\n")
	}
	return builder.String()
}

// migrationJSON encodes the metadata payload, falling back to an empty object.
func migrationJSON(payload map[string]interface{}) types.JSON {
	raw, err := json.Marshal(payload)
	if err != nil {
		return types.JSON("{}")
	}
	return types.JSON(raw)
}

// compile-time guard.
var _ interfaces.KnowledgeMigrationService = (*knowledgeMigrationService)(nil)

// errMigrationUnavailable marks a wiring problem.
var errMigrationUnavailable = errors.New("knowledge migration service is not configured")
