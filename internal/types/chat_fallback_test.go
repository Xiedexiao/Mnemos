package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCanFallbackChatBeforeStream covers the safe window.
func TestCanFallbackChatBeforeStream(t *testing.T) {
	assert.True(t, CanFallbackChat(ChatFallbackBeforeStream, false).Allowed)
	assert.True(t, CanFallbackChat(ChatFallbackStreamOpen, false).Allowed,
		"opening the stream delivered nothing yet")
}

// TestCanFallbackChatRefusesMidStream is the rule that keeps an answer from
// becoming a splice of two models.
func TestCanFallbackChatRefusesMidStream(t *testing.T) {
	decision := CanFallbackChat(ChatFallbackMidStream, true)
	assert.False(t, decision.Allowed)
	assert.Contains(t, decision.Reason, "already reached the user")

	// Even if the phase says "before stream", delivered content wins: the
	// content, not the label, decides.
	assert.False(t, CanFallbackChat(ChatFallbackBeforeStream, true).Allowed,
		"delivered content blocks fallback regardless of the phase label")
}

// TestCanFallbackChatUnknownPhaseFailsClosed keeps an unrecognised phase from
// being treated as safe.
func TestCanFallbackChatUnknownPhaseFailsClosed(t *testing.T) {
	decision := CanFallbackChat(ChatFallbackPhase("mystery"), false)
	assert.False(t, decision.Allowed)
	assert.Contains(t, decision.Reason, "unknown failure phase")
}

// TestChatFallbackPhaseValid covers the enum guard.
func TestChatFallbackPhaseValid(t *testing.T) {
	for _, phase := range []ChatFallbackPhase{
		ChatFallbackBeforeStream, ChatFallbackStreamOpen, ChatFallbackMidStream,
	} {
		assert.True(t, phase.Valid())
	}
	assert.False(t, ChatFallbackPhase("done").Valid())
}

// TestNormalizeChatFallbackCandidatesDedupes keeps a repeated model from being
// tried twice with identical parameters.
func TestNormalizeChatFallbackCandidatesDedupes(t *testing.T) {
	got := NormalizeChatFallbackCandidates("m1", []string{"m1", " m2 ", "m1", "", "m3"})
	assert.Equal(t, []string{"m1", "m2", "m3"}, got)
}

// TestNormalizeChatFallbackCandidatesFallsBackToRequested keeps the
// unconfigured path identical to before the feature.
func TestNormalizeChatFallbackCandidatesFallsBackToRequested(t *testing.T) {
	assert.Equal(t, []string{"m1"}, NormalizeChatFallbackCandidates("m1", nil))
	assert.Equal(t, []string{"m1"}, NormalizeChatFallbackCandidates(" m1 ", []string{"  "}))
	assert.Empty(t, NormalizeChatFallbackCandidates("", nil),
		"no requested model and no chain means nothing to try")
}

// TestDescribeChatFallback covers the audit rendering.
func TestDescribeChatFallback(t *testing.T) {
	assert.Equal(t, "requested model served the answer",
		DescribeChatFallback(ChatFallbackTrace{RequestedModelID: "m1", ServedBy: "m1"}))

	degraded := DescribeChatFallback(ChatFallbackTrace{
		RequestedModelID: "m1", ServedBy: "m2", Degraded: true,
		Attempts: []ChatFallbackAttempt{
			{ModelID: "m1", Level: 0, Phase: ChatFallbackBeforeStream, Err: "creds expired"},
			{ModelID: "m2", Level: 1},
		},
	})
	assert.Contains(t, degraded, "degraded to m2")
	assert.Contains(t, degraded, "1 failed")

	exhausted := DescribeChatFallback(ChatFallbackTrace{
		RequestedModelID: "m1", Degraded: true,
		Attempts: []ChatFallbackAttempt{
			{ModelID: "m1", Level: 0, Err: "down"},
			{ModelID: "m2", Level: 1, Err: "down"},
		},
	})
	assert.Contains(t, exhausted, "no model could answer")
}

// TestChatFallbackSceneMatchesUsage pins the shared key so quotas and fallbacks
// join in reports.
func TestChatFallbackSceneMatchesUsage(t *testing.T) {
	assert.Equal(t, ModelUsageSceneChat, ChatFallbackScene)
	assert.Equal(t, "chat", ChatFallbackScene)
}
