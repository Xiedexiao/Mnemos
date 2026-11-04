package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubSuggestionRepo serves the suggestion rows.
type stubSuggestionRepo struct {
	interfaces.SearchSuggestionRepository

	history  []types.SearchSuggestion
	hot      []types.SearchSuggestion
	titles   []types.SearchSuggestion
	byTenant map[uint64][]types.SearchSuggestion
	// requestedPrefixes records what reached the repository, so the "empty
	// prefix means no lookup" rule can be asserted.
	requestedPrefixes []string
	requestedKBs      [][]string

	historyErr error
	hotErr     error
	hotCalls   int
	titlesErr  error
	deletedFor []string
}

func (s *stubSuggestionRepo) RecentQueries(
	_ context.Context, _ uint64, _ string, _ int,
) ([]types.SearchSuggestion, error) {
	return s.history, s.historyErr
}

func (s *stubSuggestionRepo) HotQueries(
	_ context.Context, _ uint64, _ time.Time, _ int,
) ([]types.SearchSuggestion, error) {
	s.hotCalls++
	return s.hot, s.hotErr
}

func (s *stubSuggestionRepo) TitleMatches(
	_ context.Context, tenantID uint64, kbIDs []string, prefix string, _ int,
) ([]types.SearchSuggestion, error) {
	s.requestedPrefixes = append(s.requestedPrefixes, prefix)
	s.requestedKBs = append(s.requestedKBs, kbIDs)
	if s.titlesErr != nil {
		return nil, s.titlesErr
	}
	if s.byTenant != nil {
		return s.byTenant[tenantID], nil
	}
	return s.titles, nil
}

func (s *stubSuggestionRepo) DeleteHistory(
	_ context.Context, _ uint64, userID string,
) error {
	s.deletedFor = append(s.deletedFor, userID)
	return nil
}

// stubSuggestionKB serves the caller's knowledge bases.
type stubSuggestionKB struct {
	interfaces.KnowledgeBaseService
	bases []*types.KnowledgeBase
	err   error
}

func (s *stubSuggestionKB) ListKnowledgeBases(context.Context) ([]*types.KnowledgeBase, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.bases, nil
}

// stubSuggestionShares serves org-shared knowledge bases.
type stubSuggestionShares struct {
	interfaces.KBShareService
	shared []*types.SharedKnowledgeBaseInfo
	err    error
}

func (s *stubSuggestionShares) ListSharedKnowledgeBases(
	context.Context, uint64, types.TenantRole,
) ([]*types.SharedKnowledgeBaseInfo, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.shared, nil
}

// stubSuggestionGlossary serves the controlled vocabulary.
type stubSuggestionGlossary struct {
	interfaces.GlossaryService
	terms []*types.GlossaryTerm
	err   error
}

func (s *stubSuggestionGlossary) SearchTerms(
	context.Context, uint64, string, string,
) ([]*types.GlossaryTerm, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.terms, nil
}

// stubSuggestionACL denies a fixed set.
type stubSuggestionACL struct {
	interfaces.KnowledgeACLRepository
	denied []string
	err    error
}

func (s *stubSuggestionACL) DeniedKnowledgeIDsForSubjects(
	context.Context, []string, []types.ACLSubject, types.PermissionAction, time.Time,
) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.denied, nil
}

// newSuggestionServiceForTest wires the service against stubs.
func newSuggestionServiceForTest(
	repo *stubSuggestionRepo,
	acl *stubSuggestionACL,
	kbs []*types.KnowledgeBase,
	shared []*types.SharedKnowledgeBaseInfo,
	glossary []*types.GlossaryTerm,
) interfaces.SearchSuggestionService {
	var aclRepo interfaces.KnowledgeACLRepository
	if acl != nil {
		aclRepo = acl
	}
	var shareSvc interfaces.KBShareService
	if shared != nil {
		shareSvc = &stubSuggestionShares{shared: shared}
	}
	var glossarySvc interfaces.GlossaryService
	if glossary != nil {
		glossarySvc = &stubSuggestionGlossary{terms: glossary}
	}
	svc := NewSearchSuggestionService(
		repo, aclRepo, nil, &stubSuggestionKB{bases: kbs}, shareSvc, glossarySvc,
	).(*searchSuggestionService)
	svc.now = func() time.Time {
		return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	}
	return svc
}

// suggestionCallerCtx carries a viewer identity.
func suggestionCallerCtx() context.Context {
	return types.WithCaller(context.Background(), types.Caller{
		TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer,
	})
}

// docBase builds one document-type knowledge base.
func docBase(id string, tenantID uint64) *types.KnowledgeBase {
	return &types.KnowledgeBase{
		ID: id, TenantID: tenantID, Type: types.KnowledgeBaseTypeDocument,
	}
}

// TestSuggestEmptyPrefixSkipsRemoteLookups is the "box just opened" path: no
// title or term lookup until the reader has typed enough.
func TestSuggestEmptyPrefixSkipsRemoteLookups(t *testing.T) {
	repo := &stubSuggestionRepo{
		history: []types.SearchSuggestion{{Text: "上次搜的", Kind: types.SuggestionKindHistory}},
		hot:     []types.SearchSuggestion{{Text: "大家都在搜", Kind: types.SuggestionKindHot, Count: 9}},
	}
	svc := newSuggestionServiceForTest(repo, nil, []*types.KnowledgeBase{docBase("kb-1", 7)}, nil, nil)

	bundle, err := svc.Suggest(suggestionCallerCtx(), 7, "   ", 0, 0)
	require.NoError(t, err)
	require.Len(t, bundle.History, 1)
	require.Len(t, bundle.Hot, 1)
	assert.Empty(t, bundle.Titles)
	assert.Empty(t, bundle.GlossaryTerms)
	assert.Empty(t, repo.requestedPrefixes, "an empty prefix must not query titles")
	assert.Equal(t, types.DefaultHotQueryDays, bundle.HotWindowDays)
}

// TestSuggestShortPrefixSkipsSuggestions keeps single-character prefixes from
// dumping the vocabulary.
func TestSuggestShortPrefixSkipsSuggestions(t *testing.T) {
	repo := &stubSuggestionRepo{
		titles: []types.SearchSuggestion{{Text: "E45 报警", Kind: types.SuggestionKindTitle}},
	}
	svc := newSuggestionServiceForTest(repo, nil, []*types.KnowledgeBase{docBase("kb-1", 7)}, nil, nil)

	bundle, err := svc.Suggest(suggestionCallerCtx(), 7, "e", 0, 0)
	require.NoError(t, err)
	assert.Empty(t, bundle.Titles, "one rune is too short to suggest on")
	assert.Empty(t, repo.requestedPrefixes)
}

// TestSuggestDropsDeniedTitles is the §12.6 rule: a title the caller cannot
// open must not appear in autocomplete.
func TestSuggestDropsDeniedTitles(t *testing.T) {
	repo := &stubSuggestionRepo{
		titles: []types.SearchSuggestion{
			{Text: "E45 报警处理", Kind: types.SuggestionKindTitle, KnowledgeID: "visible"},
			{Text: "E45 机密配方", Kind: types.SuggestionKindTitle, KnowledgeID: "secret"},
		},
	}
	acl := &stubSuggestionACL{denied: []string{"secret"}}
	svc := newSuggestionServiceForTest(
		repo, acl, []*types.KnowledgeBase{docBase("kb-1", 7)}, nil, nil)

	bundle, err := svc.Suggest(suggestionCallerCtx(), 7, "e45", 0, 0)
	require.NoError(t, err)
	require.Len(t, bundle.Titles, 1)
	assert.Equal(t, "visible", bundle.Titles[0].KnowledgeID)
}

// TestSuggestWithholdsTitlesOnACLError is fail-closed: a broken ACL lookup
// empties the title bucket instead of returning everything.
func TestSuggestWithholdsTitlesOnACLError(t *testing.T) {
	repo := &stubSuggestionRepo{
		titles: []types.SearchSuggestion{
			{Text: "E45 报警处理", Kind: types.SuggestionKindTitle, KnowledgeID: "visible"},
		},
	}
	acl := &stubSuggestionACL{err: errors.New("acl down")}
	svc := newSuggestionServiceForTest(
		repo, acl, []*types.KnowledgeBase{docBase("kb-1", 7)}, nil, nil)

	bundle, err := svc.Suggest(suggestionCallerCtx(), 7, "e45", 0, 0)
	require.NoError(t, err, "a suggestion failure must not fail the request")
	assert.Empty(t, bundle.Titles, "ACL failure withholds titles rather than leaking them")
}

// TestSuggestWithholdsHotWithoutVisibleSpaces covers the ranking leak: a reader
// with no space in this tenant gets no hot list at all.
//
// 这是 §12.6 "排序不泄露"的边界形态：热门榜是本租户的行为聚合，一个在本
// 租户里一个空间都看不到的读者（例如只靠共享空间进来的外部同事）不应该
// 拿到"这里的人都在搜什么"。
func TestSuggestWithholdsHotWithoutVisibleSpaces(t *testing.T) {
	repo := &stubSuggestionRepo{
		hot: []types.SearchSuggestion{{Text: "内部高频词", Kind: types.SuggestionKindHot, Count: 50}},
	}
	svc := newSuggestionServiceForTest(repo, nil, nil, nil, nil)

	bundle, err := svc.Suggest(suggestionCallerCtx(), 7, "", 0, 0)
	require.NoError(t, err)
	assert.Empty(t, bundle.Hot)
	assert.EqualValues(t, 0, repo.hotCalls, "the hot table is not even queried")
}

// TestSuggestKeepsHotWithVisibleSpace is the positive control for the test
// above: owning one space in the tenant is enough.
func TestSuggestKeepsHotWithVisibleSpace(t *testing.T) {
	repo := &stubSuggestionRepo{
		hot: []types.SearchSuggestion{
			{Text: "e45 报警", Kind: types.SuggestionKindHot, Count: 5},
			{Text: "备件清单", Kind: types.SuggestionKindHot, Count: 9},
		},
	}
	svc := newSuggestionServiceForTest(
		repo, nil, []*types.KnowledgeBase{docBase("kb-1", 7)}, nil, nil)

	bundle, err := svc.Suggest(suggestionCallerCtx(), 7, "e45", 0, 0)
	require.NoError(t, err)
	require.Len(t, bundle.Hot, 1, "the hot list is filtered by the typed prefix")
	assert.Equal(t, "e45 报警", bundle.Hot[0].Text)
}

// TestSuggestScopesTitlesToSharedTenants proves shared spaces are searched with
// their own tenant id, not the caller's.
func TestSuggestScopesTitlesToSharedTenants(t *testing.T) {
	repo := &stubSuggestionRepo{
		byTenant: map[uint64][]types.SearchSuggestion{
			7: {{Text: "本租户的 E45", Kind: types.SuggestionKindTitle, KnowledgeID: "own"}},
			9: {{Text: "共享的 E45", Kind: types.SuggestionKindTitle, KnowledgeID: "shared"}},
		},
	}
	shared := []*types.SharedKnowledgeBaseInfo{
		{SourceTenantID: 9, KnowledgeBase: docBase("kb-shared", 9)},
	}
	svc := newSuggestionServiceForTest(
		repo, nil, []*types.KnowledgeBase{docBase("kb-1", 7)}, shared, nil)

	bundle, err := svc.Suggest(suggestionCallerCtx(), 7, "e45", 0, 0)
	require.NoError(t, err)
	require.Len(t, bundle.Titles, 2)
	// 每个来源租户各自一次查询：共享空间必须用它自己的 tenant_id。
	require.Len(t, repo.requestedKBs, 2)
	assert.Contains(t, repo.requestedKBs, []string{"kb-1"})
	assert.Contains(t, repo.requestedKBs, []string{"kb-shared"})
}

// TestSuggestReturnsGlossaryTerms covers the vocabulary bucket.
func TestSuggestReturnsGlossaryTerms(t *testing.T) {
	terms := []*types.GlossaryTerm{
		{StandardName: "E45", Domain: "设备"},
		{StandardName: "", Domain: "空名应被丢弃"},
	}
	svc := newSuggestionServiceForTest(
		&stubSuggestionRepo{}, nil,
		[]*types.KnowledgeBase{docBase("kb-1", 7)}, nil, terms)

	bundle, err := svc.Suggest(suggestionCallerCtx(), 7, "e45", 0, 0)
	require.NoError(t, err)
	require.Len(t, bundle.GlossaryTerms, 1)
	assert.Equal(t, "E45", bundle.GlossaryTerms[0].Text)
	assert.Equal(t, types.SuggestionKindGlossary, bundle.GlossaryTerms[0].Kind)
	assert.Equal(t, "设备", bundle.GlossaryTerms[0].Label)
}

// TestSuggestClampsLimitAndWindow keeps unbounded inputs from reaching SQL.
func TestSuggestClampsLimitAndWindow(t *testing.T) {
	repo := &stubSuggestionRepo{}
	svc := newSuggestionServiceForTest(
		repo, nil, []*types.KnowledgeBase{docBase("kb-1", 7)}, nil, nil)

	bundle, err := svc.Suggest(suggestionCallerCtx(), 7, "", 999, 9999)
	require.NoError(t, err)
	assert.Equal(t, 365, bundle.HotWindowDays)
}

// TestSuggestRequiresIdentity refuses anonymous callers.
func TestSuggestRequiresIdentity(t *testing.T) {
	svc := newSuggestionServiceForTest(&stubSuggestionRepo{}, nil, nil, nil, nil)
	_, err := svc.Suggest(context.Background(), 7, "e45", 0, 0)
	require.Error(t, err)
}

// TestSuggestSurvivesHistoryFailure keeps one broken bucket from failing the
// whole request.
func TestSuggestSurvivesHistoryFailure(t *testing.T) {
	repo := &stubSuggestionRepo{
		historyErr: errors.New("history table down"),
		hot:        []types.SearchSuggestion{{Text: "热门", Kind: types.SuggestionKindHot, Count: 3}},
	}
	svc := newSuggestionServiceForTest(
		repo, nil, []*types.KnowledgeBase{docBase("kb-1", 7)}, nil, nil)

	bundle, err := svc.Suggest(suggestionCallerCtx(), 7, "", 0, 0)
	require.NoError(t, err)
	assert.Empty(t, bundle.History)
	assert.Len(t, bundle.Hot, 1)
}

// TestClearHistoryUsesCallerIdentity proves the delete is scoped to the caller.
func TestClearHistoryUsesCallerIdentity(t *testing.T) {
	repo := &stubSuggestionRepo{}
	svc := newSuggestionServiceForTest(repo, nil, nil, nil, nil)
	require.NoError(t, svc.ClearHistory(suggestionCallerCtx(), 7, "reader"))
	assert.Equal(t, []string{"reader"}, repo.deletedFor)
}

// TestClearHistoryRefusesMissingIdentity stops an identity-less delete.
func TestClearHistoryRefusesMissingIdentity(t *testing.T) {
	repo := &stubSuggestionRepo{}
	svc := newSuggestionServiceForTest(repo, nil, nil, nil, nil)
	require.Error(t, svc.ClearHistory(suggestionCallerCtx(), 7, ""))
	assert.Empty(t, repo.deletedFor, "a missing identity must not reach the delete")
}
