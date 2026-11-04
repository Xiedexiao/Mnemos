package service

import (
	"context"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// queryRewriteService implements interfaces.QueryRewriteService
// (KnowAxis V3.0 §12.1 容错：拼音搜索 + 错别字纠正).
//
// 这个服务只做"多给几条召回路径"，从不替用户改词：原查询永远保留并优先
// 检索，备选词作为额外一路。改词会让"我明明搜的是这个词"变得无从解释，
// 也让搜索结果无法复现。
//
// 它依赖调用者可见的空间集合：纠错候选来自标题与术语，把不可见文档的标题
// 片段回显给用户，纠错接口就成了一次存在性探测（§12.6）。
type queryRewriteService struct {
	repo     interfaces.QueryRewriteRepository
	acl      interfaces.KnowledgeACLRepository
	subjects aclSubjectResolver
	kb       interfaces.KnowledgeBaseService
	now      func() time.Time
}

// NewQueryRewriteService constructs the service.
func NewQueryRewriteService(
	repo interfaces.QueryRewriteRepository,
	acl interfaces.KnowledgeACLRepository,
	subjectGroups interfaces.ACLSubjectGroupService,
	kb interfaces.KnowledgeBaseService,
) interfaces.QueryRewriteService {
	return &queryRewriteService{
		repo:     repo,
		acl:      acl,
		subjects: newACLSubjectResolver(subjectGroups),
		kb:       kb,
		now:      time.Now,
	}
}

// maxRewriteVocabulary caps how many candidate terms one rewrite considers.
//
// 每次纠错要对词表的每个片段算一次编辑距离。2000 个片段是毫秒级的上限，
// 再往上就该考虑倒排索引了——但企业级租户的标题量本就在这个量级内。
const maxRewriteVocabulary = 2000

// Rewrite builds the rewrite plan for one query.
//
// 失败策略：词表或 ACL 取不到时返回一份**只有原查询**的计划而不是报错。
// 容错是检索的增强项，取不到候选就不增强，绝不能因此让检索本身失败。
func (s *queryRewriteService) Rewrite(
	ctx context.Context, tenantID uint64, knowledgeBaseIDs []string, query string,
) (*types.QueryRewrite, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return &types.QueryRewrite{Original: ""}, nil
	}
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}

	kbIDs := knowledgeBaseIDs
	if len(kbIDs) == 0 {
		// 未限定空间：用调用者可见的空间。解析失败就退回"只用术语表"，
		// 因为术语不依赖文档 ACL。
		resolved, err := s.visibleKnowledgeBaseIDs(ctx, tenantID)
		if err != nil {
			logger.Warnf(ctx, "[query-rewrite] scope resolution failed: %v", err)
		}
		kbIDs = resolved
	}

	vocabulary, err := s.repo.VocabularyTerms(ctx, tenantID, kbIDs, maxRewriteVocabulary)
	if err != nil {
		logger.Warnf(ctx, "[query-rewrite] vocabulary unavailable: %v", err)
		vocabulary = nil
	}
	// 标题词表已按 (tenant, kb) 限定，但目录/文档级 deny 仍可能命中：
	// 纠错候选是把词回显给用户，必须把 deny 也扣掉。
	if len(kbIDs) > 0 && len(vocabulary) > 0 {
		vocabulary = s.withoutDeniedTitles(ctx, tenantID, kbIDs, vocabulary)
	}
	plan := types.BuildQueryRewrite(trimmed, vocabulary, 5)
	return plan, nil
}

// visibleKnowledgeBaseIDs resolves the caller's document-type spaces.
func (s *queryRewriteService) visibleKnowledgeBaseIDs(
	ctx context.Context, tenantID uint64,
) ([]string, error) {
	caller := types.CallerFromContext(ctx)
	if caller.UserID == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	if s.kb == nil {
		return nil, nil
	}
	bases, err := s.kb.ListKnowledgeBases(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(bases))
	for _, kb := range bases {
		if kb == nil || kb.Type != types.KnowledgeBaseTypeDocument {
			continue
		}
		if kb.TenantID != 0 && kb.TenantID != tenantID {
			continue
		}
		ids = append(ids, kb.ID)
	}
	return ids, nil
}

// withoutDeniedTitles drops vocabulary entries that come from denied documents.
//
// 难点：词表里只有字符串，没有知识 ID，无法逐条判定"这个词来自哪一篇"。
// 因此这里做的是**反向**判定——先取被拒文档的标题片段，再从词表里扣掉
// 完全相等的条目。代价是多一次查询，换来的是"被拒文档独有的词不会出现在
// 纠错建议里"。
func (s *queryRewriteService) withoutDeniedTitles(
	ctx context.Context, tenantID uint64, kbIDs, vocabulary []string,
) []string {
	if s.acl == nil {
		return vocabulary
	}
	caller := types.CallerFromContext(ctx)
	subjects, err := s.subjects.ForCaller(ctx, caller)
	if err != nil {
		// fail-closed：拿不到主体就无法判断哪些词该扣，宁可整份候选清空，
		// 也不要把可能来自无权限文档的词回显出去。
		logger.Warnf(ctx, "[query-rewrite] subject expansion failed, corrections withheld: %v", err)
		return nil
	}
	denied, err := s.acl.DeniedKnowledgeIDsForSubjects(
		ctx, kbIDs, subjects, types.PermissionActionView, s.now())
	if err != nil {
		logger.Warnf(ctx, "[query-rewrite] acl lookup failed, corrections withheld: %v", err)
		return nil
	}
	if len(denied) == 0 {
		return vocabulary
	}
	deniedFragments := make(map[string]struct{}, len(denied))
	for _, id := range denied {
		title, titleErr := s.repo.TitleFragment(ctx, tenantID, id)
		if titleErr != nil || title == "" {
			continue
		}
		deniedFragments[title] = struct{}{}
	}
	if len(deniedFragments) == 0 {
		// 拿不到被拒文档的标题就无法证明词表干净：同样 fail-closed。
		return nil
	}
	out := make([]string, 0, len(vocabulary))
	for _, term := range vocabulary {
		if _, blocked := deniedFragments[term]; blocked {
			continue
		}
		out = append(out, term)
	}
	return out
}

// compile-time guard.
var _ interfaces.QueryRewriteService = (*queryRewriteService)(nil)
