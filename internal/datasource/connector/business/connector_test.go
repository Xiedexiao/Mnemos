package business

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// TestAllSevenBusinessSystemsAreDeclared pins §3.3's source list.
func TestAllSevenBusinessSystemsAreDeclared(t *testing.T) {
	profiles := Profiles()
	require.Len(t, profiles, 7)

	declared := make(map[string]bool, len(profiles))
	for _, profile := range profiles {
		declared[profile.Type] = true
		assert.NotEmpty(t, profile.Name, "%s needs a display name", profile.Type)
		assert.NotEmpty(t, profile.Needs, "%s must say what contract it needs", profile.Type)
	}
	for _, expected := range []string{
		types.ConnectorTypeERP, types.ConnectorTypeMES, types.ConnectorTypePLM,
		types.ConnectorTypeQMS, types.ConnectorTypeOA, types.ConnectorTypeHR,
		types.ConnectorTypeCRM,
	} {
		assert.True(t, declared[expected], "%s must have a placeholder", expected)
	}
	assert.Nil(t, NewConnector(types.ConnectorTypeBusinessAPI),
		"business_api is implemented by GenericConnector, not a placeholder")
}

// TestPlaceholderNeverReturnsEmptyResults is the central property. A "success,
// 0 items" outcome would read as "the source holds no data", which is a
// different — and false — statement than "the integration is not built".
func TestPlaceholderNeverReturnsEmptyResults(t *testing.T) {
	ctx := context.Background()
	config := &types.DataSourceConfig{}

	for _, profile := range Profiles() {
		connector := NewConnector(profile.Type)
		require.NotNil(t, connector, "%s must construct", profile.Type)

		items, err := connector.FetchAll(ctx, config, nil)
		require.Error(t, err, "%s FetchAll must fail", profile.Type)
		assert.Nil(t, items)

		items, cursor, err := connector.FetchIncremental(ctx, config, nil)
		require.Error(t, err, "%s FetchIncremental must fail", profile.Type)
		assert.Nil(t, items)
		assert.Nil(t, cursor)

		resources, err := connector.ListResources(ctx, config, "")
		require.Error(t, err, "%s ListResources must fail", profile.Type)
		assert.Nil(t, resources)

		ancestors, err := connector.ResolveResourceAncestors(ctx, config, []string{"x"})
		require.Error(t, err)
		assert.Nil(t, ancestors)

		require.Error(t, connector.Validate(ctx, config))
	}
}

// TestErrorIsActionable keeps the failure diagnosable: it must name the system,
// what interface contract is missing, and how to proceed meanwhile.
func TestErrorIsActionable(t *testing.T) {
	connector := NewConnector(types.ConnectorTypeQMS)
	require.NotNil(t, connector)

	_, err := connector.FetchAll(context.Background(), &types.DataSourceConfig{}, nil)
	require.Error(t, err)

	var notImplemented *NotImplementedError
	require.True(t, errors.As(err, &notImplemented),
		"callers must be able to detect the permanent condition, not retry it as transient")
	assert.Equal(t, types.ConnectorTypeQMS, notImplemented.ConnectorType)
	assert.Contains(t, notImplemented.Error(), "QMS")
	assert.Contains(t, notImplemented.Error(), types.ConnectorTypeBusinessAPI,
		"the message must point at the working alternative")
}

// TestUnknownTypeConstructsNil keeps a bad registration from producing a
// connector that silently accepts anything.
func TestUnknownTypeConstructsNil(t *testing.T) {
	assert.Nil(t, NewConnector("sap_r3"))
	assert.Nil(t, NewConnector(""))
}

// TestMetadataMarksPlaceholdersPlanned is what keeps the UI from offering a
// connector that cannot sync.
func TestMetadataMarksPlaceholdersPlanned(t *testing.T) {
	metadata := MetadataFor()
	require.Len(t, metadata, 7)
	for _, entry := range metadata {
		assert.True(t, entry.Planned, "%s must be flagged planned", entry.Type)
		assert.Empty(t, entry.Capabilities,
			"%s must not advertise capabilities it does not have", entry.Type)
	}
}
