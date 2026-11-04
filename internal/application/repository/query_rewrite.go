package repository

import (
	"context"
	"strings"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// queryRewriteRepository implements interfaces.QueryRewriteRepository
// (KnowAxis V3.0 §12.1 容错：拼音搜索 / 错别字纠正).
type queryRewriteRepository struct {
	db *gorm.DB
}

// NewQueryRewriteRepository constructs the repository.
func NewQueryRewriteRepository(db *gorm.DB) interfaces.QueryRewriteRepository {
	return &queryRewriteRepository{db: db}
}

// vocabularyFragmentRunes is how many runes of a title enter the candidate set.
//
// 纠错在 2 字片段上工作，一个 60 字的标题只会给候选集添噪声与行数。取前 12
// 个字：能完整覆盖"E45 报警处理"这类"编号 + 短语"的标题（6 个会把短语切掉
// 一半，纠出来的候选就成了半个词），又不至于把整段描述拉进来。
const vocabularyFragmentRunes = 12

// VocabularyTerms returns visible title fragments plus glossary term names.
//
// 两次查询分开做而不是 UNION：标题有生命周期与 ACL 两道门槛要下推，术语表
// 没有；硬合在一起会让"术语为什么不受 ACL 约束"这个决定变得难以解释。
func (r *queryRewriteRepository) VocabularyTerms(
	ctx context.Context, tenantID uint64, knowledgeBaseIDs []string, limit int,
) ([]string, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	terms := make([]string, 0, limit)

	if len(knowledgeBaseIDs) > 0 {
		type titleRow struct {
			Title string
		}
		var rows []titleRow
		// 生命周期口径与检索一致：草稿/过期/下架不该出现在"你是不是想搜"里。
		err := r.db.WithContext(ctx).Table("knowledges AS k").
			Select("k.title").
			Where("k.tenant_id = ? AND k.knowledge_base_id IN ?", tenantID, knowledgeBaseIDs).
			Where("k.deleted_at IS NULL").
			Where("COALESCE(NULLIF(k.lifecycle_status, ''), 'valid') IN ?",
				[]string{
					string(types.KnowledgeLifecycleValid),
					string(types.KnowledgeLifecyclePendingReviewCycle),
					string(types.KnowledgeLifecycleStale),
				}).
			Where("k.title <> ''").
			Order("k.updated_at DESC").
			Limit(limit).
			Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if fragment := titleFragment(row.Title); fragment != "" {
				terms = append(terms, fragment)
			}
		}
	}

	// 术语是租户级受控词表，不受文档 ACL 约束：它是该租户公开的词汇表。
	type glossaryRow struct {
		StandardName string
	}
	var glossaryRows []glossaryRow
	err := r.db.WithContext(ctx).Table("glossary_terms").
		Select("standard_name").
		Where("tenant_id = ? AND status = ?", tenantID,
			string(types.GlossaryTermStatusActive)).
		Order("standard_name ASC").
		Limit(limit).
		Scan(&glossaryRows).Error
	if err != nil {
		// 术语表不可用不阻断标题词表：少一路候选比整个纠错失效要好。
		return terms, nil
	}
	for _, row := range glossaryRows {
		if name := strings.TrimSpace(row.StandardName); name != "" {
			terms = append(terms, name)
		}
	}
	return terms, nil
}

// GlossaryTerms returns the tenant's active term standard names.
func (r *queryRewriteRepository) GlossaryTerms(
	ctx context.Context, tenantID uint64, limit int,
) ([]string, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	type row struct {
		StandardName string
	}
	var rows []row
	err := r.db.WithContext(ctx).Table("glossary_terms").
		Select("standard_name").
		Where("tenant_id = ? AND status = ?", tenantID,
			string(types.GlossaryTermStatusActive)).
		Order("standard_name ASC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if name := strings.TrimSpace(row.StandardName); name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

// isFileExtension reports whether suffix looks like a filename extension.
func isFileExtension(suffix string) bool {
	if len(suffix) == 0 || len(suffix) > 5 {
		return false
	}
	for _, ch := range suffix {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z':
		case ch >= '0' && ch <= '9':
		default:
			return false
		}
	}
	return true
}

// titleFragment keeps the leading runes of a title, dropping any extension.
//
// 标题常带扩展名与版本后缀（"E45 报警处理_v2.docx"）。纠错只关心词本身，
// 所以去掉扩展名后再截断。
func titleFragment(title string) string {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return ""
	}
	// 扩展名判定：点后只能是 1–5 个 ASCII 字母/数字，且不含空格。
	//
	// 按字节数判断会把"版本 2.0 说明"的 " 说明"（7 字节）当成扩展名截掉——
	// 中文一个字三字节，字节长度根本不代表"这是个短后缀"。同时要求不含空格，
	// 因为"2.0 说明"里的点后面还有正文。
	if dot := strings.LastIndexByte(trimmed, '.'); dot > 0 {
		suffix := trimmed[dot+1:]
		if isFileExtension(suffix) {
			trimmed = trimmed[:dot]
		}
	}
	runes := []rune(trimmed)
	if len(runes) > vocabularyFragmentRunes {
		runes = runes[:vocabularyFragmentRunes]
	}
	return string(runes)
}

// TitleFragment returns one document's vocabulary fragment.
//
// 单独一个方法而不是复用 VocabularyTerms：纠错服务需要的是"被拒文档的片段
// 是什么"，按 ID 逐条取，不能让被拒的标题先进入候选再被扣掉——那中间态
// 一旦被记录或缓存就已经泄露了。
func (r *queryRewriteRepository) TitleFragment(
	ctx context.Context, tenantID uint64, knowledgeID string,
) (string, error) {
	if knowledgeID == "" {
		return "", nil
	}
	type titleRow struct {
		Title string
	}
	var row titleRow
	err := r.db.WithContext(ctx).Table("knowledges").
		Select("title").
		Where("tenant_id = ? AND id = ?", tenantID, knowledgeID).
		Limit(1).
		Scan(&row).Error
	if err != nil {
		return "", err
	}
	return titleFragment(row.Title), nil
}
