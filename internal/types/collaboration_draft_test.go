package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseInlineMentions covers the accepted inline syntax.
func TestParseInlineMentions(t *testing.T) {
	mentions := ParseInlineMentions("请 @[张三](user:u-1) 看一下，参考 @[E45 SOP](knowledge:k-1) 和 @[李四](u-2)")
	require.Len(t, mentions, 3)
	assert.Equal(t, Mention{Type: MentionTypeUser, ID: "u-1", Label: "张三"}, mentions[0])
	assert.Equal(t, Mention{Type: MentionTypeKnowledge, ID: "k-1", Label: "E45 SOP"}, mentions[1])
	// A bare id defaults to a user mention.
	assert.Equal(t, Mention{Type: MentionTypeUser, ID: "u-2", Label: "李四"}, mentions[2])
}

// TestParseInlineMentionsIgnoresPlainAt is the deliberate limitation: "@张三"
// without an id cannot be resolved reliably (duplicates, renames, other
// workspaces), so it stays plain text rather than being guessed.
func TestParseInlineMentionsIgnoresPlainAt(t *testing.T) {
	assert.Empty(t, ParseInlineMentions("请 @张三 看一下"))
	assert.Empty(t, ParseInlineMentions("邮箱 a@b.com"))
}

// TestParseInlineMentionsDedupes keeps one entry per (type, id).
func TestParseInlineMentionsDedupes(t *testing.T) {
	mentions := ParseInlineMentions("@[张三](u-1) 和 @[张三](user:u-1) 还有 @[专家](expert:u-1)")
	assert.Len(t, mentions, 2, "the same user twice is one mention; the expert form is distinct")
	assert.Equal(t, MentionTypeExpert, mentions[1].Type)
}

// TestNormalizeMentions covers validation, dedupe and the cap.
func TestNormalizeMentions(t *testing.T) {
	_, err := NormalizeMentions([]Mention{{Type: "ghost", ID: "x"}})
	require.Error(t, err)
	_, err = NormalizeMentions([]Mention{{Type: MentionTypeUser, ID: "  "}})
	require.Error(t, err)

	normalized, err := NormalizeMentions([]Mention{
		{Type: MentionTypeUser, ID: " u-1 ", Label: " 张三 "},
		{Type: "user", ID: "u-1"},
		{Type: "", ID: "u-2"},
	})
	require.NoError(t, err)
	require.Len(t, normalized, 2)
	assert.Equal(t, "u-1", normalized[0].ID)
	assert.Equal(t, "张三", normalized[0].Label)
	assert.Equal(t, MentionTypeUser, normalized[1].Type, "an omitted type means user")
}

// TestMergeMentionsCombinesBothSources covers handler+body merging.
func TestMergeMentionsCombinesBothSources(t *testing.T) {
	merged := MergeMentions(
		[]Mention{{Type: MentionTypeUser, ID: "u-1", Label: "张三"}},
		"另见 @[王五](user:u-2) 与 @[张三](user:u-1)",
	)
	require.Len(t, merged, 2)
	assert.Equal(t, "u-1", merged[0].ID)
	assert.Equal(t, "u-2", merged[1].ID)
}

// TestMentionLabelIsCapped keeps a pasted paragraph from becoming a label.
func TestMentionLabelIsCapped(t *testing.T) {
	long := make([]rune, 200)
	for i := range long {
		long[i] = 'a'
	}
	normalized, err := NormalizeMentions([]Mention{{Type: MentionTypeUser, ID: "u-1", Label: string(long)}})
	require.NoError(t, err)
	assert.LessOrEqual(t, len([]rune(normalized[0].Label)), 64)
}

// TestValidateDraftInput covers the draft shape rules.
func TestValidateDraftInput(t *testing.T) {
	require.NoError(t, ValidateDraftInput(DraftSaveInput{Kind: "knowledge", Body: "内容"}))
	require.Error(t, ValidateDraftInput(DraftSaveInput{Kind: "mystery"}))
	require.Error(t, ValidateDraftInput(DraftSaveInput{Kind: "knowledge", Body: string(make([]rune, DraftBodyMaxRunes+1))}))
}

// TestDraftMentionsRoundTrip covers the JSONB payload helpers.
func TestDraftMentionsRoundTrip(t *testing.T) {
	draft := &Draft{}
	require.NoError(t, draft.SetMentions([]Mention{{Type: MentionTypeKnowledge, ID: "k-1"}}))
	assert.Equal(t, []Mention{{Type: MentionTypeKnowledge, ID: "k-1"}}, draft.DraftMentions())

	draft.Mentions = JSON(`{not json`)
	assert.Nil(t, draft.DraftMentions(), "a corrupt payload yields no mentions instead of crashing")
}
