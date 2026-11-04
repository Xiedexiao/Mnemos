package types

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTenantExportWhitelistExcludesCredentials is the security core: an export
// must never carry access rights.
func TestTenantExportWhitelistExcludesCredentials(t *testing.T) {
	for _, table := range []string{
		"api_keys", "users", "auth_tokens", "tenant_invitations", "mcp_services",
	} {
		allowed, reason := TenantExportTableAllowed(table)
		assert.False(t, allowed, "%s must never be exported", table)
		assert.NotEmpty(t, reason, "a refusal states why")
	}
}

// TestExportableTablesIsSortedAndClean proves the effective list is stable and
// free of revoked entries.
func TestExportableTablesIsSortedAndClean(t *testing.T) {
	tables := ExportableTables()
	require.NotEmpty(t, tables)

	previous := ""
	for _, table := range tables {
		assert.Greater(t, table.Name, previous, "the list is sorted and unique")
		previous = table.Name
		_, revoked := revokedTenantExportTables[table.Name]
		assert.False(t, revoked, "%s must not survive in the effective list", table.Name)
	}
}

// TestTenantExportTableAllowedAcceptsWhitelisted covers a positive case.
func TestTenantExportTableAllowedAcceptsWhitelisted(t *testing.T) {
	allowed, reason := TenantExportTableAllowed("knowledges")
	assert.True(t, allowed)
	assert.Empty(t, reason)

	allowed, _ = TenantExportTableAllowed("definitely_not_a_table")
	assert.False(t, allowed, "unknown tables are not exportable by default")
}

// TestTenantExportClassificationCeilingUsesTheHighest is the rule that keeps a
// single secret row from hiding behind an average.
func TestTenantExportClassificationCeilingUsesTheHighest(t *testing.T) {
	assert.Equal(t, KnowledgeClassificationInternal,
		TenantExportClassificationCeiling([]string{"public", "internal", "public"}))
	assert.Equal(t, KnowledgeClassificationConfidential,
		TenantExportClassificationCeiling([]string{"public", "confidential", "internal"}))
	// Unlabelled rows are treated as internal, matching ParseKnowledgeClassification.
	assert.Equal(t, KnowledgeClassificationInternal,
		TenantExportClassificationCeiling([]string{"", "public"}))
	// No rows at all is public: there is nothing to protect.
	assert.Equal(t, KnowledgeClassificationPublic,
		TenantExportClassificationCeiling(nil))
}

// TestTenantExportNeedsApproval pins the threshold.
func TestTenantExportNeedsApproval(t *testing.T) {
	assert.False(t, TenantExportNeedsApproval(KnowledgeClassificationPublic))
	assert.False(t, TenantExportNeedsApproval(KnowledgeClassificationInternal))
	assert.True(t, TenantExportNeedsApproval(KnowledgeClassificationSecret))
	assert.True(t, TenantExportNeedsApproval(KnowledgeClassificationConfidential))
}

// TestValidateTenantExportManifestRejectsNewerFormats keeps a restore from
// half-reading a document it does not understand.
func TestValidateTenantExportManifestRejectsNewerFormats(t *testing.T) {
	good := NewTenantExportManifest(7, "u-1", "", time.Now())
	require.NoError(t, ValidateTenantExportManifest(good))

	newer := good
	newer.FormatVersion = TenantExportFormatVersion + 1
	err := ValidateTenantExportManifest(newer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "newer than this build supports")

	unversioned := good
	unversioned.FormatVersion = 0
	require.Error(t, ValidateTenantExportManifest(unversioned))

	noTenant := good
	noTenant.TenantID = 0
	require.Error(t, ValidateTenantExportManifest(noTenant))
}

// TestNewTenantExportManifestCarriesTheCaveat makes the artefact self-describing.
func TestNewTenantExportManifestCarriesTheCaveat(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	manifest := NewTenantExportManifest(7, "u-1", "req-1", now)

	assert.Equal(t, TenantExportFormatVersion, manifest.FormatVersion)
	assert.Equal(t, uint64(7), manifest.TenantID)
	assert.Equal(t, now, manifest.ExportedAt)
	assert.Equal(t, "req-1", manifest.ExportRequestID)
	assert.Contains(t, manifest.Note, "不是数据库备份",
		"the manifest itself says what this artefact is not")
	// Tables start empty: they are filled by the caller's preview pass, and a
	// manifest that claims completeness before anything was counted would be
	// a lie.
	assert.Empty(t, manifest.Tables)
}

// TestTenantExportWhitelistTablesHaveTenantFilters proves every whitelisted
// table either has a tenant column or is one of the tables the repository
// joins through a parent.
func TestTenantExportWhitelistTablesHaveTenantFilters(t *testing.T) {
	joined := map[string]bool{
		"knowledge_tag_relations": true,
		"change_items":            true,
	}
	for _, table := range ExportableTables() {
		if table.TenantColumn == "" {
			assert.True(t, joined[table.Name],
				"%s has no tenant column and no documented join, so exporting it would cross workspaces",
				table.Name)
			continue
		}
		assert.Equal(t, "tenant_id", table.TenantColumn, table.Name)
	}
}
