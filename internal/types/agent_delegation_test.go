package types

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateAgentDelegationRequiresPurpose refuses a call with no stated
// intent: without it the audit trail degenerates into "someone used the tool".
func TestValidateAgentDelegationRequiresPurpose(t *testing.T) {
	require.Error(t, ValidateAgentDelegation(AgentDelegation{}))
	require.Error(t, ValidateAgentDelegation(AgentDelegation{Purpose: "  "}))
	require.Error(t, ValidateAgentDelegation(AgentDelegation{Purpose: "查"}),
		"a one-character purpose says nothing")

	require.NoError(t, ValidateAgentDelegation(AgentDelegation{
		Purpose: "汇总本周设备报警工单",
	}))
}

// TestValidateAgentDelegationRejectsPlaceholders is the "must be meaningful"
// rule: a constant "query" makes the column useless.
func TestValidateAgentDelegationRejectsPlaceholders(t *testing.T) {
	for _, placeholder := range []string{
		"query", "query ", "Search", "test", "n/a", "NONE", "查询", "测试", "无",
	} {
		assert.Error(t, ValidateAgentDelegation(AgentDelegation{Purpose: placeholder}),
			placeholder)
	}
}

// TestValidateAgentDelegationBoundsFields covers the length limits.
func TestValidateAgentDelegationBoundsFields(t *testing.T) {
	longPurpose := ""
	for i := 0; i < DelegationMaxPurposeRunes+5; i++ {
		longPurpose += "目"
	}
	require.Error(t, ValidateAgentDelegation(AgentDelegation{Purpose: longPurpose}))

	longTrace := ""
	for i := 0; i < DelegationMaxTraceRunes+5; i++ {
		longTrace += "t"
	}
	require.Error(t, ValidateAgentDelegation(AgentDelegation{
		Purpose: "合理的用途说明", TraceID: longTrace,
	}))
}

// TestAgentDelegationRoundTripsThroughContext covers the plumbing.
func TestAgentDelegationRoundTripsThroughContext(t *testing.T) {
	delegation := AgentDelegation{
		DelegatedUserID: "u-1", Purpose: "生成月度设备健康报告",
		TraceID: "trace-abc", Client: "bi-portal",
	}
	ctx := WithAgentDelegation(context.Background(), delegation)

	got, ok := AgentDelegationFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, delegation, got)

	// A bare context has no delegation.
	_, ok = AgentDelegationFromContext(context.Background())
	assert.False(t, ok)

	// An empty purpose counts as absent, so a zero-value struct cannot leak in.
	_, ok = AgentDelegationFromContext(
		WithAgentDelegation(context.Background(), AgentDelegation{DelegatedUserID: "u-1"}))
	assert.False(t, ok)
}

// TestDelegationAuditDetailsRendersKeys pins the audit key names: they are the
// query surface for "show me every call in this trace".
func TestDelegationAuditDetailsRendersKeys(t *testing.T) {
	details := DelegationAuditDetails(AgentDelegation{
		DelegatedUserID: "u-1", Purpose: "生成月度设备健康报告",
		TraceID: "trace-abc", Client: "bi-portal",
	})
	assert.Equal(t, "生成月度设备健康报告", details["purpose"])
	assert.Equal(t, "u-1", details["delegated_user_id"])
	assert.Equal(t, "trace-abc", details["trace_id"])
	assert.Equal(t, "bi-portal", details["client"])

	// Optional fields are omitted rather than written as empty strings: an
	// empty value in the ledger reads like "the caller declared nothing".
	sparse := DelegationAuditDetails(AgentDelegation{Purpose: "巡检知识缺口"})
	assert.Equal(t, "巡检知识缺口", sparse["purpose"])
	assert.NotContains(t, sparse, "trace_id")
	assert.NotContains(t, sparse, "delegated_user_id")

	// No purpose means no delegation block at all.
	assert.Nil(t, DelegationAuditDetails(AgentDelegation{}))
}

// TestEffectiveSubjectUserNarrowsNeverWidens documents the permission rule.
func TestEffectiveSubjectUserNarrowsNeverWidens(t *testing.T) {
	assert.Equal(t, "caller", EffectiveSubjectUser("caller", AgentDelegation{}))
	assert.Equal(t, "end-user",
		EffectiveSubjectUser("caller", AgentDelegation{DelegatedUserID: "end-user"}))
}

// TestNormalizeTrimsDelegationFields keeps stray whitespace out of the ledger.
func TestNormalizeTrimsDelegationFields(t *testing.T) {
	delegation := AgentDelegation{
		DelegatedUserID: "  u-1  ", Purpose: "  生成报告  ",
		TraceID: " trace ", Client: " client ",
	}
	delegation.Normalize()
	assert.Equal(t, "u-1", delegation.DelegatedUserID)
	assert.Equal(t, "生成报告", delegation.Purpose)
	assert.Equal(t, "trace", delegation.TraceID)
	assert.Equal(t, "client", delegation.Client)
}
