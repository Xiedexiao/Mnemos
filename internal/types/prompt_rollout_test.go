package types

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidatePromptVersionPointerRequiresStable keeps a baseline mandatory.
func TestValidatePromptVersionPointerRequiresStable(t *testing.T) {
	require.Error(t, ValidatePromptVersionPointer(PromptVersionPointer{TemplateKey: "chat"}))
	require.Error(t, ValidatePromptVersionPointer(PromptVersionPointer{
		TemplateKey: "chat", StableVersion: 0, CanaryVersion: 3, CanaryPercent: 20,
	}))
}

// TestValidatePromptVersionPointerRejectsPercentWithoutCanary is the "looks
// like a canary but is not" case.
func TestValidatePromptVersionPointerRejectsPercentWithoutCanary(t *testing.T) {
	err := ValidatePromptVersionPointer(PromptVersionPointer{
		TemplateKey: "chat", StableVersion: 2, CanaryPercent: 30,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a canary_version")
}

// TestValidatePromptVersionPointerRejectsSameVersion keeps a no-op split out.
func TestValidatePromptVersionPointerRejectsSameVersion(t *testing.T) {
	err := ValidatePromptVersionPointer(PromptVersionPointer{
		TemplateKey: "chat", StableVersion: 2, CanaryVersion: 2, CanaryPercent: 10,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must differ")
}

// TestValidatePromptVersionPointerAcceptsARealisticSplit is the happy path.
func TestValidatePromptVersionPointerAcceptsARealisticSplit(t *testing.T) {
	require.NoError(t, ValidatePromptVersionPointer(PromptVersionPointer{
		TemplateKey: "chat", StableVersion: 2, CanaryVersion: 3, CanaryPercent: 10,
	}))
	// No canary at all is also valid.
	require.NoError(t, ValidatePromptVersionPointer(PromptVersionPointer{
		TemplateKey: "chat", StableVersion: 2,
	}))
}

// TestValidatePromptVersionPointerBoundsPercent covers the range.
func TestValidatePromptVersionPointerBoundsPercent(t *testing.T) {
	require.Error(t, ValidatePromptVersionPointer(PromptVersionPointer{
		TemplateKey: "chat", StableVersion: 1, CanaryVersion: 2, CanaryPercent: -1,
	}))
	require.Error(t, ValidatePromptVersionPointer(PromptVersionPointer{
		TemplateKey: "chat", StableVersion: 1, CanaryVersion: 2, CanaryPercent: 101,
	}))
}

// TestSelectPromptVersionWithoutPointerFallsBackToLatest keeps the previous
// behaviour for deployments that never configure one.
func TestSelectPromptVersionWithoutPointerFallsBackToLatest(t *testing.T) {
	selection := SelectPromptVersion(PromptVersionPointer{}, "sess-1")
	assert.Zero(t, selection.Version)
	assert.False(t, selection.Canary)
	assert.Contains(t, selection.Reason, "latest")
}

// TestSelectPromptVersionIsStablePerSubject is the reproducibility rule: the
// same subject must always land on the same side of the split.
func TestSelectPromptVersionIsStablePerSubject(t *testing.T) {
	pointer := PromptVersionPointer{
		TemplateKey: "chat", StableVersion: 1, CanaryVersion: 2, CanaryPercent: 50,
	}
	first := SelectPromptVersion(pointer, "session-abc")
	for i := 0; i < 20; i++ {
		assert.Equal(t, first, SelectPromptVersion(pointer, "session-abc"))
	}
}

// TestSelectPromptVersionSplitsRoughlyInHalf is a distribution sanity check.
//
// 不是精确断言（哈希不保证均匀到小数点），只验证"两边都有人"——一个永远
// 落到同一侧的实现会让灰度完全失效。
func TestSelectPromptVersionSplitsRoughlyInHalf(t *testing.T) {
	pointer := PromptVersionPointer{
		TemplateKey: "chat", StableVersion: 1, CanaryVersion: 2, CanaryPercent: 50,
	}
	canary := 0
	const subjects = 400
	for i := 0; i < subjects; i++ {
		if SelectPromptVersion(pointer, fmt.Sprintf("session-%d", i)).Canary {
			canary++
		}
	}
	assert.Greater(t, canary, subjects/5, "the canary group must not be empty")
	assert.Less(t, canary, subjects*4/5, "the stable group must not be empty")
}

// TestSelectPromptVersionAtHundredPercent serves everyone the canary.
func TestSelectPromptVersionAtHundredPercent(t *testing.T) {
	pointer := PromptVersionPointer{
		TemplateKey: "chat", StableVersion: 1, CanaryVersion: 2, CanaryPercent: 100,
	}
	for i := 0; i < 20; i++ {
		selection := SelectPromptVersion(pointer, fmt.Sprintf("s-%d", i))
		assert.True(t, selection.Canary)
		assert.Equal(t, 2, selection.Version)
	}
}

// TestSelectPromptVersionWithoutCanaryIsStable covers the plain case.
func TestSelectPromptVersionWithoutCanaryIsStable(t *testing.T) {
	selection := SelectPromptVersion(PromptVersionPointer{
		TemplateKey: "chat", StableVersion: 4,
	}, "anyone")
	assert.Equal(t, 4, selection.Version)
	assert.False(t, selection.Canary)
}

// TestValidatePromptRollbackRequiresANote is the accountability rule.
func TestValidatePromptRollbackRequiresANote(t *testing.T) {
	require.Error(t, ValidatePromptRollback(PromptRollbackRequest{
		TemplateKey: "chat", ToVersion: 3,
	}), "a rollback without a reason cannot be reviewed")

	require.NoError(t, ValidatePromptRollback(PromptRollbackRequest{
		TemplateKey: "chat", ToVersion: 3, Note: "新版本答复明显变短",
	}))

	require.Error(t, ValidatePromptRollback(PromptRollbackRequest{
		TemplateKey: "chat", ToVersion: 0, Note: "x",
	}))
}

// TestDescribePromptRollback covers the audit line.
func TestDescribePromptRollback(t *testing.T) {
	line := DescribePromptRollback("chat", 4, 3, " 答复变短 ")
	assert.Contains(t, line, "chat")
	assert.Contains(t, line, "v4")
	assert.Contains(t, line, "v3")
	assert.Contains(t, line, "答复变短")
}
