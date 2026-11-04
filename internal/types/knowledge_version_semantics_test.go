package types

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseSemanticVersion covers the accepted forms.
func TestParseSemanticVersion(t *testing.T) {
	cases := map[string]SemanticVersion{
		"1.2.3":  {1, 2, 3},
		"v1.2.3": {1, 2, 3},
		"2":      {2, 0, 0},
		"1.2":    {1, 2, 0},
	}
	for raw, want := range cases {
		got, err := ParseSemanticVersion(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, want, got, raw)
	}

	for _, bad := range []string{"", "abc", "1.2.3.4", "-1.0.0", "1.x.0"} {
		_, err := ParseSemanticVersion(bad)
		require.Error(t, err, bad)
	}
}

// TestSemanticVersionBump covers the §7.4 ladder.
func TestSemanticVersionBump(t *testing.T) {
	base := SemanticVersion{1, 2, 3}
	assert.Equal(t, SemanticVersion{1, 2, 4}, base.Bump(VersionBumpPatch))
	assert.Equal(t, SemanticVersion{1, 3, 0}, base.Bump(VersionBumpMinor))
	assert.Equal(t, SemanticVersion{2, 0, 0}, base.Bump(VersionBumpMajor))
	// An unknown bump defaults to the least destructive step.
	assert.Equal(t, SemanticVersion{1, 2, 4}, base.Bump(VersionBump("bogus")))
}

// TestSemanticVersionCompare covers ordering across components.
func TestSemanticVersionCompare(t *testing.T) {
	assert.Equal(t, -1, SemanticVersion{1, 2, 3}.Compare(SemanticVersion{1, 2, 4}))
	assert.Equal(t, 1, SemanticVersion{1, 3, 0}.Compare(SemanticVersion{1, 2, 9}))
	assert.Equal(t, 1, SemanticVersion{2, 0, 0}.Compare(SemanticVersion{1, 99, 99}))
	assert.Equal(t, 0, SemanticVersion{1, 2, 3}.Compare(SemanticVersion{1, 2, 3}))
}

// TestClassifyVersionBump maps change types onto bump sizes.
func TestClassifyVersionBump(t *testing.T) {
	assert.Equal(t, VersionBumpMajor, ClassifyVersionBump(ChangeTypeParamChange))
	assert.Equal(t, VersionBumpMajor, ClassifyVersionBump(ChangeTypeReplacement))
	assert.Equal(t, VersionBumpMinor, ClassifyVersionBump(ChangeTypeContentRevision))
	assert.Equal(t, VersionBumpPatch, ClassifyVersionBump(ChangeTypeTextFix))
}

// TestDiffParagraphsDetectsInsertion is why LCS is used: a single inserted
// paragraph must not mark everything after it as changed.
func TestDiffParagraphsDetectsInsertion(t *testing.T) {
	left := "第一段\n第二段\n第三段"
	right := "第一段\n插入的新段\n第二段\n第三段"
	lines, added, removed, unchanged, truncated := DiffParagraphs(left, right)
	assert.False(t, truncated)
	assert.Equal(t, 1, added)
	assert.Zero(t, removed)
	assert.Equal(t, 3, unchanged)

	var addedText string
	for _, line := range lines {
		if line.Kind == DiffLineAdded {
			addedText = line.Text
		}
	}
	assert.Equal(t, "插入的新段", addedText)
}

// TestDiffParagraphsDetectsRemovalAndEdit covers the other directions.
func TestDiffParagraphsDetectsRemovalAndEdit(t *testing.T) {
	lines, added, removed, unchanged, _ := DiffParagraphs("a\nb\nc", "a\nc")
	assert.Equal(t, 1, removed)
	assert.Zero(t, added)
	assert.Equal(t, 2, unchanged)
	assert.Len(t, lines, 3)

	lines, added, removed, _, _ = DiffParagraphs("旧结论", "新结论")
	assert.Equal(t, 1, added)
	assert.Equal(t, 1, removed)
	assert.False(t, func() bool {
		for _, line := range lines {
			if line.Kind == DiffLineUnchanged {
				return true
			}
		}
		return false
	}(), "a full rewrite has nothing unchanged")
}

// TestDiffParagraphsIgnoresFormatting keeps blank lines and trailing spaces
// from becoming changes.
func TestDiffParagraphsIgnoresFormatting(t *testing.T) {
	lines, added, removed, unchanged, _ := DiffParagraphs("a\n\nb   ", "a\nb\r\n")
	assert.Zero(t, added)
	assert.Zero(t, removed)
	assert.Equal(t, 2, unchanged)
	assert.Len(t, lines, 2)
}

// TestDiffParagraphsTruncatesHugeDocuments keeps a giant document from
// spending minutes in the comparison.
func TestDiffParagraphsTruncatesHugeDocuments(t *testing.T) {
	builder := strings.Builder{}
	for i := 0; i < diffMaxParagraphs+1; i++ {
		builder.WriteString("段落\n")
	}
	big := builder.String()
	_, added, removed, unchanged, truncated := DiffParagraphs(big, "短")
	assert.True(t, truncated)
	assert.Zero(t, unchanged)
	assert.Equal(t, 1, added)
	assert.Equal(t, diffMaxParagraphs+1, removed)
}

// TestValidateVersionRollbackInput requires a reason.
func TestValidateVersionRollbackInput(t *testing.T) {
	require.NoError(t, ValidateVersionRollbackInput(VersionRollbackInput{
		KnowledgeID: "k-1", TargetVersion: "1.0.0", ChangeNote: "回退到旧参数",
	}))
	require.Error(t, ValidateVersionRollbackInput(VersionRollbackInput{
		KnowledgeID: "k-1", TargetVersion: "1.0.0",
	}), "a rollback without a reason cannot be audited")
	require.Error(t, ValidateVersionRollbackInput(VersionRollbackInput{
		TargetVersion: "1.0.0", ChangeNote: "x",
	}))
	require.Error(t, ValidateVersionRollbackInput(VersionRollbackInput{
		KnowledgeID: "k-1", ChangeNote: "x",
	}))
}
