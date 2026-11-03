package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubEmbedPushRepo is an in-memory push store.
type stubEmbedPushRepo struct {
	interfaces.EmbedPushRepository

	rules    []*types.EmbedPushRule
	tagMatch map[string][]types.EmbedPushKnowledgeMatch
	prefs    map[string]*types.EmbedPushPreference
	events   []*types.EmbedPushEvent
	nextID   int
}

func newStubEmbedPushRepo() *stubEmbedPushRepo {
	return &stubEmbedPushRepo{
		tagMatch: map[string][]types.EmbedPushKnowledgeMatch{},
		prefs:    map[string]*types.EmbedPushPreference{},
	}
}

func (s *stubEmbedPushRepo) CreateRule(_ context.Context, rule *types.EmbedPushRule) error {
	s.nextID++
	rule.ID = "rule-" + itoaSmall(s.nextID)
	s.rules = append(s.rules, rule)
	return nil
}

func (s *stubEmbedPushRepo) GetRule(
	_ context.Context, _ uint64, ruleID string,
) (*types.EmbedPushRule, error) {
	for _, rule := range s.rules {
		if rule.ID == ruleID {
			return rule, nil
		}
	}
	return nil, errors.New("rule not found")
}

func (s *stubEmbedPushRepo) ListRules(
	_ context.Context, _ uint64, enabledOnly bool,
) ([]*types.EmbedPushRule, error) {
	out := make([]*types.EmbedPushRule, 0, len(s.rules))
	for _, rule := range s.rules {
		if enabledOnly && !rule.Enabled {
			continue
		}
		out = append(out, rule)
	}
	return out, nil
}

func (s *stubEmbedPushRepo) UpdateRule(_ context.Context, rule *types.EmbedPushRule) error {
	for index, existing := range s.rules {
		if existing.ID == rule.ID {
			s.rules[index] = rule
			return nil
		}
	}
	return errors.New("rule not found")
}

func (s *stubEmbedPushRepo) DeleteRule(_ context.Context, _ uint64, ruleID string) error {
	kept := s.rules[:0]
	for _, rule := range s.rules {
		if rule.ID != ruleID {
			kept = append(kept, rule)
		}
	}
	s.rules = kept
	return nil
}

func (s *stubEmbedPushRepo) MatchKnowledgeByTags(
	_ context.Context, _ uint64, tagNames []string,
) ([]types.EmbedPushKnowledgeMatch, error) {
	seen := map[string]bool{}
	var out []types.EmbedPushKnowledgeMatch
	for _, name := range tagNames {
		for _, match := range s.tagMatch[strings.ToLower(name)] {
			if seen[match.KnowledgeID] {
				continue
			}
			seen[match.KnowledgeID] = true
			out = append(out, match)
		}
	}
	return out, nil
}

func (s *stubEmbedPushRepo) GetPreference(
	_ context.Context, tenantID uint64, userID string,
) (*types.EmbedPushPreference, error) {
	return s.prefs[itoaSmall(int(tenantID))+"|"+userID], nil
}

func (s *stubEmbedPushRepo) UpsertPreference(
	_ context.Context, preference *types.EmbedPushPreference,
) error {
	s.prefs[itoaSmall(int(preference.TenantID))+"|"+preference.UserID] = preference
	return nil
}

func (s *stubEmbedPushRepo) CreateEvent(_ context.Context, event *types.EmbedPushEvent) error {
	event.ID = "event-" + itoaSmall(len(s.events)+1)
	s.events = append(s.events, event)
	return nil
}

func (s *stubEmbedPushRepo) ListDismissals(
	_ context.Context, tenantID uint64, userID string, since time.Time,
) ([]types.EmbedPushDismissal, error) {
	var out []types.EmbedPushDismissal
	for _, event := range s.events {
		if event.TenantID != tenantID || event.UserID != userID {
			continue
		}
		if event.EventType != string(types.EmbedPushEventDismiss) {
			continue
		}
		if event.CreatedAt.Before(since) {
			continue
		}
		out = append(out, types.EmbedPushDismissal{
			RuleID: event.RuleID, KnowledgeID: event.KnowledgeID,
		})
	}
	return out, nil
}

func (s *stubEmbedPushRepo) RuleStats(
	_ context.Context, tenantID uint64, since time.Time,
) ([]types.EmbedPushRuleStats, error) {
	since = since.UTC()
	counts := map[string]*types.EmbedPushRuleStats{}
	for _, rule := range s.rules {
		if rule.TenantID == tenantID {
			counts[rule.ID] = &types.EmbedPushRuleStats{RuleID: rule.ID, RuleName: rule.Name}
		}
	}
	for _, event := range s.events {
		stat, ok := counts[event.RuleID]
		if !ok || event.CreatedAt.Before(since) {
			continue
		}
		switch types.EmbedPushEventType(event.EventType) {
		case types.EmbedPushEventImpression:
			stat.Impressions++
		case types.EmbedPushEventClick:
			stat.Clicks++
		case types.EmbedPushEventDismiss:
			stat.Dismissals++
		}
	}
	out := make([]types.EmbedPushRuleStats, 0, len(counts))
	for _, rule := range s.rules {
		if stat, ok := counts[rule.ID]; ok {
			out = append(out, *stat)
		}
	}
	return out, nil
}

// stubPushKnowledgeRepo serves the knowledge reads the push service needs.
type stubPushKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	rows         map[string]*types.Knowledge
	excluded     []string
	folderDenied map[string][]string
	lifecycleErr error
}

func (s *stubPushKnowledgeRepo) GetKnowledgeBatch(
	_ context.Context, _ uint64, ids []string,
) ([]*types.Knowledge, error) {
	var out []*types.Knowledge
	for _, id := range ids {
		if knowledge, ok := s.rows[id]; ok {
			out = append(out, knowledge)
		}
	}
	return out, nil
}

func (s *stubPushKnowledgeRepo) GetLifecycleExcludedKnowledgeIDs(
	_ context.Context, _ []string, _ time.Time,
) ([]string, error) {
	if s.lifecycleErr != nil {
		return nil, s.lifecycleErr
	}
	return s.excluded, nil
}

func (s *stubPushKnowledgeRepo) ListKnowledgeIDsUnderFolderPaths(
	_ context.Context, kbID string, _ []string,
) ([]string, error) {
	return s.folderDenied[kbID], nil
}

// stubPushACL denies a fixed set, or fails when err is set.
type stubPushACL struct {
	interfaces.KnowledgeACLService
	denied []string
	err    error
}

func (s *stubPushACL) DeniedKnowledgeIDs(
	_ context.Context, _ []string, _ string, _ types.PermissionAction,
) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.denied, nil
}

// stubPushFolderACL returns fixed grants per KB.
type stubPushFolderACL struct {
	interfaces.FolderACLRepository
	grants map[string][]*types.FolderACL
}

func (s *stubPushFolderACL) ListByKnowledgeBase(
	_ context.Context, _ uint64, kbID string,
) ([]*types.FolderACL, error) {
	return s.grants[kbID], nil
}

// embedPushFixture assembles a service with stubbed dependencies.
type embedPushFixture struct {
	svc        *embedPushService
	repo       *stubEmbedPushRepo
	knowledges *stubPushKnowledgeRepo
	acl        *stubPushACL
}

func newEmbedPushFixture() *embedPushFixture {
	repo := newStubEmbedPushRepo()
	knowledges := &stubPushKnowledgeRepo{
		rows: map[string]*types.Knowledge{
			"k1": {ID: "k1", KnowledgeBaseID: "kb1", Title: "E45 换型 SOP", Description: "换型步骤"},
			"k2": {ID: "k2", KnowledgeBaseID: "kb1", Title: "锁具安全规程", Description: "断电挂牌"},
			"k3": {ID: "k3", KnowledgeBaseID: "kb1", Title: "已禁用规则指向", Description: ""},
			"k4": {ID: "k4", KnowledgeBaseID: "kb1", Title: "机密工艺卡", Description: "机密"},
			"k5": {ID: "k5", KnowledgeBaseID: "kb1", Title: "过期版本", Description: "旧版"},
			"k6": {ID: "k6", KnowledgeBaseID: "kb1", Title: "供应商目录内文档", Description: "不可见"},
		},
		excluded:     []string{"k5"},
		folderDenied: map[string][]string{"kb1": {"k6"}},
	}
	acl := &stubPushACL{denied: []string{"k4"}}
	folderACL := &stubPushFolderACL{grants: map[string][]*types.FolderACL{
		"kb1": {{SubjectID: "u1", Effect: string(types.KnowledgeACLEffectDeny), FolderPath: "supplier"}},
	}}
	svc := &embedPushService{
		repo:       repo,
		knowledges: knowledges,
		acl:        acl,
		folderACL:  folderACL,
		now:        func() time.Time { return time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC) },
	}
	return &embedPushFixture{svc: svc, repo: repo, knowledges: knowledges, acl: acl}
}

func embedWorkOrderContext() types.EmbedPushContext {
	return types.EmbedPushContext{
		System: "mes", Scene: "work_order", EntityType: "equipment", EntityID: "E45",
		Attributes: map[string]string{"line": "A"},
	}
}

func addEmbedRules(t *testing.T, repo *stubEmbedPushRepo) {
	t.Helper()
	repo.rules = []*types.EmbedPushRule{
		{
			ID: "rule-pin", TenantID: 7, Name: "E45 指定 SOP", Enabled: true, Priority: 10,
			System: "mes", Scene: "work_order", EntityType: "equipment", EntityValue: "E45",
			KnowledgeIDs: types.JSON(`["k1"]`), Reason: "该设备的换型步骤",
		},
		{
			ID: "rule-tag", TenantID: 7, Name: "工单安全规程", Enabled: true, Priority: 5,
			Scene: "work_order", TagNames: types.JSON(`["安全规程"]`),
			Reason: "工单相关安全要求",
		},
		{
			ID: "rule-disabled", TenantID: 7, Name: "停用规则", Enabled: false, Priority: 99,
			KnowledgeIDs: types.JSON(`["k3"]`),
		},
		{
			ID: "rule-other-system", TenantID: 7, Name: "CRM 规则", Enabled: true, Priority: 50,
			System: "crm", KnowledgeIDs: types.JSON(`["k3"]`),
		},
		{
			ID: "rule-attr", TenantID: 7, Name: "A 线专用", Enabled: true, Priority: 1,
			Attributes: types.JSON(`{"line":"B"}`), KnowledgeIDs: types.JSON(`["k3"]`),
		},
	}
	repo.tagMatch["安全规程"] = []types.EmbedPushKnowledgeMatch{
		{KnowledgeID: "k2", KnowledgeBaseID: "kb1", TagName: "安全规程"},
		{KnowledgeID: "k4", KnowledgeBaseID: "kb1", TagName: "安全规程"},
		{KnowledgeID: "k5", KnowledgeBaseID: "kb1", TagName: "安全规程"},
		{KnowledgeID: "k6", KnowledgeBaseID: "kb1", TagName: "安全规程"},
	}
}

// TestEmbedPushResolveFiltersAndOrders is the core §15.5 contract: the card
// list is rule-ordered and has already passed lifecycle, document ACL and
// folder ACL.
func TestEmbedPushResolveFiltersAndOrders(t *testing.T) {
	fixture := newEmbedPushFixture()
	addEmbedRules(t, fixture.repo)

	result, err := fixture.svc.Resolve(context.Background(), 7, "u1", embedWorkOrderContext(), 3)
	require.NoError(t, err)
	assert.True(t, result.Enabled)
	require.Len(t, result.Items, 2, "expired, denied, folder-denied and disabled-rule cards are gone")
	assert.Equal(t, "k1", result.Items[0].KnowledgeID, "higher-priority rule wins")
	assert.Equal(t, "rule-pin", result.Items[0].RuleID)
	assert.Equal(t, "该设备的换型步骤", result.Items[0].Reason)
	assert.Equal(t, "k2", result.Items[1].KnowledgeID)
	assert.Equal(t, "安全规程", result.Items[1].MatchedTag)

	require.Len(t, fixture.repo.events, 2, "every shown card produces an impression")
	assert.Equal(t, string(types.EmbedPushEventImpression), fixture.repo.events[0].EventType)
}

// TestEmbedPushDismissSuppressesCard covers §15.5's "不打断工作流": a card the
// user dismissed does not come back on the next push.
func TestEmbedPushDismissSuppressesCard(t *testing.T) {
	fixture := newEmbedPushFixture()
	addEmbedRules(t, fixture.repo)
	ctx := context.Background()

	_, err := fixture.svc.Resolve(ctx, 7, "u1", embedWorkOrderContext(), 3)
	require.NoError(t, err)
	require.NoError(t, fixture.svc.RecordEvent(ctx, 7, "u1",
		"rule-pin", "k1", types.EmbedPushEventDismiss, embedWorkOrderContext()))

	result, err := fixture.svc.Resolve(ctx, 7, "u1", embedWorkOrderContext(), 3)
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	assert.Equal(t, "k2", result.Items[0].KnowledgeID)
}

// TestEmbedPushPreferenceOff covers the opt-out: no cards, no events.
func TestEmbedPushPreferenceOff(t *testing.T) {
	fixture := newEmbedPushFixture()
	addEmbedRules(t, fixture.repo)
	ctx := context.Background()

	preference, err := fixture.svc.SetPreference(ctx, 7, "u1", false)
	require.NoError(t, err)
	assert.False(t, preference.Enabled)

	result, err := fixture.svc.Resolve(ctx, 7, "u1", embedWorkOrderContext(), 3)
	require.NoError(t, err)
	assert.False(t, result.Enabled)
	assert.Empty(t, result.Items)
	assert.Equal(t, "disabled", result.Suppressed)
	assert.Empty(t, fixture.repo.events, "a disabled user is not even tracked")

	// An event after opting out is ignored rather than resurrecting the panel.
	require.NoError(t, fixture.svc.RecordEvent(ctx, 7, "u1",
		"rule-pin", "k1", types.EmbedPushEventClick, embedWorkOrderContext()))
	assert.Empty(t, fixture.repo.events)
}

// TestEmbedPushACLFailureFailsClosed is the security property: an ACL query
// failure must not fall back to pushing everything.
func TestEmbedPushACLFailureFailsClosed(t *testing.T) {
	fixture := newEmbedPushFixture()
	addEmbedRules(t, fixture.repo)
	fixture.acl.err = errors.New("acl db down")

	_, err := fixture.svc.Resolve(context.Background(), 7, "u1", embedWorkOrderContext(), 3)
	require.Error(t, err)
	assert.Empty(t, fixture.repo.events, "nothing is shown when permission cannot be proven")
}

// TestEmbedPushNoMatchingRule covers the empty-but-normal case.
func TestEmbedPushNoMatchingRule(t *testing.T) {
	fixture := newEmbedPushFixture()
	addEmbedRules(t, fixture.repo)
	result, err := fixture.svc.Resolve(context.Background(), 7, "u1", types.EmbedPushContext{
		System: "oa", Scene: "notice",
	}, 3)
	require.NoError(t, err)
	assert.True(t, result.Enabled)
	assert.Empty(t, result.Items)
	assert.Equal(t, "no_match", result.Suppressed)
}

// TestEmbedPushRuleCRUDValidation covers the admin rule shape.
func TestEmbedPushRuleCRUDValidation(t *testing.T) {
	fixture := newEmbedPushFixture()
	ctx := context.Background()

	_, err := fixture.svc.CreateRule(ctx, 7, "admin", types.EmbedPushRuleInput{Name: "空规则"})
	require.Error(t, err, "a rule with no knowledge source can never match")

	rule, err := fixture.svc.CreateRule(ctx, 7, "admin", types.EmbedPushRuleInput{
		Name: " E45 工艺 ", System: "MES ", Scene: " work_order ",
		TagNames: []string{"工艺", " 工艺 "}, Reason: "换型前必读",
	})
	require.NoError(t, err)
	assert.Equal(t, "E45 工艺", rule.Name)
	assert.Equal(t, "mes", rule.System, "matching fields are normalized")
	assert.Equal(t, []string{"工艺"}, rule.EmbedPushRuleTagNames())

	// Clearing both sources on update is rejected; keeping one is fine.
	_, err = fixture.svc.UpdateRule(ctx, 7, rule.ID, "admin", types.EmbedPushRuleInput{
		TagNames: []string{},
	})
	require.Error(t, err)

	updated, err := fixture.svc.UpdateRule(ctx, 7, rule.ID, "admin", types.EmbedPushRuleInput{
		TagNames: []string{}, KnowledgeIDs: []string{"k1"}, Enabled: func() *bool { v := false; return &v }(),
	})
	require.NoError(t, err)
	assert.Empty(t, updated.EmbedPushRuleTagNames())
	assert.Equal(t, []string{"k1"}, updated.EmbedPushRuleKnowledgeIDs())
	assert.False(t, updated.Enabled)

	rules, err := fixture.svc.ListRules(ctx, 7)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	require.NoError(t, fixture.svc.DeleteRule(ctx, 7, rule.ID, "admin"))
	rules, err = fixture.svc.ListRules(ctx, 7)
	require.NoError(t, err)
	assert.Empty(t, rules)
}

// TestEmbedPushRuleStats covers the interaction counters.
func TestEmbedPushRuleStats(t *testing.T) {
	fixture := newEmbedPushFixture()
	addEmbedRules(t, fixture.repo)
	ctx := context.Background()

	_, err := fixture.svc.Resolve(ctx, 7, "u1", embedWorkOrderContext(), 3)
	require.NoError(t, err)
	require.NoError(t, fixture.svc.RecordEvent(ctx, 7, "u1",
		"rule-pin", "k1", types.EmbedPushEventClick, embedWorkOrderContext()))
	require.NoError(t, fixture.svc.RecordEvent(ctx, 7, "u1",
		"rule-tag", "k2", types.EmbedPushEventDismiss, embedWorkOrderContext()))

	stats, err := fixture.svc.RuleStats(ctx, 7, 30)
	require.NoError(t, err)
	byRule := map[string]types.EmbedPushRuleStats{}
	for _, stat := range stats {
		byRule[stat.RuleID] = stat
	}
	require.Contains(t, byRule, "rule-pin")
	assert.EqualValues(t, 1, byRule["rule-pin"].Impressions)
	assert.EqualValues(t, 1, byRule["rule-pin"].Clicks)
	require.Contains(t, byRule, "rule-tag")
	assert.EqualValues(t, 1, byRule["rule-tag"].Impressions)
	assert.EqualValues(t, 1, byRule["rule-tag"].Dismissals)
	// A rule with no traffic still appears with zeros.
	require.Contains(t, byRule, "rule-disabled")
	assert.Zero(t, byRule["rule-disabled"].Impressions)
}
