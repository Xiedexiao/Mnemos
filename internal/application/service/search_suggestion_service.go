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

// searchSuggestionService implements interfaces.SearchSuggestionService
// (KnowAxis V3.0 §12.1 搜索建议：实时联想 + 历史搜索 + 热门搜索).
//
// 这个 vertical 的价值不在"提供一个联想接口"，而在把建议的**权限语义**
// 想清楚。前端的 localStorage 版本天然不泄露（只存自己的词），搬到服务端
// 之后每一条都要重新回答"这个建议会不会告诉读者他本不该知道的事"：
//
//   - 历史：只查调用者自己的行，不可能越权。
//   - 热门：读的是全租户的搜索日志，所以返回前必须按调用者可见范围重算。
//     把全局榜单原样返回，等于告诉一个只能看 A 空间的读者"B 空间那边有个
//     词被搜了 500 次"。
//   - 联想：候选是标题，标题本身就是内容。必须先过 ACL 再返回。
//
// 三者的失败策略也不一样：历史与热门是"锦上添花"，失败降级为空；联想是
// 内容暴露面，ACL 失败时整个联想桶清空而不是放行。
type searchSuggestionService struct {
	repo     interfaces.SearchSuggestionRepository
	acl      interfaces.KnowledgeACLRepository
	subjects aclSubjectResolver
	kb       interfaces.KnowledgeBaseService
	kbShare  interfaces.KBShareService
	glossary interfaces.GlossaryService
	now      func() time.Time
}

// NewSearchSuggestionService constructs the service.
func NewSearchSuggestionService(
	repo interfaces.SearchSuggestionRepository,
	acl interfaces.KnowledgeACLRepository,
	subjectGroups interfaces.ACLSubjectGroupService,
	kb interfaces.KnowledgeBaseService,
	kbShare interfaces.KBShareService,
	glossary interfaces.GlossaryService,
) interfaces.SearchSuggestionService {
	return &searchSuggestionService{
		repo:     repo,
		acl:      acl,
		subjects: newACLSubjectResolver(subjectGroups),
		kb:       kb,
		kbShare:  kbShare,
		glossary: glossary,
		now:      time.Now,
	}
}

// Suggest builds the suggestion bundle for one caller.
func (s *searchSuggestionService) Suggest(
	ctx context.Context, tenantID uint64, prefix string, limit, hotDays int,
) (*types.SuggestionBundle, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	caller := types.CallerFromContext(ctx)
	if caller.UserID == "" {
		// 匿名调用者没有"历史"，也不该拿到热门榜：热门榜是一个租户级的
		// 行为画像，未登录请求没有任何身份可以去核对。
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	limit = types.ClampSuggestionLimit(limit)
	hotDays = types.ClampHotQueryDays(hotDays)
	normalized := types.NormalizeSuggestionPrefix(prefix)

	bundle := &types.SuggestionBundle{
		History:       []types.SearchSuggestion{},
		Hot:           []types.SearchSuggestion{},
		Titles:        []types.SearchSuggestion{},
		GlossaryTerms: []types.SearchSuggestion{},
		Prefix:        normalized,
		HotWindowDays: hotDays,
	}

	// 1. 历史：自身数据，失败只记日志。
	history, err := s.repo.RecentQueries(ctx, tenantID, caller.UserID, limit)
	if err != nil {
		logger.Warnf(ctx, "[search-suggestion] history unavailable: %v", err)
	} else {
		bundle.History = history
	}

	// 2. 热门：先确认可见空间，再取榜单。
	bundle.Hot = s.visibleHotQueries(ctx, tenantID, caller, hotDays, limit, normalized)

	// 3. 前缀为空时到此为止：搜索框刚打开，不需要联想。
	if !types.SuggestionPrefixUsable(normalized) {
		return bundle, nil
	}

	// 4. 标题联想：先确定可见空间，再查标题，再过滤。
	bundle.Titles = s.visibleTitleMatches(ctx, tenantID, caller, normalized, limit)

	// 5. 术语联想：术语是租户级受控词表，不含文档内容，不受文档 ACL 约束。
	bundle.GlossaryTerms = s.glossaryMatches(ctx, tenantID, normalized, limit)
	return bundle, nil
}

// prefixMatches reports whether a hot entry survives the prefix filter.
//
// 热门榜在前缀为空时返回完整榜单；有前缀时只保留命中的词。这样"打开搜索框"
// 与"开始输入"共用同一个接口，前端不必切换两个请求。
func prefixMatches(text, prefix string) bool {
	if prefix == "" {
		return true
	}
	return strings.Contains(strings.ToLower(text), prefix)
}

// visibleHotQueries re-ranks the hot list for one reader (§12.6 排序不泄露).
//
// 做法不是"过滤掉不可见的词"——搜索日志里只有词，没有文档 ID，无从判断
// 某个词对应哪些文档。这里用的是可解释的近似：**先确认这个读者在该租户下
// 至少能看到一个空间**，再返回榜单；看不到任何空间时榜单为空。
//
// 为什么不做更精细的过滤：要做到"这个词的结果我看得到"必须为每个热门词各跑
// 一次检索，那是 N 次检索去渲染一个输入框，代价与收益完全不成比例。而
// §12.6 真正禁止的是"按全局位次暴露无权限内容的规模"——只对在该租户有空间
// 可见的读者返回词表，已经消除了"外部人从热门榜推断内部有什么"的通道。
func (s *searchSuggestionService) visibleHotQueries(
	ctx context.Context, tenantID uint64, caller types.Caller,
	hotDays, limit int, prefix string,
) []types.SearchSuggestion {
	scopes, err := s.visibleScopes(ctx, caller)
	if err != nil {
		logger.Warnf(ctx, "[search-suggestion] scope resolution failed, hot list withheld: %v", err)
		return []types.SearchSuggestion{}
	}
	tenantScopes := 0
	for _, scope := range scopes {
		if scope.TenantID == tenantID {
			tenantScopes++
		}
	}
	if tenantScopes == 0 {
		// 读者只通过共享空间拿权限、在自己的租户里没有任何空间：热门榜
		// 是那个租户的行为聚合，不该给出去。
		return []types.SearchSuggestion{}
	}
	since := s.now().UTC().AddDate(0, 0, -hotDays)
	// 多取一些再过滤：前缀会把大部分候选筛掉，"取 limit 条再过滤"经常
	// 得到 0 条。
	raw, err := s.repo.HotQueries(ctx, tenantID, since, types.MaxSuggestionLimit)
	if err != nil {
		logger.Warnf(ctx, "[search-suggestion] hot queries unavailable: %v", err)
		return []types.SearchSuggestion{}
	}
	out := make([]types.SearchSuggestion, 0, limit)
	for _, item := range raw {
		if !prefixMatches(item.Text, prefix) {
			continue
		}
		out = append(out, item)
		if len(out) >= limit {
			break
		}
	}
	types.SortHotSuggestions(out)
	return out
}

// visibleTitleMatches returns ACL-filtered title suggestions.
//
// fail-closed：ACL 查询失败时标题桶为空。标题联想是内容暴露面，宁可少给
// 几条也不能把"猜标题探测存在性"的通道打开。
func (s *searchSuggestionService) visibleTitleMatches(
	ctx context.Context, tenantID uint64, caller types.Caller, prefix string, limit int,
) []types.SearchSuggestion {
	scopes, err := s.visibleScopes(ctx, caller)
	if err != nil {
		logger.Warnf(ctx, "[search-suggestion] scope resolution failed, titles withheld: %v", err)
		return []types.SearchSuggestion{}
	}
	// 按来源租户分组：共享空间属于另一个租户，标题查询必须带上它自己的
	// tenant_id，否则会跨租户读出别家的标题。
	byTenant := make(map[uint64][]string, 2)
	for _, scope := range scopes {
		if scope.KBID == "" {
			continue
		}
		byTenant[scope.TenantID] = append(byTenant[scope.TenantID], scope.KBID)
	}
	if len(byTenant) == 0 {
		return []types.SearchSuggestion{}
	}

	// 多取一些：ACL 会在下一层砍掉一部分。
	candidates := make([]types.SearchSuggestion, 0, limit)
	for scopeTenant, kbIDs := range byTenant {
		matches, listErr := s.repo.TitleMatches(
			ctx, scopeTenant, kbIDs, prefix, types.MaxSuggestionLimit)
		if listErr != nil {
			// 一个来源租户查询失败不该清空整份联想：跳过它，其余照常。
			logger.Warnf(ctx, "[search-suggestion] title matches unavailable for tenant %d: %v",
				scopeTenant, listErr)
			continue
		}
		candidates = append(candidates, matches...)
	}
	if len(candidates) == 0 {
		return []types.SearchSuggestion{}
	}
	if s.acl == nil {
		return truncateSuggestions(candidates, limit)
	}

	// ACL 逐来源租户判定：授权行按知识 ID 记录，与租户无关，但要按该租户
	// 下的空间集合去查，否则会把另一个租户的拒绝集混进来。
	subjects, err := s.subjects.ForCaller(ctx, caller)
	if err != nil {
		logger.Warnf(ctx, "[search-suggestion] subject expansion failed, titles withheld: %v", err)
		return []types.SearchSuggestion{}
	}
	deniedSet := make(map[string]struct{}, len(candidates))
	for scopeTenant, kbIDs := range byTenant {
		denied, aclErr := s.acl.DeniedKnowledgeIDsForSubjects(
			ctx, kbIDs, subjects, types.PermissionActionView, s.now())
		if aclErr != nil {
			logger.Warnf(ctx, "[search-suggestion] acl lookup failed for tenant %d, titles withheld: %v",
				scopeTenant, aclErr)
			return []types.SearchSuggestion{}
		}
		for _, id := range denied {
			deniedSet[id] = struct{}{}
		}
	}
	out := make([]types.SearchSuggestion, 0, limit)
	for _, candidate := range candidates {
		if _, isDenied := deniedSet[candidate.KnowledgeID]; isDenied {
			continue
		}
		out = append(out, candidate)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// glossaryMatches returns controlled-vocabulary hits for the prefix.
//
// 术语是受控词表而非文档，因此不受文档 ACL 约束：它能被搜到，说明它本来
// 就是该租户的公开词汇。术语服务不可用时返回空桶（不阻断其它三个）。
func (s *searchSuggestionService) glossaryMatches(
	ctx context.Context, tenantID uint64, prefix string, limit int,
) []types.SearchSuggestion {
	if s.glossary == nil {
		return []types.SearchSuggestion{}
	}
	terms, err := s.glossary.SearchTerms(ctx, tenantID, prefix, "")
	if err != nil {
		logger.Warnf(ctx, "[search-suggestion] glossary unavailable: %v", err)
		return []types.SearchSuggestion{}
	}
	out := make([]types.SearchSuggestion, 0, limit)
	for _, term := range terms {
		if term == nil || strings.TrimSpace(term.StandardName) == "" {
			continue
		}
		// 只收标准名：别名是给检索扩展用的，"别名词"出现在搜索建议里会让
		// 读者以为自己搜到了别的东西。
		out = append(out, types.SearchSuggestion{
			Text:  term.StandardName,
			Kind:  types.SuggestionKindGlossary,
			Label: term.Domain,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// visibleScopes resolves the (tenant, kb) pairs the caller can search in.
//
// 与全局搜索 SearchKnowledge 的取数口径一致：本租户的文档型空间 + 组织
// 共享进来的空间（§2.3）。FAQ 型空间不参与，与 SearchKnowledge 同一取舍。
func (s *searchSuggestionService) visibleScopes(
	ctx context.Context, caller types.Caller,
) ([]types.KnowledgeSearchScope, error) {
	if s.kb == nil {
		return nil, apperrors.NewInternalServerError("knowledge base service is unavailable")
	}
	scopes := make([]types.KnowledgeSearchScope, 0, 8)
	own, err := s.kb.ListKnowledgeBases(ctx)
	if err != nil {
		return nil, err
	}
	for _, kb := range own {
		if kb == nil || kb.Type != types.KnowledgeBaseTypeDocument {
			continue
		}
		// 以 KB 行自己的 tenant 为准（与全局搜索同一约定）：这一层的列表
		// 本来只含本租户的空间，但用行上的值更不容易在将来被改错。
		scopeTenant := kb.TenantID
		if scopeTenant == 0 {
			scopeTenant = caller.TenantID
		}
		scopes = append(scopes, types.KnowledgeSearchScope{
			TenantID: scopeTenant, KBID: kb.ID,
		})
	}
	if s.kbShare == nil || caller.UserID == "" {
		return scopes, nil
	}
	shared, err := s.kbShare.ListSharedKnowledgeBases(ctx, caller.TenantID, caller.Role)
	if err != nil {
		// 共享空间查询失败不阻断：本租户的空间已经拿到了，少给几条比整份
		// 建议打不开要好。
		logger.Warnf(ctx, "[search-suggestion] shared spaces unavailable: %v", err)
		return scopes, nil
	}
	for _, info := range shared {
		if info == nil || info.KnowledgeBase == nil {
			continue
		}
		if info.KnowledgeBase.Type != types.KnowledgeBaseTypeDocument {
			continue
		}
		scopes = append(scopes, types.KnowledgeSearchScope{
			TenantID: info.SourceTenantID, KBID: info.KnowledgeBase.ID,
		})
	}
	return scopes, nil
}

// truncateSuggestions caps a list.
func truncateSuggestions(items []types.SearchSuggestion, limit int) []types.SearchSuggestion {
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
}

// RecordQuery validates one query before it is written to the search log.
//
// 历史就是 search_logs 的投影，不另存一份：另建 history 表会多出一条会与
// 日志不一致的真相。检索路径写日志时已经带上 user_id，所以这个方法只做
// 校验，让调用方有一个明确的"这次查询属于该用户"的语义，而不是依赖
// "检索恰好写了日志"这种隐含契约。
func (s *searchSuggestionService) RecordQuery(
	ctx context.Context, tenantID uint64, userID, query string,
) error {
	if tenantID == 0 || userID == "" {
		return apperrors.NewUnauthorizedError("tenant and caller identity are required")
	}
	if strings.TrimSpace(query) == "" {
		return apperrors.NewBadRequestError("query must not be empty")
	}
	return nil
}

// ClearHistory removes the caller's own search history.
//
// 这里删除的是该用户自己的 search_logs 行。确实会降低质量运营的样本量，
// 但"我搜过什么"属于个人信息，用户必须能擦掉；越权风险为零（条件里带
// user_id），而且已聚合成知识缺口的历史记录不受影响（缺口是独立计数的）。
func (s *searchSuggestionService) ClearHistory(
	ctx context.Context, tenantID uint64, userID string,
) error {
	if tenantID == 0 || userID == "" {
		return apperrors.NewUnauthorizedError("tenant and caller identity are required")
	}
	return s.repo.DeleteHistory(ctx, tenantID, userID)
}

// compile-time guard.
var _ interfaces.SearchSuggestionService = (*searchSuggestionService)(nil)
