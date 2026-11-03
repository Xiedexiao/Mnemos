package business

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// newTestConnector wires the reader to a test server instead of the SSRF-safe
// client, which would refuse 127.0.0.1 by design.
func newTestConnector(t *testing.T, handler http.Handler) (*GenericConnector, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return newGenericConnectorWithHTTPClient(server.Client()), server
}

// apiConfig builds the typed settings the connector reads.
func apiConfig(server *httptest.Server, settings map[string]interface{}) *types.DataSourceConfig {
	merged := map[string]interface{}{
		"base_url":   server.URL,
		"endpoint":   "/api/v1/work-orders",
		"id_path":    "id",
		"title_path": "title",
	}
	for key, value := range settings {
		merged[key] = value
	}
	return &types.DataSourceConfig{Settings: merged}
}

// TestFieldMappingProducesReadableKnowledge covers §3.3's requirement that a
// synced record carries its business keys rather than a bare id.
func TestFieldMappingProducesReadableKnowledge(t *testing.T) {
	connector, server := newTestConnector(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"id":         "WO-1001",
				"title":      "E45 停线工单",
				"updated_at": "2026-11-01T08:30:00Z",
				"equipment":  "E45",
				"status":     "closed",
				"qty":        float64(12),
			},
		})
	}))

	items, err := connector.FetchAll(context.Background(), apiConfig(server, map[string]interface{}{
		"body_fields":     []string{"equipment", "status", "qty"},
		"updated_at_path": "updated_at",
	}), nil)
	require.NoError(t, err)
	require.Len(t, items, 1)

	item := items[0]
	assert.Equal(t, "WO-1001", item.Metadata["business_id"])
	assert.Equal(t, "business_api:WO-1001", item.ExternalID)
	assert.Equal(t, "E45 停线工单", item.Title)
	assert.Contains(t, string(item.Content), "**equipment**: E45")
	assert.Contains(t, string(item.Content), "**qty**: 12",
		"JSON numbers must not render as 12.000000")
	assert.Equal(t, time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC), item.UpdatedAt.UTC())
}

// TestRecordsPathUnwrapsEnvelope covers the {data:{items:[...]}} shape most
// business APIs use.
func TestRecordsPathUnwrapsEnvelope(t *testing.T) {
	connector, server := newTestConnector(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": map[string]interface{}{
				"items": []map[string]interface{}{{"id": "1", "title": "首件检验记录"}},
			},
		})
	}))

	items, err := connector.FetchAll(context.Background(), apiConfig(server, map[string]interface{}{
		"records_path": "data.items",
	}), nil)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "首件检验记录", items[0].Title)
}

// TestWalkStopsAtShortPage pins the end-of-pagination rule: a page smaller than
// page_size is the last page, without relying on total/has_more fields that
// differ per system.
func TestWalkStopsAtShortPage(t *testing.T) {
	var mu sync.Mutex
	var pages []string
	connector, server := newTestConnector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		mu.Lock()
		pages = append(pages, r.URL.Query().Get("page"))
		mu.Unlock()

		records := make([]map[string]interface{}, 0, 2)
		if page == 1 {
			records = append(records,
				map[string]interface{}{"id": "1", "title": "a"},
				map[string]interface{}{"id": "2", "title": "b"})
		} else {
			records = append(records, map[string]interface{}{"id": "3", "title": "c"})
		}
		_ = json.NewEncoder(w).Encode(records)
	}))

	items, err := connector.FetchAll(context.Background(), apiConfig(server, map[string]interface{}{
		"page_param":      "page",
		"page_size_param": "page_size",
		"page_size":       2,
	}), nil)
	require.NoError(t, err)
	require.Len(t, items, 3)
	assert.Equal(t, []string{"1", "2"}, pages, "a short page must end the walk")
}

// TestIncrementalSendsConfiguredSinceParam covers the cursor contract: the
// parameter name comes from config, not a hardcoded "updated_since".
func TestIncrementalSendsConfiguredSinceParam(t *testing.T) {
	var gotSince, gotPage string
	connector, server := newTestConnector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSince = r.URL.Query().Get("updated_after")
		gotPage = r.URL.Query().Get("page")
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
	}))

	since := time.Date(2026, 10, 31, 6, 0, 0, 0, time.UTC)
	items, cursor, err := connector.FetchIncremental(
		context.Background(),
		apiConfig(server, map[string]interface{}{
			"since_param": "updated_after",
			"page_param":  "page",
			"page_size":   1,
		}),
		&types.SyncCursor{LastSyncTime: since},
	)
	require.NoError(t, err)
	assert.Empty(t, items)
	assert.Equal(t, since.Format(time.RFC3339), gotSince)
	assert.Equal(t, "1", gotPage, "incremental still pages from the first page")
	require.NotNil(t, cursor)
	assert.False(t, cursor.LastSyncTime.IsZero())
}

// TestValidateRejectsUnresolvableMapping is why Validate fetches a record: a
// typo in id_path would otherwise sync every record with an empty identity.
func TestValidateRejectsUnresolvableMapping(t *testing.T) {
	connector, server := newTestConnector(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{{"title": "no id here"}})
	}))

	err := connector.Validate(context.Background(), apiConfig(server, nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field mapping")
}

// TestValidateAllowsEmptyList keeps a brand-new source usable: no data yet is
// not a configuration error.
func TestValidateAllowsEmptyList(t *testing.T) {
	connector, server := newTestConnector(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
	}))
	require.NoError(t, connector.Validate(context.Background(), apiConfig(server, nil)))
}

// TestValidateRequiresIdentityMapping covers the config-shape errors.
func TestValidateRequiresIdentityMapping(t *testing.T) {
	connector := NewGenericConnector()
	_, err := connector.FetchAll(context.Background(), &types.DataSourceConfig{
		Settings: map[string]interface{}{"base_url": "https://example.com", "endpoint": "/x"},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "id_path")

	_, err = connector.FetchAll(context.Background(), &types.DataSourceConfig{
		Settings: map[string]interface{}{"base_url": "https://example.com", "id_path": "id", "title_path": "t"},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "endpoint")
}

// TestHTTPErrorSurfacesBody keeps a 4xx/5xx diagnosable instead of decoding an
// error envelope as an empty record list.
func TestHTTPErrorSurfacesBody(t *testing.T) {
	connector, server := newTestConnector(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"token expired"}`))
	}))

	_, err := connector.FetchAll(context.Background(), apiConfig(server, nil), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
	assert.Contains(t, err.Error(), "token expired")
}

// TestLaterPageFailureKeepsPartialResults makes a flaky source degrade to a
// partial sync rather than throwing away the pages that did arrive.
func TestLaterPageFailureKeepsPartialResults(t *testing.T) {
	connector, server := newTestConnector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{{"id": "1", "title": "a"}})
	}))

	items, err := connector.FetchAll(context.Background(), apiConfig(server, map[string]interface{}{
		"page_param": "page",
		"page_size":  1,
	}), nil)
	require.NoError(t, err)
	require.Len(t, items, 1, "page 1's record must survive page 2's failure")
	assert.Equal(t, "business_api:1", items[0].ExternalID)
}

// TestFirstPageFailureFailsTheSync is the other end: with nothing fetched there
// is no partial result to keep.
func TestFirstPageFailureFailsTheSync(t *testing.T) {
	connector, server := newTestConnector(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	_, err := connector.FetchAll(context.Background(), apiConfig(server, nil), nil)
	require.Error(t, err)
}

// TestUnmappableRecordIsSkipped keeps one bad row from failing the whole page.
func TestUnmappableRecordIsSkipped(t *testing.T) {
	connector, server := newTestConnector(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{"title": "missing id"},
			{"id": "2", "title": "good"},
			{"id": "3", "title": ""},
		})
	}))
	items, err := connector.FetchAll(context.Background(), apiConfig(server, nil), nil)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "business_api:2", items[0].ExternalID)
}

// TestCredentialsBecomeHeaders covers auth: secrets live in Credentials so they
// are encrypted at rest, and are sent as headers.
func TestCredentialsBecomeHeaders(t *testing.T) {
	var gotAuth string
	connector, server := newTestConnector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
	}))
	config := apiConfig(server, nil)
	config.Credentials = map[string]interface{}{"Authorization": "Bearer secret"}
	_, err := connector.FetchAll(context.Background(), config, nil)
	require.NoError(t, err)
	assert.Equal(t, "Bearer secret", gotAuth)
}

// TestFieldStringFormatsScalars pins the JSON-number handling that keeps a
// quantity from rendering as "12.000000".
func TestFieldStringFormatsScalars(t *testing.T) {
	record := map[string]interface{}{
		"whole":   float64(12),
		"deci":    float64(1.5),
		"text":    " value ",
		"flag":    true,
		"nested":  map[string]interface{}{"deep": "found"},
		"arr":     []interface{}{"x"},
		"missing": nil,
	}
	assert.Equal(t, "12", fieldString(record, "whole"))
	assert.Equal(t, "1.5", fieldString(record, "deci"))
	assert.Equal(t, "value", fieldString(record, "text"))
	assert.Equal(t, "true", fieldString(record, "flag"))
	assert.Equal(t, "found", fieldString(record, "nested.deep"))
	assert.Empty(t, fieldString(record, "arr"))
	assert.Empty(t, fieldString(record, "missing"))
	assert.Empty(t, fieldString(record, ""))
}

// TestParseGenericTime covers the formats business APIs actually emit.
func TestParseGenericTime(t *testing.T) {
	for _, raw := range []string{
		"2026-11-01T08:30:00Z", "2026-11-01T08:30:00",
		"2026-11-01 08:30:00", "2026-11-01",
	} {
		parsed, err := parseGenericTime(raw)
		assert.NoError(t, err, raw)
		assert.Equal(t, 2026, parsed.Year(), raw)
	}
	_, err := parseGenericTime("01/11/2026")
	assert.Error(t, err)
}

// TestExtractRecordsRejectsBadPaths keeps a typo from silently reading nothing.
func TestExtractRecordsRejectsBadPaths(t *testing.T) {
	decoded := map[string]interface{}{"data": map[string]interface{}{"items": []interface{}{}}}

	records, err := extractRecords(decoded, "data.items")
	require.NoError(t, err)
	assert.Empty(t, records)

	_, err = extractRecords(decoded, "data.missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")

	_, err = extractRecords(decoded, "data.items.deeper")
	require.Error(t, err)

	_, err = extractRecords("not an object", "data")
	require.Error(t, err)
}

// TestListResourcesReturnsTheEndpoint covers the resource picker: the generic
// reader has exactly one selectable resource.
func TestListResourcesReturnsTheEndpoint(t *testing.T) {
	connector, server := newTestConnector(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{{"id": "1", "title": "a"}})
	}))
	config := apiConfig(server, nil)

	resources, err := connector.ListResources(context.Background(), config, "")
	require.NoError(t, err)
	require.Len(t, resources, 1)
	assert.Equal(t, "/api/v1/work-orders", resources[0].ExternalID)
	assert.Equal(t, server.URL+"/api/v1/work-orders", resources[0].URL)

	nested, err := connector.ListResources(context.Background(), config, "/api/v1/work-orders")
	require.NoError(t, err)
	assert.Empty(t, nested, "the list is flat: no children")
}

// TestProbeFailureStillListsTheResource keeps the picker usable when the source
// is temporarily down: the resource is selectable, flagged with the probe error.
func TestProbeFailureStillListsTheResource(t *testing.T) {
	connector, server := newTestConnector(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	resources, err := connector.ListResources(context.Background(), apiConfig(server, nil), "")
	require.NoError(t, err)
	require.Len(t, resources, 1)
	assert.Contains(t, resources[0].Description, "probe failed")
}

// TestTypeIsBusinessAPI keeps registration and the placeholder's error message
// pointing at the same identifier.
func TestTypeIsBusinessAPI(t *testing.T) {
	assert.Equal(t, types.ConnectorTypeBusinessAPI, NewGenericConnector().Type())
}

// TestFingerprintDetectsChange is the change detector used by the sync service.
func TestFingerprintDetectsChange(t *testing.T) {
	first := genericFingerprint("title", "body")
	assert.Equal(t, first, genericFingerprint("title", "body"))
	assert.NotEqual(t, first, genericFingerprint("title", "body "))
	assert.NotEqual(t, first, genericFingerprint("title2", "body"))
}

// TestSanitizeFileName keeps a title from escaping the knowledge naming scheme.
func TestSanitizeFileName(t *testing.T) {
	assert.Equal(t, "E45_停线", sanitizeGenericFileName("E45/停线"))
	assert.Equal(t, "record", sanitizeGenericFileName("   "))

	long := strings.Repeat("很长的标题", 40)
	assert.LessOrEqual(t, len([]rune(sanitizeGenericFileName(long))), 120,
		"an over-long title must be truncated by rune, not byte")
}

// TestNilConfigIsRejected guards the wiring: a nil config must not panic.
func TestNilConfigIsRejected(t *testing.T) {
	connector := NewGenericConnector()
	_, err := connector.FetchAll(context.Background(), nil, nil)
	require.Error(t, err)
	var notImplemented *NotImplementedError
	assert.False(t, errors.As(err, &notImplemented))
}
