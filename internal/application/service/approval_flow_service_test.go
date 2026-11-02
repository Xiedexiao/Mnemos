package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

func flowWithRules(t *testing.T, mode string, rules types.ApprovalRoutingRules) *types.ApprovalFlow {
	t.Helper()
	raw, err := jsonMarshalApprovalRules(rules)
	require.NoError(t, err)
	return &types.ApprovalFlow{Mode: mode, Rules: raw, Version: 3}
}

func jsonMarshalApprovalRules(rules types.ApprovalRoutingRules) (types.JSON, error) {
	raw, err := json.Marshal(rules)
	if err != nil {
		return nil, err
	}
	return types.JSON(raw), nil
}

// TestResolveApprovalChain covers §6.3/§6.4: the chain must be generated from
// content risk plus configuration, and configuration may only raise strength.
func TestResolveApprovalChain(t *testing.T) {
	cases := []struct {
		name         string
		flow         *types.ApprovalFlow
		contentRisk  types.ApprovalRiskLevel
		wantMode     types.ApprovalFlowMode
		wantQuorum   int
		wantRisk     types.ApprovalRiskLevel
		wantReason   string
		wantDistinct bool
	}{
		{
			name:        "no configuration falls back to single",
			flow:        nil,
			contentRisk: types.ApprovalRiskLow,
			wantMode:    types.ApprovalModeSingle,
			wantQuorum:  1,
			wantRisk:    types.ApprovalRiskLow,
		},
		{
			name:        "high risk can never be single-approved",
			flow:        &types.ApprovalFlow{Mode: string(types.ApprovalModeSingle)},
			contentRisk: types.ApprovalRiskHigh,
			wantMode:    types.ApprovalModeDual,
			wantQuorum:  2,
			wantRisk:    types.ApprovalRiskHigh,
			wantReason:  "high_risk_requires_dual",
		},
		{
			name:        "space default dual applies too",
			flow:        &types.ApprovalFlow{Mode: string(types.ApprovalModeDual)},
			contentRisk: types.ApprovalRiskLow,
			wantMode:    types.ApprovalModeDual,
			wantQuorum:  2,
			wantRisk:    types.ApprovalRiskLow,
			wantReason:  "space_default_mode",
		},
		{
			name: "min risk floor raises the computed level",
			flow: flowWithRules(t, string(types.ApprovalModeSingle), types.ApprovalRoutingRules{
				MinRiskLevel: string(types.ApprovalRiskMid),
			}),
			contentRisk: types.ApprovalRiskLow,
			wantMode:    types.ApprovalModeSingle,
			wantQuorum:  1,
			wantRisk:    types.ApprovalRiskMid,
			wantReason:  "min_risk_level",
		},
		{
			name: "force dual above mid",
			flow: flowWithRules(t, string(types.ApprovalModeSingle), types.ApprovalRoutingRules{
				ForceDualAbove: string(types.ApprovalRiskMid),
			}),
			contentRisk: types.ApprovalRiskMid,
			wantMode:    types.ApprovalModeDual,
			wantQuorum:  2,
			wantRisk:    types.ApprovalRiskMid,
			wantReason:  "force_dual_above",
		},
		{
			name: "force dual above high does not affect mid",
			flow: flowWithRules(t, string(types.ApprovalModeSingle), types.ApprovalRoutingRules{
				ForceDualAbove: string(types.ApprovalRiskHigh),
			}),
			contentRisk: types.ApprovalRiskMid,
			wantMode:    types.ApprovalModeSingle,
			wantQuorum:  1,
			wantRisk:    types.ApprovalRiskMid,
		},
		{
			name: "distinct reviewer roles propagate",
			flow: flowWithRules(t, string(types.ApprovalModeDual), types.ApprovalRoutingRules{
				RequireDistinctReviewerRoles: true,
			}),
			contentRisk:  types.ApprovalRiskLow,
			wantMode:     types.ApprovalModeDual,
			wantQuorum:   2,
			wantRisk:     types.ApprovalRiskLow,
			wantDistinct: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolution := ResolveApprovalChain(tc.flow, tc.contentRisk)
			assert.Equal(t, tc.wantMode, resolution.Mode)
			assert.Equal(t, tc.wantQuorum, resolution.Quorum)
			assert.Equal(t, tc.wantRisk, resolution.RiskLevel)
			assert.Equal(t, tc.wantDistinct, resolution.RequireDistinctReviewerRoles)
			if tc.wantReason != "" {
				assert.Contains(t, resolution.Reasons, tc.wantReason)
			}
		})
	}
}

// TestResolveApprovalChainNeverLowersRisk is the core anti-abuse property: no
// configuration value may weaken the chain below what the content requires.
func TestResolveApprovalChainNeverLowersRisk(t *testing.T) {
	// A flow that tries to say "everything here is low risk".
	flow := flowWithRules(t, string(types.ApprovalModeSingle), types.ApprovalRoutingRules{
		MinRiskLevel: string(types.ApprovalRiskLow),
	})
	resolution := ResolveApprovalChain(flow, types.ApprovalRiskHigh)
	assert.Equal(t, types.ApprovalRiskHigh, resolution.RiskLevel)
	assert.Equal(t, types.ApprovalModeDual, resolution.Mode)
	assert.Equal(t, 2, resolution.Quorum)
}

// TestValidateApprovalFlow rejects configurations that would silently break the
// gate rather than failing at submission time.
func TestValidateApprovalFlow(t *testing.T) {
	require.NoError(t, validateApprovalFlow(&types.ApprovalFlow{Mode: "single"}))
	require.NoError(t, validateApprovalFlow(&types.ApprovalFlow{Mode: "dual"}))
	require.Error(t, validateApprovalFlow(&types.ApprovalFlow{Mode: "triple"}))
	require.Error(t, validateApprovalFlow(&types.ApprovalFlow{SLAHours: -1}))
	require.Error(t, validateApprovalFlow(&types.ApprovalFlow{
		Rules: types.JSON(`{"min_risk_level":"critical"}`),
	}))
	require.Error(t, validateApprovalFlow(&types.ApprovalFlow{
		Rules: types.JSON(`{not json`),
	}))
}

// TestDefaultApprovalFlowIsSingle documents the fallback a space with no
// configuration gets: single approval, a 72h SLA, escalation to admin.
func TestDefaultApprovalFlowIsSingle(t *testing.T) {
	flow := defaultApprovalFlow(7, "kb-1")
	assert.Equal(t, string(types.ApprovalModeSingle), flow.Mode)
	assert.Equal(t, 0, flow.Version)
	assert.Equal(t, approvalFlowDefaultSLAHours, flow.SLAHours)
	assert.Equal(t, string(types.TenantRoleAdmin), flow.EscalateRole)
	assert.True(t, flow.Enabled)
}
