package wecom

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	htmltomd "github.com/JohannesKaufmann/html-to-markdown/v2"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// Compile-time proof that *Connector satisfies the datasource.Connector contract.
var _ datasource.Connector = (*Connector)(nil)

// Connector implements datasource.Connector for WeCom documents.
type Connector struct {
	// httpClient overrides the SSRF-safe client. Left nil in production: the
	// SSRF client is what keeps a crafted base_url from reaching internal
	// services. Tests inject a plain client to talk to an httptest server,
	// which the SSRF policy correctly blocks (127.0.0.1).
	httpClient *http.Client
}

// NewConnector creates a WeCom connector.
func NewConnector() *Connector { return &Connector{} }

// newConnectorWithHTTPClient builds a connector bound to a specific HTTP
// client. Only tests use it.
func newConnectorWithHTTPClient(client *http.Client) *Connector {
	return &Connector{httpClient: client}
}

// Type returns the connector identifier.
func (c *Connector) Type() string { return types.ConnectorTypeWecom }

// validateAuth exchanges the corp credentials for a token, which is the only
// reliable way to know the pair is usable.
func (c *Connector) validateAuth(ctx context.Context, config *types.DataSourceConfig) (*client, *Config, error) {
	corpID, secret, baseURL, err := credentials(config)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := parseConfig(config)
	if err != nil {
		return nil, nil, err
	}
	cli := newClient(corpID, secret, baseURL)
	if c.httpClient != nil {
		cli.httpClient = c.httpClient
	}
	if _, err := cli.accessToken(ctx); err != nil {
		return nil, nil, err
	}
	return cli, cfg, nil
}

// Validate verifies the credentials and that every selected collection exists.
func (c *Connector) Validate(ctx context.Context, config *types.DataSourceConfig) error {
	cli, cfg, err := c.validateAuth(ctx, config)
	if err != nil {
		return err
	}
	if len(cfg.CollectionIDs) == 0 {
		// No selection yet: verifying credentials is all that can be checked.
		return nil
	}
	for _, collectionID := range cfg.CollectionIDs {
		if _, err := c.listDocuments(ctx, cli, collectionID); err != nil {
			return fmt.Errorf("list collection %s: %w", collectionID, err)
		}
	}
	return nil
}

// ResolveResourceAncestors returns the collection of each document. WeCom has
// no deeper nesting than collection → document, so one hop is enough.
func (c *Connector) ResolveResourceAncestors(
	ctx context.Context, config *types.DataSourceConfig, resourceIDs []string,
) ([]string, error) {
	// Resource IDs are "collectionID" or "collectionID:docID". Only the latter
	// has an ancestor to reveal.
	seen := make(map[string]struct{}, len(resourceIDs))
	out := make([]string, 0, len(resourceIDs))
	for _, resourceID := range resourceIDs {
		collectionID, _, found := strings.Cut(resourceID, ":")
		if !found {
			continue
		}
		if _, ok := seen[collectionID]; ok {
			continue
		}
		seen[collectionID] = struct{}{}
		out = append(out, collectionID)
	}
	return out, nil
}

// wecomDocument is one row of a collection listing.
type wecomDocument struct {
	DocID      string `json:"docid"`
	Name       string `json:"name"`
	DocType    string `json:"doc_type"`
	ApperType  int    `json:"apper_type"`
	ModifyTime int64  `json:"modify_time"`
	CreateTime int64  `json:"create_time"`
	Path       string `json:"path"`
}

// listDocuments returns one collection's documents.
//
// The collection listing is paginated; this walks it fully. A per-collection
// failure is returned so the caller can decide whether it is fatal.
func (c *Connector) listDocuments(
	ctx context.Context, cli *client, collectionID string,
) ([]wecomDocument, error) {
	var out []wecomDocument
	for offset := 0; ; {
		query := url.Values{}
		query.Set("collection_id", collectionID)
		query.Set("offset", fmt.Sprintf("%d", offset))
		query.Set("limit", "100")

		var payload struct {
			DocList []wecomDocument `json:"doclist"`
			HasMore bool            `json:"has_more"`
		}
		if err := cli.call(ctx, "/cgi-bin/wedoc/get_doc_list", query, &payload); err != nil {
			return nil, err
		}
		out = append(out, payload.DocList...)
		if !payload.HasMore || len(payload.DocList) == 0 {
			break
		}
		offset += len(payload.DocList)
	}
	return out, nil
}

// fetchDocumentContent returns a document's body as Markdown.
//
// WeCom serves document bodies as HTML through the wedoc content API; the
// connector converts to Markdown so the ingest path sees the same shape as
// every other text source.
func (c *Connector) fetchDocumentContent(
	ctx context.Context, cli *client, collectionID, docID string,
) (string, error) {
	query := url.Values{}
	query.Set("collection_id", collectionID)
	query.Set("docid", docID)

	var payload struct {
		Content string `json:"content"`
	}
	if err := cli.call(ctx, "/cgi-bin/wedoc/get_doc_content", query, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Content) == "" {
		return "", nil
	}
	converted, err := htmltomd.ConvertString(payload.Content)
	if err != nil || strings.TrimSpace(converted) == "" {
		// Conversion failure must not drop the document: keep the raw text so a
		// reviewer can still find it, exactly as the RSS connector does.
		return strings.TrimSpace(payload.Content), nil
	}
	return strings.TrimSpace(converted), nil
}

// ListResources returns each configured collection as a resource.
func (c *Connector) ListResources(
	ctx context.Context, config *types.DataSourceConfig, parentID string,
) ([]types.Resource, error) {
	cli, cfg, err := c.validateAuth(ctx, config)
	if err != nil {
		return nil, err
	}
	// Collections are flat: a lazy-load request for a specific parent has
	// nothing extra to return.
	if parentID != "" {
		return []types.Resource{}, nil
	}
	out := make([]types.Resource, 0, len(cfg.CollectionIDs))
	for _, collectionID := range cfg.CollectionIDs {
		resource := types.Resource{
			ExternalID: collectionID,
			Type:       "collection",
			Name:       collectionID,
		}
		docs, listErr := c.listDocuments(ctx, cli, collectionID)
		if listErr != nil {
			logger.Warnf(ctx, "[WeCom] list collection %s failed: %v", collectionID, listErr)
			resource.Description = "list failed: " + listErr.Error()
			out = append(out, resource)
			continue
		}
		resource.Metadata = map[string]interface{}{"item_count": len(docs)}
		out = append(out, resource)
	}
	return out, nil
}

// FetchAll performs a full sync of the selected collections.
func (c *Connector) FetchAll(
	ctx context.Context, config *types.DataSourceConfig, resourceIDs []string,
) ([]types.FetchedItem, error) {
	items, _, err := c.walk(ctx, config, resourceIDs, nil, false)
	return items, err
}

// FetchIncremental returns only documents changed since the cursor.
func (c *Connector) FetchIncremental(
	ctx context.Context, config *types.DataSourceConfig, cursor *types.SyncCursor,
) ([]types.FetchedItem, *types.SyncCursor, error) {
	prev := decodeCursor(cursor)
	items, next, err := c.walk(ctx, config, config.ResourceIDs, prev, true)
	return items, encodeCursor(next), err
}

// walk is the shared sync implementation.
//
// Deletions are emitted as IsDeleted items when a document recorded in the
// previous cursor is missing from the listing, which is what lets the ingest
// path demote it to "待确认下架" instead of leaving a stale copy (§3.4).
func (c *Connector) walk(
	ctx context.Context,
	config *types.DataSourceConfig,
	resourceIDs []string,
	prev *wecomCursor,
	incremental bool,
) ([]types.FetchedItem, *wecomCursor, error) {
	cli, cfg, err := c.validateAuth(ctx, config)
	if err != nil {
		return nil, nil, err
	}

	collections := resourceIDs
	if len(collections) == 0 {
		collections = cfg.CollectionIDs
	}
	if len(collections) == 0 {
		return nil, nil, fmt.Errorf("no collection selected: pick at least one WeCom document collection")
	}

	next := &wecomCursor{
		Version:      wecomCursorVersion,
		LastSyncTime: time.Now().UTC(),
		Docs:         make(map[string]map[string]int64, len(collections)),
	}

	var out []types.FetchedItem
	var failures []string

	for _, collectionID := range collections {
		docs, listErr := c.listDocuments(ctx, cli, collectionID)
		if listErr != nil {
			// 单个集合失败不阻断其余集合：失败的集合保留旧游标，下一轮重试
			// （§3.3 失败与对账）。
			logger.Warnf(ctx, "[WeCom] list collection %s failed: %v", collectionID, listErr)
			failures = append(failures, fmt.Sprintf("%s: %v", collectionID, listErr))
			if prev != nil && prev.Docs[collectionID] != nil {
				next.Docs[collectionID] = prev.Docs[collectionID]
			}
			continue
		}

		next.Docs[collectionID] = make(map[string]int64, len(docs))
		seen := make(map[string]struct{}, len(docs))
		var prevDocs map[string]int64
		if incremental && prev != nil {
			prevDocs = prev.Docs[collectionID]
		}

		for _, doc := range docs {
			if doc.DocID == "" {
				continue
			}
			seen[doc.DocID] = struct{}{}
			next.Docs[collectionID][doc.DocID] = doc.ModifyTime

			// 快路径：源系统自己的修改时间未变就不拉正文。WeCom 的
			// modify_time 是秒级，够用作"没动过"的判据。
			if prevDocs != nil {
				if previousModified, ok := prevDocs[doc.DocID]; ok && previousModified == doc.ModifyTime {
					continue
				}
			}

			content, contentErr := c.fetchDocumentContent(ctx, cli, collectionID, doc.DocID)
			if contentErr != nil {
				logger.Warnf(ctx, "[WeCom] fetch %s/%s failed: %v", collectionID, doc.DocID, contentErr)
				failures = append(failures, fmt.Sprintf("%s/%s: %v", collectionID, doc.DocID, contentErr))
				if prevDocs != nil {
					if previousModified, ok := prevDocs[doc.DocID]; ok {
						next.Docs[collectionID][doc.DocID] = previousModified
					}
				}
				continue
			}

			out = append(out, c.buildItem(collectionID, doc, content, cfg))
		}

		// 删除：旧游标里有、这次列表没有的文档，作为删除候选上报。真正的
		// 处理（下架/归档）由 ingest 决定，连接器只负责如实报告。
		if prevDocs != nil {
			for docID := range prevDocs {
				if _, ok := seen[docID]; ok {
					continue
				}
				out = append(out, types.FetchedItem{
					ExternalID:       itemExternalID(collectionID, docID),
					Title:            docID,
					SourceResourceID: collectionID,
					IsDeleted:        true,
					Metadata: map[string]string{
						"channel":         types.ChannelWecom,
						"collection_id":   collectionID,
						"doc_id":          docID,
						"deletion_reason": "absent_from_source_listing",
					},
				})
			}
		}
	}

	if len(failures) > 0 {
		if len(out) == 0 {
			return nil, next, fmt.Errorf("all collections failed: %s", strings.Join(failures, "; "))
		}
		return out, next, partialFetch(failures)
	}
	return out, next, nil
}

// buildItem turns one WeCom document into a FetchedItem.
func (c *Connector) buildItem(
	collectionID string, doc wecomDocument, content string, cfg *Config,
) types.FetchedItem {
	title := strings.TrimSpace(doc.Name)
	if title == "" {
		title = doc.DocID
	}
	folderPath := ""
	if cfg.PreserveFolders {
		folderPath = strings.Trim(strings.TrimSpace(doc.Path), "/")
	}
	metadata := map[string]string{
		"channel":       types.ChannelWecom,
		"collection_id": collectionID,
		"doc_id":        doc.DocID,
		"doc_type":      doc.DocType,
		"fingerprint":   documentFingerprint(title, content),
	}
	if folderPath != "" {
		metadata["folder_path"] = folderPath
	}
	item := types.FetchedItem{
		ExternalID:       itemExternalID(collectionID, doc.DocID),
		Title:            title,
		Content:          []byte(content),
		ContentType:      "text/markdown",
		FileName:         sanitizeFileName(title) + ".md",
		SourceResourceID: collectionID,
		Metadata:         metadata,
	}
	if doc.ModifyTime > 0 {
		item.UpdatedAt = time.Unix(doc.ModifyTime, 0).UTC()
	}
	if doc.CreateTime > 0 {
		item.CreatedAt = time.Unix(doc.CreateTime, 0).UTC()
	}
	return item
}

// decodeDocumentList is a small helper kept for tests that need to exercise the
// listing envelope without a live API.
func decodeDocumentList(raw []byte) ([]wecomDocument, error) {
	var payload struct {
		DocList []wecomDocument `json:"doclist"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return payload.DocList, nil
}
