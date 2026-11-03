// Package wecom implements the WeCom (企业微信) document connector.
//
// It syncs WeCom doc / smart-sheet collections into a WeKnora knowledge base
// through the WeCom document API (`wedoc`). Each selected collection is a
// selectable resource; each document becomes a knowledge entry rendered as
// Markdown.
//
// Capabilities:
//   - Authoritative source: WeCom stays the source of truth. The knowledge row
//     records the WeCom docid as external_id, so an upstream edit replaces the
//     local copy instead of creating a duplicate (§3.4 权威源).
//   - Incremental: the collection listing carries each document's last-modified
//     time, so unchanged documents are skipped without a content fetch.
//   - Deletion: a document missing from the collection is emitted as a deletion
//     candidate and demoted to "待确认下架" by the ingest path, never hard-deleted
//     (§3.4 删除同步).
//
// All outbound requests go through the SSRF-safe HTTP client, so a crafted
// base URL cannot reach internal services.
package wecom

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
)

// Credentials keys. WeCom's document API is authorised with a corp id + secret
// pair exchanged for a short-lived access_token.
const (
	credCorpID   = "corp_id"
	credSecret   = "corp_secret"
	credAgentID  = "agent_id"
	credBaseURL  = "base_url"
	defaultBase  = "https://qyapi.weixin.qq.com"
	credTokenKey = "access_token"
)

// Config is the non-secret part of a WeCom source: which collections to sync
// and how the document tree maps onto folders.
type Config struct {
	// CollectionIDs are the WeCom doc collections to sync. Stored in Settings
	// because they are selection state, not secrets.
	CollectionIDs []string `json:"collection_ids"`
	// DocumentType restricts the sync to one WeCom document kind: doc,
	// spreadsheet or both. Empty means both.
	DocumentType string `json:"document_type,omitempty"`
	// PreserveFolders keeps the collection hierarchy as folder paths.
	PreserveFolders bool `json:"preserve_folders"`
}

// parseConfig decodes Settings, tolerating the shapes older rows used.
func parseConfig(config *types.DataSourceConfig) (*Config, error) {
	if config == nil {
		return nil, fmt.Errorf("data source config is required")
	}
	cfg := &Config{}
	if len(config.Settings) == 0 {
		return cfg, nil
	}
	raw, err := json.Marshal(config.Settings)
	if err != nil {
		return nil, fmt.Errorf("encode settings: %w", err)
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("decode settings: %w", err)
	}
	return cfg, nil
}

// credentials reads and validates the secret half of the configuration.
func credentials(config *types.DataSourceConfig) (corpID, secret, baseURL string, err error) {
	if config == nil || len(config.Credentials) == 0 {
		return "", "", "", fmt.Errorf("WeCom corp_id and corp_secret are required")
	}
	corpID = stringCredential(config, credCorpID)
	secret = stringCredential(config, credSecret)
	baseURL = stringCredential(config, credBaseURL)
	if corpID == "" || secret == "" {
		return "", "", "", fmt.Errorf("WeCom corp_id and corp_secret are required")
	}
	if baseURL == "" {
		baseURL = defaultBase
	}
	return corpID, secret, strings.TrimRight(baseURL, "/"), nil
}

// stringCredential reads one credential value as a trimmed string.
func stringCredential(config *types.DataSourceConfig, key string) string {
	if config == nil || config.Credentials == nil {
		return ""
	}
	raw, ok := config.Credentials[key]
	if !ok {
		return ""
	}
	value, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// wecomCursor is the resumable sync state. It records the last-modified instant
// per document so an unchanged document is skipped without a content fetch, and
// so a resumed sync after a timeout converges.
type wecomCursor struct {
	Version      int                         `json:"v"`
	LastSyncTime time.Time                   `json:"last_sync_time"`
	Docs         map[string]map[string]int64 `json:"docs"`
}

// wecomCursorVersion lets a future shape change invalidate old cursors safely.
const wecomCursorVersion = 1

// encodeCursor renders the cursor into the generic SyncCursor shape.
func encodeCursor(cursor *wecomCursor) *types.SyncCursor {
	if cursor == nil {
		return nil
	}
	raw, err := json.Marshal(cursor)
	if err != nil {
		return nil
	}
	var bag map[string]interface{}
	if err := json.Unmarshal(raw, &bag); err != nil {
		return nil
	}
	return &types.SyncCursor{LastSyncTime: cursor.LastSyncTime, ConnectorCursor: bag}
}

// decodeCursor reads a SyncCursor back, returning nil when the shape is
// unusable so the caller falls back to a full sync rather than failing.
func decodeCursor(syncCursor *types.SyncCursor) *wecomCursor {
	if syncCursor == nil || syncCursor.ConnectorCursor == nil {
		return nil
	}
	raw, err := json.Marshal(syncCursor.ConnectorCursor)
	if err != nil {
		return nil
	}
	var cursor wecomCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return nil
	}
	if cursor.Version != wecomCursorVersion || cursor.Docs == nil {
		return nil
	}
	return &cursor
}

// documentFingerprint is the change detector for one document: title plus
// content digest. WeCom's own modify time is used as a fast path; the
// fingerprint catches an edit that did not advance it.
func documentFingerprint(title, content string) string {
	sum := sha256.Sum256([]byte(title + "\x00" + content))
	return hex.EncodeToString(sum[:])
}

// itemExternalID is the stable identity of one WeCom document inside the
// datasource. Scoping by collection keeps two collections that happen to expose
// the same docid from colliding (§3.3 external_id scoping).
func itemExternalID(collectionID, docID string) string {
	return "wecom:" + collectionID + ":" + docID
}

// sanitizeFileName turns a document title into a safe file name.
func sanitizeFileName(title string) string {
	cleaned := strings.TrimSpace(title)
	if cleaned == "" {
		cleaned = "untitled"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_", "\n", " ", "\r", " ")
	cleaned = replacer.Replace(cleaned)
	if runes := []rune(cleaned); len(runes) > 120 {
		cleaned = string(runes[:120])
	}
	return cleaned
}

// partialFetch keeps a per-collection failure from failing the whole sync when
// other collections succeeded (§3.3 失败与对账).
func partialFetch(details []string) error {
	if len(details) == 0 {
		return nil
	}
	return &datasource.PartialFetchError{Details: details}
}
