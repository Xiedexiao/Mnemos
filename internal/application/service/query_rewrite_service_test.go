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

// stubRewriteRepo serves the vocabulary.
type stubRewriteRepo struct {
	interfaces.QueryRewriteRepository

	terms     []string
	fragments map[string]string
	termsErr  error
	// requestedBases records the scope the service passed down.
	requestedBases []string
}

func (s *stubRewriteRepo) VocabularyTerms(
	_ context.Context, _ uint64, kbIDs []string, _ int,
) ([]string, error) {
	s.requestedBases = kbIDs
	return s.terms, s.termsErr
}

func (s *stubRewriteRepo) TitleFragment(
	_ context.Context, _ uint64, knowledgeID string,
) (string, error) {
	return s.fragments[knowledgeID], nil
}

func (s *stubRewriteRepo) GlossaryTerms(
	context.Context, uint64, int,
) ([]string, error) {
	return nil, nil
}

// stubRewriteKB serves the caller's spaces.
type stubRewriteKB struct {
	interfaces.KnowledgeBaseService
	bases []*types.KnowledgeBase
	err   error
}

func (s *stubRewriteKB) ListKnowledgeBases(context.Context) ([]*types.KnowledgeBase, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.bases, nil
}

// stubRewriteACL denies a fixed set.
type stubRewriteACL struct {
	interfaces.KnowledgeACLRepository
	denied []string
	err    error
}

func (s *stubRewriteACL) DeniedKnowledgeIDsForSubjects(
	context.Context, []string, []types.ACLSubject, types.PermissionAction, time.Time,
) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.denied, nil
}

func newRewriteServiceForTest(
	repo *stubRewriteRepo, acl *stubRewriteACL, kbs []*types.KnowledgeBase,
) interfaces.QueryRewriteService {
	var aclRepo interfaces.KnowledgeACLRepository
	if acl != nil {
		aclRepo = acl
	}
	svc := NewQueryRewriteService(repo, aclRepo, nil, &stubRewriteKB{bases: kbs}).(*queryRewriteService)
	svc.now = func() time.Time {
		return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	}
	return svc
}

// rewriteCallerCtx carries a viewer identity.
func rewriteCallerCtx() context.Context {
	return types.WithCaller(context.Background(), types.Caller{
		TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer,
	})
}

func rewriteKB(id string) *types.KnowledgeBase {
	return &types.KnowledgeBase{ID: id, TenantID: 7, Type: types.KnowledgeBaseTypeDocument}
}

// TestRewriteKeepsOriginalAndAddsCorrections is the core contract.
func TestRewriteKeepsOriginalAndAddsCorrections(t *testing.T) {
	repo := &stubRewriteRepo{terms: []string{"报警处理", "备件清单"}}
	svc := newRewriteServiceForTest(repo, nil, []*types.KnowledgeBase{rewriteKB("kb-1")})

	plan, err := svc.Rewrite(rewriteCallerCtx(), 7, nil, "报井处理")
	require.NoError(t, err)
	assert.Equal(t, "报井处理", plan.Original, "the original is never replaced")
	assert.Equal(t, "baojingchuli", plan.Pinyin)
	assert.Contains(t, plan.Alternatives, "报警处理")
	assert.Equal(t, []string{"kb-1"}, repo.requestedBases,
		"an unscoped request resolves the caller's visible spaces")
}

// TestRewriteReturnsPinyinCandidates covers the Latin-input path end to end.
func TestRewriteReturnsPinyinCandidates(t *testing.T) {
	repo := &stubRewriteRepo{terms: []string{"报警处理"}}
	svc := newRewriteServiceForTest(repo, nil, []*types.KnowledgeBase{rewriteKB("kb-1")})

	plan, err := svc.Rewrite(rewriteCallerCtx(), 7, nil, "baojing")
	require.NoError(t, err)
	assert.Equal(t, []string{"报警处理"}, plan.ChineseFromPinyin)
}

// TestRewriteSurvivesVocabularyFailure is the availability rule: a missing
// vocabulary degrades the plan to "original only", it must not fail the search.
func TestRewriteSurvivesVocabularyFailure(t *testing.T) {
	repo := &stubRewriteRepo{termsErr: errors.New("table locked")}
	svc := newRewriteServiceForTest(repo, nil, []*types.KnowledgeBase{rewriteKB("kb-1")})

	plan, err := svc.Rewrite(rewriteCallerCtx(), 7, nil, "报井处理")
	require.NoError(t, err)
	assert.Equal(t, "报井处理", plan.Original)
	assert.Empty(t, plan.Alternatives)
	// The pinyin of the query itself is still useful to the caller.
	assert.Equal(t, "baojingchuli", plan.Pinyin)
}

// TestRewriteWithholdsCorrectionsWhenACLFails is fail-closed: without an
// authorization verdict the candidate list is emptied rather than leaked.
func TestRewriteWithholdsCorrectionsWhenACLFails(t *testing.T) {
	repo := &stubRewriteRepo{
		terms: []string{"报警处理"},
		fragments: map[string]string{
			"hidden": "机密配方",
		},
	}
	acl := &stubRewriteACL{err: errors.New("acl down")}
	svc := newRewriteServiceForTest(repo, acl, []*types.KnowledgeBase{rewriteKB("kb-1")})

	plan, err := svc.Rewrite(rewriteCallerCtx(), 7, nil, "报井处理")
	require.NoError(t, err, "a suggestion failure must not fail the request")
	assert.Equal(t, "报井处理", plan.Original)
	assert.Empty(t, plan.Alternatives)
	assert.Empty(t, plan.Corrections)
}

// TestRewriteSubtractsDeniedFragments is the existence-protection rule.
func TestRewriteSubtractsDeniedFragments(t *testing.T) {
	repo := &stubRewriteRepo{
		terms: []string{"报警处理", "机密配方"},
		fragments: map[string]string{
			"hidden": "机密配方",
		},
	}
	acl := &stubRewriteACL{denied: []string{"hidden"}}
	svc := newRewriteServiceForTest(repo, acl, []*types.KnowledgeBase{rewriteKB("kb-1")})

	// "机密配方" is distance 1 from "机密配万" but must never be suggested,
	// because it only exists in a document the caller cannot read.
	plan, err := svc.Rewrite(rewriteCallerCtx(), 7, nil, "机密配万")
	require.NoError(t, err)
	for _, correction := range plan.Corrections {
		assert.NotEqual(t, "机密配方", correction.Suggestion,
			"a fragment from a denied document must not surface")
	}
	for _, alternative := range plan.Alternatives {
		assert.NotContains(t, alternative, "机密配方")
	}
}

// TestRewriteKeepsVisibleFragments is the positive control: an unrelated deny
// does not empty the vocabulary.
func TestRewriteKeepsVisibleFragments(t *testing.T) {
	repo := &stubRewriteRepo{
		terms:     []string{"报警处理"},
		fragments: map[string]string{"hidden": "机密配方"},
	}
	acl := &stubRewriteACL{denied: []string{"hidden"}}
	svc := newRewriteServiceForTest(repo, acl, []*types.KnowledgeBase{rewriteKB("kb-1")})

	plan, err := svc.Rewrite(rewriteCallerCtx(), 7, nil, "报井处理")
	require.NoError(t, err)
	assert.Contains(t, plan.Alternatives, "报警处理")
}

// TestRewriteSkipsACLWhenNoBases proves the ACL gate is not called needlessly.
func TestRewriteSkipsACLWhenNoBases(t *testing.T) {
	repo := &stubRewriteRepo{terms: []string{"报警处理"}}
	acl := &stubRewriteACL{err: errors.New("must not be called")}
	svc := newRewriteServiceForTest(repo, acl, nil)

	plan, err := svc.Rewrite(rewriteCallerCtx(), 7, nil, "报井处理")
	require.NoError(t, err)
	assert.Contains(t, plan.Alternatives, "报警处理")
}

// TestRewriteEmptyQueryReturnsEmptyPlan is a no-op, not an error.
func TestRewriteEmptyQueryReturnsEmptyPlan(t *testing.T) {
	svc := newRewriteServiceForTest(&stubRewriteRepo{}, nil, nil)
	plan, err := svc.Rewrite(rewriteCallerCtx(), 7, nil, "   ")
	require.NoError(t, err)
	assert.Equal(t, "", plan.Original)
	assert.Empty(t, plan.Alternatives)
}

// TestRewriteRequiresTenant refuses a missing tenant context.
func TestRewriteRequiresTenant(t *testing.T) {
	svc := newRewriteServiceForTest(&stubRewriteRepo{}, nil, nil)
	_, err := svc.Rewrite(context.Background(), 0, nil, "报警")
	require.Error(t, err)
}

// TestRewriteScopesToCallerTenant drops spaces from another tenant.
func TestRewriteScopesToCallerTenant(t *testing.T) {
	repo := &stubRewriteRepo{terms: []string{"报警处理"}}
	otherTenant := &types.KnowledgeBase{
		ID: "kb-other", TenantID: 9, Type: types.KnowledgeBaseTypeDocument,
	}
	svc := newRewriteServiceForTest(repo, nil, []*types.KnowledgeBase{otherTenant})

	_, err := svc.Rewrite(rewriteCallerCtx(), 7, nil, "报井处理")
	require.NoError(t, err)
	assert.Empty(t, repo.requestedBases,
		"another tenant's space must not become a candidate source")
}

// TestRewriteIgnoresFAQSpaces matches the retrieval scope rule.
func TestRewriteIgnoresFAQSpaces(t *testing.T) {
	repo := &stubRewriteRepo{terms: []string{"报警处理"}}
	faq := &types.KnowledgeBase{ID: "kb-faq", TenantID: 7, Type: types.KnowledgeBaseTypeFAQ}
	svc := newRewriteServiceForTest(repo, nil, []*types.KnowledgeBase{faq})

	_, err := svc.Rewrite(rewriteCallerCtx(), 7, nil, "报井处理")
	require.NoError(t, err)
	assert.Empty(t, repo.requestedBases)
}

// TestRewriteUsesExplicitBases skips scope resolution when the caller narrows.
func TestRewriteUsesExplicitBases(t *testing.T) {
	repo := &stubRewriteRepo{terms: []string{"报警处理"}}
	svc := newRewriteServiceForTest(repo, nil, []*types.KnowledgeBase{rewriteKB("kb-1")})

	_, err := svc.Rewrite(rewriteCallerCtx(), 7, []string{"kb-9"}, "报井处理")
	require.NoError(t, err)
	assert.Equal(t, []string{"kb-9"}, repo.requestedBases)
}
