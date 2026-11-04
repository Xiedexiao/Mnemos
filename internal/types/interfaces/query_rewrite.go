package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// QueryRewriteRepository supplies the vocabulary behind §12.1 容错 corrections.
//
// 词表必须只含**调用者有权看到**的内容：纠错建议会把候选词直接回显给用户
// （"你是不是想搜 报警处理"），如果候选来自不可见的文档标题，纠错接口就变成
// 了一个存在性探测通道（§12.6 建议不泄露）。
type QueryRewriteRepository interface {
	// VocabularyTerms returns the distinct title fragments and glossary term
	// names visible in the given knowledge bases, capped at limit.
	//
	// Title fragments are the first N runes of each title rather than the whole
	// title: corrections operate on 2-rune tokens, and a full 60-character title
	// only adds noise (and rows) to the candidate set.
	VocabularyTerms(
		ctx context.Context, tenantID uint64, knowledgeBaseIDs []string, limit int,
	) ([]string, error)
	// TitleFragment returns the vocabulary fragment of one document's title
	// ("" when the document is gone). Used to subtract the fragments belonging
	// to denied documents: the vocabulary itself carries no knowledge ids.
	TitleFragment(ctx context.Context, tenantID uint64, knowledgeID string) (string, error)
	// GlossaryTerms returns the tenant's active term standard names.
	GlossaryTerms(ctx context.Context, tenantID uint64, limit int) ([]string, error)
}

// QueryRewriteService is the §12.1 容错 surface: pinyin search and typo
// correction as an additive plan, never as a silent replacement.
type QueryRewriteService interface {
	// Rewrite builds the rewrite plan for one query in one space.
	// knowledgeBaseIDs may be empty, in which case the caller's visible spaces
	// are resolved from their organizational context.
	Rewrite(
		ctx context.Context, tenantID uint64, knowledgeBaseIDs []string, query string,
	) (*types.QueryRewrite, error)
}
