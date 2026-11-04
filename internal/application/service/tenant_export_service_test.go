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

// stubTenantExportRepo serves export rows.
type stubTenantExportRepo struct {
	interfaces.TenantExportRepository

	// existing is the set of tables this deployment has.
	existing map[string]bool
	// rows is table → the tenant's rows.
	rows map[string][]map[string]any
	// rowErr forces one table's read to fail.
	rowErr map[string]error
	// classifications is what the tenant's documents contain.
	classifications []string
	// requestedTenants records the tenant each call asked for.
	requestedTenants []uint64
}

func newStubTenantExportRepo() *stubTenantExportRepo {
	repo := &stubTenantExportRepo{
		existing: map[string]bool{}, rows: map[string][]map[string]any{},
		rowErr: map[string]error{},
	}
	// Every whitelisted table exists unless a test removes it.
	for _, table := range types.ExportableTables() {
		repo.existing[table.Name] = true
	}
	return repo
}

func (s *stubTenantExportRepo) TableExists(
	_ context.Context, table string,
) (bool, error) {
	return s.existing[table], nil
}

func (s *stubTenantExportRepo) ExportTable(
	_ context.Context, tenantID uint64, table types.TenantExportTable, _ int,
) ([]map[string]any, error) {
	s.requestedTenants = append(s.requestedTenants, tenantID)
	if err := s.rowErr[table.Name]; err != nil {
		return nil, err
	}
	return s.rows[table.Name], nil
}

func (s *stubTenantExportRepo) CountTable(
	_ context.Context, tenantID uint64, table types.TenantExportTable,
) (int64, error) {
	s.requestedTenants = append(s.requestedTenants, tenantID)
	if err := s.rowErr[table.Name]; err != nil {
		return 0, err
	}
	return int64(len(s.rows[table.Name])), nil
}

func (s *stubTenantExportRepo) DistinctClassifications(
	_ context.Context, tenantID uint64,
) ([]string, error) {
	s.requestedTenants = append(s.requestedTenants, tenantID)
	return s.classifications, nil
}

// stubExportControl serves approved export requests.
type stubExportControl struct {
	interfaces.ExportControlService

	approved map[string]bool
	err      error
}

func (s *stubExportControl) List(
	_ context.Context, _ uint64, status string,
) ([]*types.ExportRequest, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([]*types.ExportRequest, 0, len(s.approved))
	for id := range s.approved {
		out = append(out, &types.ExportRequest{ID: id, Status: status})
	}
	return out, nil
}

func newTenantExportServiceForTest(
	repo interfaces.TenantExportRepository, exports interfaces.ExportControlService,
) *tenantExportService {
	svc := NewTenantExportService(repo, exports).(*tenantExportService)
	svc.now = func() time.Time {
		return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	}
	return svc
}

// TestExportRefusesHighClassificationWithoutApproval is the headline rule:
// data that has left cannot be recalled, so there is no "export first, approve
// later".
func TestExportRefusesHighClassificationWithoutApproval(t *testing.T) {
	repo := newStubTenantExportRepo()
	repo.classifications = []string{"public", "secret"}
	svc := newTenantExportServiceForTest(repo, nil)

	_, err := svc.Export(context.Background(), 7, "u-1", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret")
}

// TestExportAcceptsAnApprovedRequest covers the approved path.
func TestExportAcceptsAnApprovedRequest(t *testing.T) {
	repo := newStubTenantExportRepo()
	repo.classifications = []string{"secret"}
	control := &stubExportControl{approved: map[string]bool{"req-1": true}}
	svc := newTenantExportServiceForTest(repo, control)

	document, err := svc.Export(context.Background(), 7, "u-1", "req-1")
	require.NoError(t, err)
	require.NotNil(t, document)
	assert.Equal(t, "req-1", document.Manifest.ExportRequestID)
	assert.Equal(t, "u-1", document.Manifest.ExportedBy)
}

// TestExportRefusesAnUnknownRequest keeps a made-up id from working.
func TestExportRefusesAnUnknownRequest(t *testing.T) {
	repo := newStubTenantExportRepo()
	repo.classifications = []string{"confidential"}
	control := &stubExportControl{approved: map[string]bool{"req-1": true}}
	svc := newTenantExportServiceForTest(repo, control)

	_, err := svc.Export(context.Background(), 7, "u-1", "req-999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no approved export request")
}

// TestExportLowClassificationNeedsNoRequest keeps ordinary exports usable.
func TestExportLowClassificationNeedsNoRequest(t *testing.T) {
	repo := newStubTenantExportRepo()
	repo.classifications = []string{"public", "internal"}
	svc := newTenantExportServiceForTest(repo, nil)

	document, err := svc.Export(context.Background(), 7, "u-1", "")
	require.NoError(t, err)
	assert.Empty(t, document.Manifest.ExportRequestID)
}

// TestExportScopesEveryReadToTheTenant is the isolation proof: every repository
// call must carry the caller's tenant id.
func TestExportScopesEveryReadToTheTenant(t *testing.T) {
	repo := newStubTenantExportRepo()
	svc := newTenantExportServiceForTest(repo, nil)

	_, err := svc.Export(context.Background(), 7, "u-1", "")
	require.NoError(t, err)

	require.NotEmpty(t, repo.requestedTenants)
	for _, tenantID := range repo.requestedTenants {
		assert.Equal(t, uint64(7), tenantID,
			"every read must be scoped to the exporting workspace")
	}
}

// TestExportSkipsMissingTablesButSaysSo keeps a version gap visible.
func TestExportSkipsMissingTablesButSaysSo(t *testing.T) {
	repo := newStubTenantExportRepo()
	delete(repo.existing, "sampling_inspections")
	svc := newTenantExportServiceForTest(repo, nil)

	document, err := svc.Export(context.Background(), 7, "u-1", "")
	require.NoError(t, err)

	var skipped *types.TenantExportTableSummary
	for index, summary := range document.Manifest.Tables {
		if summary.Name == "sampling_inspections" {
			skipped = &document.Manifest.Tables[index]
		}
	}
	require.NotNil(t, skipped)
	assert.True(t, skipped.Skipped)
	assert.NotEmpty(t, skipped.Reason, "a skip is explained, not silent")
	assert.NotContains(t, document.Data, "sampling_inspections")
}

// TestExportSurvivesOneTableFailing keeps the rest of the export usable.
func TestExportSurvivesOneTableFailing(t *testing.T) {
	repo := newStubTenantExportRepo()
	repo.rows["knowledges"] = []map[string]any{{"id": "doc-1", "title": "报警处理"}}
	repo.rowErr["glossary_terms"] = errors.New("table locked")
	svc := newTenantExportServiceForTest(repo, nil)

	document, err := svc.Export(context.Background(), 7, "u-1", "")
	require.NoError(t, err)

	assert.Contains(t, document.Data, "knowledges", "the healthy tables still export")
	for _, summary := range document.Manifest.Tables {
		if summary.Name == "glossary_terms" {
			assert.True(t, summary.Skipped)
			assert.Contains(t, summary.Reason, "table locked")
		}
	}
}

// TestExportRequiresIdentity refuses an unattributed export.
func TestExportRequiresIdentity(t *testing.T) {
	svc := newTenantExportServiceForTest(newStubTenantExportRepo(), nil)
	_, err := svc.Export(context.Background(), 0, "u-1", "")
	require.Error(t, err)

	_, err = svc.Export(context.Background(), 7, "  ", "")
	require.Error(t, err)
}

// TestPlanReportsTheRequiredCeiling tells the caller what approval to obtain.
func TestPlanReportsTheRequiredCeiling(t *testing.T) {
	repo := newStubTenantExportRepo()
	repo.rows["knowledges"] = []map[string]any{{"id": "d1"}, {"id": "d2"}}
	repo.classifications = []string{"public", "confidential"}
	svc := newTenantExportServiceForTest(repo, nil)

	manifest, err := svc.Plan(context.Background(), 7)
	require.NoError(t, err)
	assert.Contains(t, manifest.Note, "confidential")
	assert.Contains(t, manifest.Note, "需要审批")

	// The preview counts rows per table.
	found := false
	for _, summary := range manifest.Tables {
		if summary.Name == "knowledges" {
			found = true
			assert.Equal(t, 2, summary.Rows)
		}
	}
	assert.True(t, found)
}

// TestPlanForLowClassificationNeedsNoApproval keeps the note quiet.
func TestPlanForLowClassificationNeedsNoApproval(t *testing.T) {
	repo := newStubTenantExportRepo()
	repo.classifications = []string{"internal"}
	svc := newTenantExportServiceForTest(repo, nil)

	manifest, err := svc.Plan(context.Background(), 7)
	require.NoError(t, err)
	assert.NotContains(t, manifest.Note, "需要审批")
}

// TestPlanRequiresTenant refuses an unscoped plan.
func TestPlanRequiresTenant(t *testing.T) {
	svc := newTenantExportServiceForTest(newStubTenantExportRepo(), nil)
	_, err := svc.Plan(context.Background(), 0)
	require.Error(t, err)
}

// TestExportNeverIncludesRevokedTables is the last line of defence: even if a
// table slipped into the whitelist, a revoked one must not appear.
func TestExportNeverIncludesRevokedTables(t *testing.T) {
	repo := newStubTenantExportRepo()
	svc := newTenantExportServiceForTest(repo, nil)

	document, err := svc.Export(context.Background(), 7, "u-1", "")
	require.NoError(t, err)
	for _, forbidden := range []string{
		"api_keys", "users", "auth_tokens", "tenant_invitations", "mcp_services",
	} {
		assert.NotContains(t, document.Data, forbidden,
			"%s must never reach an export", forbidden)
	}
}
