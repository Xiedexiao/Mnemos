package wecom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// fakeWeCom serves the two endpoints the connector uses and counts token
// exchanges so the caching behaviour is observable.
type fakeWeCom struct {
	server      *httptest.Server
	tokenCalls  atomic.Int32
	docsByID    map[string][]map[string]interface{}
	contentByID map[string]string
	// failCollections makes listDocuments fail for these collection ids.
	failCollections map[string]bool
}

func newFakeWeCom(t *testing.T) *fakeWeCom {
	t.Helper()
	fake := &fakeWeCom{
		docsByID:    map[string][]map[string]interface{}{},
		contentByID: map[string]string{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/cgi-bin/gettoken", func(w http.ResponseWriter, _ *http.Request) {
		fake.tokenCalls.Add(1)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok","access_token":"tok","expires_in":7200}`))
	})
	mux.HandleFunc("/cgi-bin/wedoc/get_doc_list", func(w http.ResponseWriter, r *http.Request) {
		collectionID := r.URL.Query().Get("collection_id")
		if fake.failCollections[collectionID] {
			_, _ = w.Write([]byte(`{"errcode":50001,"errmsg":"no permission"}`))
			return
		}
		body, _ := json.Marshal(map[string]interface{}{
			"errcode":  0,
			"doclist":  fake.docsByID[collectionID],
			"has_more": false,
		})
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/cgi-bin/wedoc/get_doc_content", func(w http.ResponseWriter, r *http.Request) {
		docID := r.URL.Query().Get("docid")
		body, _ := json.Marshal(map[string]interface{}{
			"errcode": 0,
			"content": fake.contentByID[docID],
		})
		_, _ = w.Write(body)
	})
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeWeCom) config(collectionIDs ...string) *types.DataSourceConfig {
	settings, _ := json.Marshal(map[string]interface{}{
		"collection_ids":   collectionIDs,
		"preserve_folders": true,
	})
	var settingsMap map[string]interface{}
	_ = json.Unmarshal(settings, &settingsMap)
	return &types.DataSourceConfig{
		Type: types.ConnectorTypeWecom,
		Credentials: map[string]interface{}{
			"corp_id":     "corp",
			"corp_secret": "secret",
			"base_url":    f.server.URL,
		},
		Settings: settingsMap,
	}
}

func doc(docID, name, path string, modify int64) map[string]interface{} {
	return map[string]interface{}{
		"docid":       docID,
		"name":        name,
		"doc_type":    "doc",
		"modify_time": modify,
		"create_time": modify - 100,
		"path":        path,
	}
}

// TestValidateExchangesToken covers the credential check.
func TestValidateExchangesToken(t *testing.T) {
	fake := newFakeWeCom(t)
	fake.docsByID["coll-1"] = []map[string]interface{}{doc("d1", "SOP", "/a", 100)}
	connector := newConnectorWithHTTPClient(fake.server.Client())

	require.NoError(t, connector.Validate(context.Background(), fake.config("coll-1")))
	assert.EqualValues(t, 1, fake.tokenCalls.Load())

	// A missing secret is rejected before any network call.
	bad := fake.config("coll-1")
	bad.Credentials["corp_secret"] = ""
	require.Error(t, connector.Validate(context.Background(), bad))
}

// TestFetchAllBuildsItems covers the item shape: external id scoped by
// collection, Markdown body, and the source timestamps the ingest path uses
// for out-of-order protection.
func TestFetchAllBuildsItems(t *testing.T) {
	fake := newFakeWeCom(t)
	fake.docsByID["coll-1"] = []map[string]interface{}{
		doc("d1", "设备保养 SOP", "/sop/eq", 1700000000),
	}
	fake.contentByID["d1"] = "<h1>标题</h1><p>正文</p>"
	connector := newConnectorWithHTTPClient(fake.server.Client())

	items, err := connector.FetchAll(context.Background(), fake.config("coll-1"), nil)
	require.NoError(t, err)
	require.Len(t, items, 1)

	item := items[0]
	assert.Equal(t, "wecom:coll-1:d1", item.ExternalID)
	assert.Equal(t, "设备保养 SOP", item.Title)
	assert.Contains(t, string(item.Content), "标题")
	assert.Contains(t, string(item.Content), "正文")
	assert.Equal(t, "text/markdown", item.ContentType)
	assert.Equal(t, types.ChannelWecom, item.Metadata["channel"])
	assert.Equal(t, "sop/eq", item.Metadata["folder_path"])
	assert.Equal(t, time.Unix(1700000000, 0).UTC(), item.UpdatedAt,
		"the source modified time must reach the ingest path for ordering")
}

// TestFetchIncrementalSkipsUnchangedDocuments covers the modify_time fast path:
// an unchanged document must not trigger a content fetch.
func TestFetchIncrementalSkipsUnchangedDocuments(t *testing.T) {
	fake := newFakeWeCom(t)
	fake.docsByID["coll-1"] = []map[string]interface{}{
		doc("d1", "A", "", 100),
		doc("d2", "B", "", 200),
	}
	fake.contentByID["d1"] = "<p>a</p>"
	fake.contentByID["d2"] = "<p>b</p>"
	connector := newConnectorWithHTTPClient(fake.server.Client())
	config := fake.config("coll-1")

	items, cursor, err := connector.FetchIncremental(context.Background(), config, nil)
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.NotNil(t, cursor)

	// Nothing changed: no items, but the cursor still reflects both documents.
	items, cursor, err = connector.FetchIncremental(context.Background(), config, cursor)
	require.NoError(t, err)
	assert.Empty(t, items)

	// Only d2 moved.
	fake.docsByID["coll-1"] = []map[string]interface{}{
		doc("d1", "A", "", 100),
		doc("d2", "B", "", 300),
	}
	items, _, err = connector.FetchIncremental(context.Background(), config, cursor)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "wecom:coll-1:d2", items[0].ExternalID)
}

// TestFetchIncrementalReportsDeletions covers §3.4: a document missing from the
// listing is reported as a deletion candidate, not silently kept.
func TestFetchIncrementalReportsDeletions(t *testing.T) {
	fake := newFakeWeCom(t)
	fake.docsByID["coll-1"] = []map[string]interface{}{
		doc("d1", "A", "", 100),
		doc("d2", "B", "", 200),
	}
	fake.contentByID["d1"] = "a"
	fake.contentByID["d2"] = "b"
	connector := newConnectorWithHTTPClient(fake.server.Client())
	config := fake.config("coll-1")

	_, cursor, err := connector.FetchIncremental(context.Background(), config, nil)
	require.NoError(t, err)

	// d2 disappears upstream.
	fake.docsByID["coll-1"] = []map[string]interface{}{doc("d1", "A", "", 100)}
	items, _, err := connector.FetchIncremental(context.Background(), config, cursor)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.True(t, items[0].IsDeleted)
	assert.Equal(t, "wecom:coll-1:d2", items[0].ExternalID)
}

// TestPartialCollectionFailureKeepsOthers covers §3.3: one broken collection
// must not fail the whole sync.
func TestPartialCollectionFailureKeepsOthers(t *testing.T) {
	fake := newFakeWeCom(t)
	fake.docsByID["ok"] = []map[string]interface{}{doc("d1", "A", "", 100)}
	fake.contentByID["d1"] = "a"
	fake.failCollections = map[string]bool{"broken": true}
	connector := newConnectorWithHTTPClient(fake.server.Client())

	items, err := connector.FetchAll(context.Background(), fake.config("ok", "broken"), nil)
	require.Error(t, err, "the failure is surfaced")
	assert.Len(t, items, 1, "the healthy collection still syncs")
}

// TestNoCollectionSelectedIsActionable keeps a misconfiguration readable.
func TestNoCollectionSelectedIsActionable(t *testing.T) {
	fake := newFakeWeCom(t)
	connector := newConnectorWithHTTPClient(fake.server.Client())
	_, err := connector.FetchAll(context.Background(), fake.config(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no collection selected")
}

// TestTokenIsCached covers the token cache: many documents must not mean many
// token exchanges.
func TestTokenIsCached(t *testing.T) {
	fake := newFakeWeCom(t)
	for _, id := range []string{"d1", "d2", "d3"} {
		fake.docsByID["coll-1"] = append(fake.docsByID["coll-1"], doc(id, id, "", 100))
		fake.contentByID[id] = "x"
	}
	connector := newConnectorWithHTTPClient(fake.server.Client())
	_, err := connector.FetchAll(context.Background(), fake.config("coll-1"), nil)
	require.NoError(t, err)
	assert.EqualValues(t, 1, fake.tokenCalls.Load())
}

// TestDocumentFingerprintDetectsEdits covers the change detector.
func TestDocumentFingerprintDetectsEdits(t *testing.T) {
	base := documentFingerprint("title", "body")
	assert.Equal(t, base, documentFingerprint("title", "body"))
	assert.NotEqual(t, base, documentFingerprint("title", "body2"))
	assert.NotEqual(t, base, documentFingerprint("title2", "body"))
}

// TestSanitizeFileName keeps titles usable as file names.
func TestSanitizeFileName(t *testing.T) {
	assert.Equal(t, "a_b_c", sanitizeFileName("a/b\\c"))
	assert.Equal(t, "untitled", sanitizeFileName("   "))
	long := make([]rune, 300)
	for i := range long {
		long[i] = 'x'
	}
	assert.LessOrEqual(t, len([]rune(sanitizeFileName(string(long)))), 120)
}

// TestResolveResourceAncestors reveals the collection of a document.
func TestResolveResourceAncestors(t *testing.T) {
	connector := NewConnector()
	ancestors, err := connector.ResolveResourceAncestors(context.Background(), nil,
		[]string{"coll-1:d1", "coll-1:d2", "coll-2"})
	require.NoError(t, err)
	assert.Equal(t, []string{"coll-1"}, ancestors,
		"only document ids have an ancestor, and each collection appears once")
}

// TestDecodeCursorRejectsUnknownVersion keeps a stale cursor from being trusted.
func TestDecodeCursorRejectsUnknownVersion(t *testing.T) {
	assert.Nil(t, decodeCursor(nil))
	assert.Nil(t, decodeCursor(&types.SyncCursor{ConnectorCursor: map[string]interface{}{"v": 999}}))

	valid := encodeCursor(&wecomCursor{
		Version: wecomCursorVersion,
		Docs:    map[string]map[string]int64{"c": {"d": 1}},
	})
	decoded := decodeCursor(valid)
	require.NotNil(t, decoded)
	assert.Equal(t, int64(1), decoded.Docs["c"]["d"])
}
