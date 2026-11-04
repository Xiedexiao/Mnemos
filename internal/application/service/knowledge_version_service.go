package service

import (
	"context"
	"errors"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 版本操作的审计动作。
const (
	auditActionVersionDiffed     = "knowledge.version_diffed"
	auditActionVersionRolledBack = "knowledge.version_rolled_back"
	auditActionVersionAsOfRead   = "knowledge.version_as_of"
)

// knowledgeVersionQuery implements interfaces.KnowledgeVersionQueryService
// (KnowAxis V3.0 §7.4 diff / as-of / rollback).
type knowledgeVersionQuery struct {
	versions  interfaces.KnowledgeVersionRepository
	knowledge interfaces.KnowledgeRepository
	lifecycle interfaces.KnowledgeLifecycleService
	audit     interfaces.AuditLogService
	now       func() time.Time
}

// NewKnowledgeVersionQuery constructs the service.
func NewKnowledgeVersionQuery(
	versions interfaces.KnowledgeVersionRepository,
	knowledge interfaces.KnowledgeRepository,
	lifecycle interfaces.KnowledgeLifecycleService,
	audit interfaces.AuditLogService,
) interfaces.KnowledgeVersionQueryService {
	return &knowledgeVersionQuery{
		versions:  versions,
		knowledge: knowledge,
		lifecycle: lifecycle,
		audit:     audit,
		now:       time.Now,
	}
}

// Diff compares two versions of one document.
//
// 两侧都可以用语义化版本（"1.2.0"）、递增号（"3"）或 "current"（当前内容）。
// 缺省 left 取上一个版本：最常见的问题是"这次改了什么"，而不是"v1 和 v7
// 有什么不同"。
func (k *knowledgeVersionQuery) Diff(
	ctx context.Context, tenantID uint64, knowledgeID, leftRef, rightRef string,
) (*types.VersionDiff, error) {
	if strings.TrimSpace(knowledgeID) == "" {
		return nil, apperrors.NewBadRequestError("knowledge_id is required")
	}
	knowledge, err := k.knowledge.GetKnowledgeByID(ctx, tenantID, knowledgeID)
	if err != nil {
		return nil, apperrors.NewNotFoundError("knowledge not found")
	}

	right, rightLabel, err := k.resolveVersion(ctx, tenantID, knowledge, rightRef, true)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(leftRef) == "" {
		left, leftLabel, prevErr := k.previousVersion(ctx, tenantID, knowledge, right)
		if prevErr != nil {
			return nil, prevErr
		}
		leftRef = leftLabel
		rightRef = rightLabel
		if left == nil {
			return nil, apperrors.NewBadRequestError(
				"no previous version to compare against; pass both sides explicitly")
		}
		return k.buildDiff(ctx, tenantID, knowledgeID, left, leftRef, right, rightRef), nil
	}

	left, leftLabel, err := k.resolveVersion(ctx, tenantID, knowledge, leftRef, false)
	if err != nil {
		return nil, err
	}
	return k.buildDiff(ctx, tenantID, knowledgeID, left, leftLabel, right, rightLabel), nil
}

// buildDiff assembles the payload and audits the read.
func (k *knowledgeVersionQuery) buildDiff(
	ctx context.Context, tenantID uint64, knowledgeID string,
	left *versionSnapshot, leftLabel string, right *versionSnapshot, rightLabel string,
) *types.VersionDiff {
	lines, added, removed, unchanged, truncated := types.DiffParagraphs(left.body, right.body)
	diff := &types.VersionDiff{
		Left:               leftLabel,
		Right:              rightLabel,
		Lines:              lines,
		Added:              added,
		Removed:            removed,
		Unchanged:          unchanged,
		Changed:            added > 0 || removed > 0 || left.hash != right.hash,
		ContentHashChanged: left.hash != right.hash,
		Truncated:          truncated,
	}
	k.logAudit(ctx, tenantID, knowledgeID, auditActionVersionDiffed, map[string]interface{}{
		"left": leftLabel, "right": rightLabel, "changed": diff.Changed,
	})
	return diff
}

// versionSnapshot is one side of a comparison (a stored version or the
// document's current content).
type versionSnapshot struct {
	version int
	label   string
	title   string
	body    string
	hash    string
	// effectiveAt is the snapshot's effective instant when recorded.
	effectiveAt *time.Time
	raw         *types.KnowledgeVersion
}

// resolveVersion resolves one ref to a snapshot.
func (k *knowledgeVersionQuery) resolveVersion(
	ctx context.Context, tenantID uint64, knowledge *types.Knowledge,
	ref string, allowCurrent bool,
) (*versionSnapshot, string, error) {
	ref = strings.TrimSpace(ref)
	if allowCurrent && (ref == "" || strings.EqualFold(ref, "current")) {
		return currentSnapshot(knowledge), "current", nil
	}
	if strings.EqualFold(ref, "current") {
		return currentSnapshot(knowledge), "current", nil
	}
	row, err := k.findVersion(ctx, tenantID, knowledge.ID, ref)
	if err != nil {
		return nil, "", err
	}
	if row == nil {
		return nil, "", apperrors.NewNotFoundError("version " + ref + " not found")
	}
	return storedSnapshot(row), labelOf(row), nil
}

// previousVersion returns the version immediately before right.
func (k *knowledgeVersionQuery) previousVersion(
	ctx context.Context, tenantID uint64, knowledge *types.Knowledge, right *versionSnapshot,
) (*versionSnapshot, string, error) {
	if right == nil || right.version <= 1 {
		return nil, "", nil
	}
	row, err := k.versions.GetKnowledgeVersion(ctx, tenantID, knowledge.ID, right.version-1)
	if err != nil {
		return nil, "", err
	}
	if row == nil {
		// 版本号不连续（历史删改或早期数据）：退回"比它小的最新一条"。
		rows, listErr := k.versions.ListKnowledgeVersions(ctx, knowledge.ID, 200)
		if listErr != nil {
			return nil, "", listErr
		}
		for _, candidate := range rows {
			if candidate.Version < right.version {
				return storedSnapshot(candidate), labelOf(candidate), nil
			}
		}
		return nil, "", nil
	}
	return storedSnapshot(row), labelOf(row), nil
}

// findVersion resolves a version ref which may be semantic ("1.2.0") or the
// legacy integer ("3").
func (k *knowledgeVersionQuery) findVersion(
	ctx context.Context, tenantID uint64, knowledgeID, ref string,
) (*types.KnowledgeVersion, error) {
	// 先按语义化版本找：中间可能有点号，整数解析会失败。
	if strings.Contains(ref, ".") {
		if _, err := types.ParseSemanticVersion(ref); err == nil {
			rows, listErr := k.versions.ListKnowledgeVersions(ctx, knowledgeID, 200)
			if listErr != nil {
				return nil, listErr
			}
			for _, row := range rows {
				if strings.TrimPrefix(row.SemanticVersion, "v") == strings.TrimPrefix(ref, "v") {
					return row, nil
				}
			}
			return nil, nil
		}
	}
	var number int
	if _, scanErr := parseVersionNumber(ref, &number); scanErr != nil || number <= 0 {
		return nil, apperrors.NewBadRequestError("version must be a number like 3 or 1.2.0")
	}
	return k.versions.GetKnowledgeVersion(ctx, tenantID, knowledgeID, number)
}

// AsOf resolves the version in force at one instant.
func (k *knowledgeVersionQuery) AsOf(
	ctx context.Context, tenantID uint64, knowledgeID string, at time.Time,
) (*types.AsOfVersion, error) {
	if strings.TrimSpace(knowledgeID) == "" {
		return nil, apperrors.NewBadRequestError("knowledge_id is required")
	}
	if at.IsZero() {
		return nil, apperrors.NewBadRequestError("at is required")
	}
	rows, err := k.versions.ListKnowledgeVersionsUpTo(ctx, knowledgeID, at, 1)
	if err != nil {
		return nil, err
	}
	result := &types.AsOfVersion{KnowledgeID: knowledgeID, At: at.UTC()}
	if len(rows) == 0 {
		result.Note = "该时点之前没有发布版本"
		return result, nil
	}
	row := rows[0]
	result.Version = row
	result.Title = row.Title
	result.EffectiveFrom = row.EffectiveAt
	k.logAudit(ctx, tenantID, knowledgeID, auditActionVersionAsOfRead, map[string]interface{}{
		"at": at.UTC().Format(time.RFC3339), "version": row.Version,
	})
	return result, nil
}

// Rollback restores a historical version as a NEW version.
//
// 不删历史：§7.4 要求历史版本永久可查，删掉 v5 回退到 v4 会让"v5 存在过"
// 这件事从系统里消失——而"某段时间线上跑的是 v5"往往正是事故复盘需要的。
// 因此回滚 = 以旧内容新建一版，并在新版本上记录 rolled_back_from。
func (k *knowledgeVersionQuery) Rollback(
	ctx context.Context, tenantID uint64, input types.VersionRollbackInput,
) (*types.VersionRollbackResult, error) {
	if err := types.ValidateVersionRollbackInput(input); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	knowledge, err := k.knowledge.GetKnowledgeByID(ctx, tenantID, input.KnowledgeID)
	if err != nil {
		return nil, apperrors.NewNotFoundError("knowledge not found")
	}
	target, err := k.findVersion(ctx, tenantID, knowledge.ID, input.TargetVersion)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, apperrors.NewNotFoundError("target version not found")
	}
	latest, err := k.versions.GetLatestKnowledgeVersion(ctx, knowledge.ID)
	if err != nil {
		return nil, err
	}
	if latest != nil && latest.Version == target.Version {
		return nil, apperrors.NewBadRequestError("target version is already the latest")
	}

	// 回滚走生命周期发布：它会写快照、推进版本、记审计，并把内容状态
	// 拉回 valid。直接插版本行会绕过状态机与审批凭证校验。
	transition := &types.KnowledgeLifecycleTransitionInput{
		KnowledgeID:    knowledge.ID,
		Action:         types.KnowledgeLifecycleActionPublish,
		ActorID:        types.CallerFromContext(ctx).UserID,
		ChangeNote:     input.ChangeNote,
		VersionBump:    string(types.VersionBumpPatch),
		RolledBackFrom: labelOf(target),
	}
	if _, err := k.lifecycle.Transition(ctx, transition); err != nil {
		return nil, err
	}
	created, err := k.versions.GetLatestKnowledgeVersion(ctx, knowledge.ID)
	if err != nil {
		return nil, err
	}
	previous := ""
	if latest != nil {
		previous = labelOf(latest)
	}
	k.logAudit(ctx, tenantID, knowledge.ID, auditActionVersionRolledBack, map[string]interface{}{
		"restored_from": labelOf(target),
		"previous":      previous,
		"change_note":   input.ChangeNote,
	})
	return &types.VersionRollbackResult{
		NewVersion:      created,
		RestoredFrom:    labelOf(target),
		PreviousVersion: previous,
	}, nil
}

// logAudit writes one version audit event; failures never block the read.
func (k *knowledgeVersionQuery) logAudit(
	ctx context.Context, tenantID uint64, knowledgeID, action string,
	details map[string]interface{},
) {
	if k.audit == nil || tenantID == 0 {
		return
	}
	_ = k.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: types.CallerFromContext(ctx).UserID,
		Action:      types.AuditAction(action),
		ScopeType:   "knowledge",
		ScopeID:     knowledgeID,
		TargetType:  "knowledge_version",
		TargetID:    knowledgeID,
		Outcome:     "success",
		Details:     lifecycleJSON(details),
	})
}

// currentSnapshot projects the live document as one side of a diff.
func currentSnapshot(knowledge *types.Knowledge) *versionSnapshot {
	return &versionSnapshot{
		version: knowledge.CurrentVersion,
		label:   "current",
		title:   knowledge.Title,
		body:    knowledge.Description,
		hash:    knowledge.FileHash,
	}
}

// storedSnapshot projects a stored version as one side of a diff.
func storedSnapshot(row *types.KnowledgeVersion) *versionSnapshot {
	if row == nil {
		return nil
	}
	body := ""
	if len(row.Snapshot) > 0 {
		if payload, err := row.Snapshot.Map(); err == nil {
			if text, ok := payload["body"].(string); ok {
				body = text
			} else if text, ok := payload["description"].(string); ok {
				body = text
			}
		}
	}
	return &versionSnapshot{
		version:     row.Version,
		label:       labelOf(row),
		title:       row.Title,
		body:        body,
		hash:        row.ContentHash,
		effectiveAt: row.EffectiveAt,
		raw:         row,
	}
}

// labelOf renders a version label, preferring the semantic version.
func labelOf(row *types.KnowledgeVersion) string {
	if row == nil {
		return ""
	}
	if strings.TrimSpace(row.SemanticVersion) != "" {
		return row.SemanticVersion
	}
	return "v" + itoaVersion(row.Version)
}

// itoaVersion renders an int without importing strconv in this file's callers.
func itoaVersion(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := make([]byte, 0, 8)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

// parseVersionNumber parses a bare integer version ref.
func parseVersionNumber(text string, out *int) (int, error) {
	number := 0
	for _, r := range strings.TrimSpace(text) {
		if r < '0' || r > '9' {
			return 0, errNotANumber
		}
		number = number*10 + int(r-'0')
	}
	*out = number
	return 1, nil
}

// errNotANumber marks a non-numeric version ref.
var errNotANumber = errors.New("not a number")

// compile-time guard.
var _ interfaces.KnowledgeVersionQueryService = (*knowledgeVersionQuery)(nil)

// ensure the logger import stays used if the audit helpers change.
var _ = logger.Infof
