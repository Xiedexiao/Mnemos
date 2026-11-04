package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveMigrationFolderLongestPrefixWins is the mapping rule: a specific
// mapping must beat a general one.
func TestResolveMigrationFolderLongestPrefixWins(t *testing.T) {
	mappings := []FolderMapping{
		{From: "sop", To: "工艺/SOP"},
		{From: "sop/e45", To: "工艺/E45专用"},
	}
	assert.Equal(t, "工艺/E45专用", ResolveMigrationFolder("sop/e45/换型.md", mappings))
	// A file's own directory is what gets mapped, so the subdirectory is kept.
	assert.Equal(t, "工艺/SOP/通用", ResolveMigrationFolder("sop/通用/a.md", mappings))
	assert.Equal(t, "工艺/SOP", ResolveMigrationFolder("sop/a.md", mappings))
	// A path without an extension is treated as a directory: a manifest may
	// legitimately describe a folder ("sop/通用"), and assuming the last
	// segment is always a file name would silently drop it.
	assert.Equal(t, "工艺/SOP/通用", ResolveMigrationFolder("sop/通用", mappings))
}

// TestResolveMigrationFolderFallsBackToSourceDir keeps unmapped trees usable.
func TestResolveMigrationFolderFallsBackToSourceDir(t *testing.T) {
	assert.Equal(t, "manual/handbook", ResolveMigrationFolder("manual/handbook/entry.md", nil))
	assert.Equal(t, "", ResolveMigrationFolder("toplevel.md", nil))
	assert.Equal(t, "", ResolveMigrationFolder("", nil))
}

// TestResolveMigrationFolderEmptyTargetFlattens covers the "move to root" mapping.
func TestResolveMigrationFolderEmptyTargetFlattens(t *testing.T) {
	mappings := []FolderMapping{{From: "legacy", To: ""}}
	assert.Equal(t, "sub", ResolveMigrationFolder("legacy/sub/doc.md", mappings))
	assert.Equal(t, "", ResolveMigrationFolder("legacy/doc.md", mappings))
}

// TestMigrationItemTitleDerivation covers the fallback title chain.
func TestMigrationItemTitleDerivation(t *testing.T) {
	assert.Equal(t, "换型作业指导", MigrationItemTitle(MigrationItem{
		SourcePath: "sop/e45/换型作业指导.md",
	}))
	assert.Equal(t, "显式标题", MigrationItemTitle(MigrationItem{
		SourcePath: "sop/x.md", Title: "显式标题",
	}))
	assert.Equal(t, "idp-1", MigrationItemTitle(MigrationItem{ExternalID: "idp-1"}))
}

// TestNormalizeMigrationManifestCoversShapeRules covers the pre-flight checks.
func TestNormalizeMigrationManifestCoversShapeRules(t *testing.T) {
	base := MigrationManifest{
		KnowledgeBaseID: "kb-1",
		Items:           []MigrationItem{{SourcePath: "sop/a.md"}},
	}

	normalized, err := NormalizeMigrationManifest(base)
	require.NoError(t, err)
	assert.Equal(t, "sop/a.md", normalized.Items[0].SourcePath)

	// Knowledge base is required.
	_, err = NormalizeMigrationManifest(MigrationManifest{
		Items: []MigrationItem{{SourcePath: "a.md"}},
	})
	require.Error(t, err)

	// At least one item.
	_, err = NormalizeMigrationManifest(MigrationManifest{KnowledgeBaseID: "kb-1"})
	require.Error(t, err)

	// A typo'd classification must fail, not silently become internal.
	_, err = NormalizeMigrationManifest(MigrationManifest{
		KnowledgeBaseID: "kb-1",
		Items:           []MigrationItem{{SourcePath: "a.md", Classification: "secrett"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secrett")

	// Duplicate external ids mean two manifest rows fight over one target.
	_, err = NormalizeMigrationManifest(MigrationManifest{
		KnowledgeBaseID: "kb-1",
		Items: []MigrationItem{
			{ExternalID: "dup", SourcePath: "a.md"},
			{ExternalID: "dup", SourcePath: "b.md"},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")

	// A bad timestamp is rejected before any write.
	_, err = NormalizeMigrationManifest(MigrationManifest{
		KnowledgeBaseID: "kb-1",
		Items:           []MigrationItem{{SourcePath: "a.md", SourceUpdatedAt: "yesterday"}},
	})
	require.Error(t, err)
}

// TestNormalizeMigrationManifestSortsMappings keeps the resolution rule stable.
func TestNormalizeMigrationManifestSortsMappings(t *testing.T) {
	normalized, err := NormalizeMigrationManifest(MigrationManifest{
		KnowledgeBaseID: "kb-1",
		FolderMappings: []FolderMapping{
			{From: "sop", To: "A"},
			{From: "sop/e45/deep", To: "C"},
			{From: "sop/e45", To: "B"},
		},
		Items: []MigrationItem{{SourcePath: "x.md"}},
	})
	require.NoError(t, err)
	require.Len(t, normalized.FolderMappings, 3)
	// Longest prefix first, so the specific mapping is consulted first.
	assert.Equal(t, "sop/e45/deep", normalized.FolderMappings[0].From)
	assert.Equal(t, "sop/e45", normalized.FolderMappings[1].From)
	assert.Equal(t, "sop", normalized.FolderMappings[2].From)
}

// TestMigrationSourceUpdatedAtAcceptsCommonFormats covers export-tool timestamps.
func TestMigrationSourceUpdatedAtAcceptsCommonFormats(t *testing.T) {
	for _, raw := range []string{
		"2026-11-01T08:30:00Z", "2026-11-01 08:30:00", "2026-11-01",
	} {
		parsed, ok := MigrationSourceUpdatedAt(MigrationItem{SourceUpdatedAt: raw})
		assert.True(t, ok, raw)
		assert.Equal(t, 2026, parsed.Year(), raw)
	}
	_, ok := MigrationSourceUpdatedAt(MigrationItem{SourceUpdatedAt: "not a date"})
	assert.False(t, ok)
}
