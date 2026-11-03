package business

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
)

// 通用业务系统接入（§3.3）。
//
// 七个专用连接器（ERP/MES/PLM/QMS/OA/HR/CRM）都在等各自部署方的接口契约。
// 在契约到位之前，这是一个能真正跑起来的通用读法：配置一个 HTTP+JSON 列表
// 接口与字段映射，就能把业务记录同步成知识。
//
// 设计取舍：
//   - 只做只读列表 + 按 updated_at 增量。业务系统的写入仍回原系统，这是 §3.3
//     的硬边界（平台不做交易主库）。
//   - 字段映射必须显式配置：不猜字段名。猜错会把"设备号"当"标题"，产出错误
//     知识比不产出更糟。
//   - 记录正文由映射拼接成 Markdown（标题 + 各字段），保留业务键，便于回查。

const (
	// genericRequestTimeout bounds one page fetch.
	genericRequestTimeout = 20 * time.Second
	// genericMaxResponseSize caps a page so a hostile or broken endpoint cannot
	// exhaust memory.
	genericMaxResponseSize = 8 * 1024 * 1024
	// genericDefaultPageSize is the page size sent when the config omits one.
	genericDefaultPageSize = 100
	// genericMaxPages bounds a full sync so a paging bug cannot loop forever.
	genericMaxPages = 1000
)

// GenericConfig is the non-secret half of a business_api source.
type GenericConfig struct {
	// BaseURL is the API root; Endpoint is joined onto it.
	BaseURL string `json:"base_url"`
	// Endpoint is the list path, e.g. /api/v1/work-orders.
	Endpoint string `json:"endpoint"`
	// RecordsPath is the JSON path to the record array inside the response,
	// e.g. "data.items". Empty means the response body is the array.
	RecordsPath string `json:"records_path,omitempty"`
	// IDPath / TitlePath / UpdatedAtPath / BodyTemplate map a record onto a
	// FetchedItem. IDPath and TitlePath are required: without a stable id the
	// sync cannot dedupe, and without a title the knowledge row is unnamed.
	IDPath        string `json:"id_path"`
	TitlePath     string `json:"title_path"`
	UpdatedAtPath string `json:"updated_at_path,omitempty"`
	// BodyFields lists the record fields rendered into the Markdown body, in
	// order. Empty falls back to the title only.
	BodyFields []string `json:"body_fields,omitempty"`
	// PageParam / PageSizeParam / PageSize describe the paging contract.
	PageParam     string `json:"page_param,omitempty"`
	PageSizeParam string `json:"page_size_param,omitempty"`
	PageSize      int    `json:"page_size,omitempty"`
	// SinceParam is the query parameter that carries the incremental cursor
	// (e.g. "updated_since", "updated_after"). Empty disables incremental
	// filtering: every sync then reads the full list and dedupes by external id.
	SinceParam string `json:"since_param,omitempty"`
	// Channel is the knowledge source label recorded on synced rows.
	Channel string `json:"channel,omitempty"`
}

// GenericConnector reads one business system through a configured HTTP+JSON
// list endpoint.
type GenericConnector struct {
	// httpClient overrides the SSRF-safe client (tests only).
	httpClient *http.Client
	now        func() time.Time
}

// Compile-time proof of the connector contract.
var _ datasource.Connector = (*GenericConnector)(nil)

// NewGenericConnector constructs the generic business reader.
func NewGenericConnector() *GenericConnector {
	return &GenericConnector{now: time.Now}
}

// newGenericConnectorWithHTTPClient builds a reader bound to a specific client
// (tests only).
func newGenericConnectorWithHTTPClient(client *http.Client) *GenericConnector {
	return &GenericConnector{httpClient: client, now: time.Now}
}

// Type returns the connector identifier.
func (c *GenericConnector) Type() string { return types.ConnectorTypeBusinessAPI }

// client returns the HTTP client to use, preferring the injected one.
func (c *GenericConnector) client() *http.Client {
	if c.httpClient != nil {
		return c.httpClient
	}
	cfg := utils.DefaultSSRFSafeHTTPClientConfig()
	cfg.Timeout = genericRequestTimeout
	return utils.NewSSRFSafeHTTPClient(cfg)
}

// genericCredentials reads the non-secret config and the auth headers.
func genericCredentials(config *types.DataSourceConfig) (*GenericConfig, url.Values, error) {
	if config == nil {
		return nil, nil, fmt.Errorf("data source config is required")
	}
	cfg := &GenericConfig{}
	if len(config.Settings) > 0 {
		raw, err := json.Marshal(config.Settings)
		if err != nil {
			return nil, nil, fmt.Errorf("encode settings: %w", err)
		}
		if err := json.Unmarshal(raw, cfg); err != nil {
			return nil, nil, fmt.Errorf("decode settings: %w", err)
		}
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	if cfg.BaseURL == "" || cfg.Endpoint == "" {
		return nil, nil, fmt.Errorf("base_url and endpoint are required")
	}
	if strings.TrimSpace(cfg.IDPath) == "" || strings.TrimSpace(cfg.TitlePath) == "" {
		return nil, nil, fmt.Errorf("id_path and title_path are required: without a stable id the sync cannot dedupe, and without a title the entry is unnamed")
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = genericDefaultPageSize
	}

	// Auth headers live in Credentials: they carry a token and must be
	// encrypted at rest, unlike the mapping above which is ordinary config.
	headers := url.Values{}
	if config.Credentials != nil {
		for key, value := range config.Credentials {
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) == "" {
				continue
			}
			headers.Set(key, text)
		}
	}
	return cfg, headers, nil
}

// Validate fetches one page and checks the mapping resolves on a real record.
//
// 只验证连通性是不够的：映射写错（比如 id_path 指向一个不存在的字段）会
// 让同步"成功但全是空标题"。这里在第一条记录上真的跑一次取值。
func (c *GenericConnector) Validate(ctx context.Context, config *types.DataSourceConfig) error {
	cfg, headers, err := genericCredentials(config)
	if err != nil {
		return err
	}
	records, err := c.fetchPage(ctx, cfg, headers, 1, "")
	if err != nil {
		return err
	}
	if len(records) == 0 {
		// 空列表是合法状态（新系统还没有数据），无法验证映射，直接通过。
		logger.Infof(ctx, "[business_api] %s%s returned no records; mapping not exercised", cfg.BaseURL, cfg.Endpoint)
		return nil
	}
	if _, err := buildGenericItem(cfg, records[0], cfg.Channel); err != nil {
		return fmt.Errorf("field mapping does not resolve on the first record: %w", err)
	}
	return nil
}

// ListResources reports the endpoint as a single selectable resource.
//
// 通用接入没有比"这个接口"更细的资源边界：分页接口就是全部内容。
func (c *GenericConnector) ListResources(
	ctx context.Context, config *types.DataSourceConfig, parentID string,
) ([]types.Resource, error) {
	if parentID != "" {
		return []types.Resource{}, nil
	}
	cfg, headers, err := genericCredentials(config)
	if err != nil {
		return nil, err
	}
	resource := types.Resource{
		ExternalID: cfg.Endpoint,
		Type:       "endpoint",
		Name:       cfg.Endpoint,
		URL:        cfg.BaseURL + cfg.Endpoint,
	}
	records, fetchErr := c.fetchPage(ctx, cfg, headers, 1, "")
	if fetchErr != nil {
		logger.Warnf(ctx, "[business_api] list resource probe failed: %v", fetchErr)
		resource.Description = "probe failed: " + fetchErr.Error()
		return []types.Resource{resource}, nil
	}
	resource.Metadata = map[string]interface{}{"page_size_returned": len(records)}
	return []types.Resource{resource}, nil
}

// ResolveResourceAncestors has nothing to do: the endpoint is a flat list.
func (c *GenericConnector) ResolveResourceAncestors(
	ctx context.Context, config *types.DataSourceConfig, resourceIDs []string,
) ([]string, error) {
	return []string{}, nil
}

// FetchAll pages through the whole list.
func (c *GenericConnector) FetchAll(
	ctx context.Context, config *types.DataSourceConfig, resourceIDs []string,
) ([]types.FetchedItem, error) {
	items, _, err := c.walk(ctx, config, nil)
	return items, err
}

// FetchIncremental pages with the updated_since cursor when configured.
func (c *GenericConnector) FetchIncremental(
	ctx context.Context, config *types.DataSourceConfig, cursor *types.SyncCursor,
) ([]types.FetchedItem, *types.SyncCursor, error) {
	since := ""
	if cursor != nil && !cursor.LastSyncTime.IsZero() {
		since = cursor.LastSyncTime.UTC().Format(time.RFC3339)
	}
	return c.walk(ctx, config, &since)
}

// walk pages through the endpoint, stopping at the first short page.
//
// 不依赖响应里的 total/has_more 字段：不同系统的分页契约差异很大，而"这一
// 页比 page_size 少就到底了"是所有分页接口都成立的性质。
func (c *GenericConnector) walk(
	ctx context.Context, config *types.DataSourceConfig, since *string,
) ([]types.FetchedItem, *types.SyncCursor, error) {
	cfg, headers, err := genericCredentials(config)
	if err != nil {
		return nil, nil, err
	}
	sinceValue := ""
	if since != nil {
		sinceValue = *since
	}

	var out []types.FetchedItem
	for page := 1; page <= genericMaxPages; page++ {
		records, fetchErr := c.fetchPage(ctx, cfg, headers, page, sinceValue)
		if fetchErr != nil {
			if page == 1 {
				return nil, nil, fetchErr
			}
			// 后续页失败：保留已取到的部分，让同步以 partial 结束，而不是
			// 丢掉整轮成果。
			logger.Warnf(ctx, "[business_api] page %d failed after %d record(s): %v",
				page, len(out), fetchErr)
			break
		}
		for _, record := range records {
			item, buildErr := buildGenericItem(cfg, record, cfg.Channel)
			if buildErr != nil {
				logger.Warnf(ctx, "[business_api] skipping unmappable record: %v", buildErr)
				continue
			}
			out = append(out, *item)
		}
		if len(records) < cfg.PageSize {
			break
		}
	}

	// 游标语义：下一次增量从"这次开始的时间"往后取，避免同步过程中的
	// 新记录被漏掉（源系统按 updated_at 升序返回是通用假设）。
	next := &types.SyncCursor{LastSyncTime: c.now().UTC()}
	return out, next, nil
}

// fetchPage performs one page request and returns the raw records.
func (c *GenericConnector) fetchPage(
	ctx context.Context, cfg *GenericConfig, headers url.Values, page int, since string,
) ([]map[string]interface{}, error) {
	endpoint := cfg.BaseURL + cfg.Endpoint
	query := url.Values{}
	if cfg.PageParam != "" {
		query.Set(cfg.PageParam, fmt.Sprintf("%d", page))
	}
	if cfg.PageSizeParam != "" {
		query.Set(cfg.PageSizeParam, fmt.Sprintf("%d", cfg.PageSize))
	}
	if since != "" && cfg.SinceParam != "" {
		query.Set(cfg.SinceParam, since)
	}
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	for key, values := range headers {
		if len(values) > 0 {
			request.Header.Set(key, values[0])
		}
	}

	response, err := c.client().Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, genericMaxResponseSize))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("business api returned http %d: %s", response.StatusCode, truncateBody(body))
	}

	var decoded interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode business api response: %w", err)
	}
	return extractRecords(decoded, cfg.RecordsPath)
}

// extractRecords walks the dotted RecordsPath to the record array.
func extractRecords(decoded interface{}, recordsPath string) ([]map[string]interface{}, error) {
	node := decoded
	if recordsPath != "" {
		for _, part := range strings.Split(recordsPath, ".") {
			object, ok := node.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("records_path %q does not resolve: %q is not an object", recordsPath, part)
			}
			node, ok = object[part]
			if !ok {
				return nil, fmt.Errorf("records_path %q: field %q is missing", recordsPath, part)
			}
		}
	}
	array, ok := node.([]interface{})
	if !ok {
		return nil, fmt.Errorf("records_path %q does not point at an array", recordsPath)
	}
	out := make([]map[string]interface{}, 0, len(array))
	for _, entry := range array {
		if object, ok := entry.(map[string]interface{}); ok {
			out = append(out, object)
		}
	}
	return out, nil
}

// buildGenericItem maps one record onto a FetchedItem.
func buildGenericItem(
	cfg *GenericConfig, record map[string]interface{}, channel string,
) (*types.FetchedItem, error) {
	id := fieldString(record, cfg.IDPath)
	if id == "" {
		return nil, fmt.Errorf("id_path %q is empty on this record", cfg.IDPath)
	}
	title := fieldString(record, cfg.TitlePath)
	if title == "" {
		return nil, fmt.Errorf("title_path %q is empty on this record", cfg.TitlePath)
	}

	// 正文按配置的字段顺序渲染成 Markdown：保留业务键，便于回查原文。
	var builder strings.Builder
	builder.WriteString("# " + title + "\n\n")
	for _, field := range cfg.BodyFields {
		value := fieldString(record, field)
		if value == "" {
			continue
		}
		builder.WriteString(fmt.Sprintf("- **%s**: %s\n", field, value))
	}
	content := builder.String()

	item := &types.FetchedItem{
		ExternalID:       "business_api:" + id,
		Title:            title,
		Content:          []byte(content),
		ContentType:      "text/markdown",
		FileName:         sanitizeGenericFileName(title) + ".md",
		SourceResourceID: cfg.Endpoint,
		Metadata: map[string]string{
			"channel":     channelOrDefault(channel),
			"business_id": id,
			"fingerprint": genericFingerprint(title, content),
		},
	}
	if cfg.UpdatedAtPath != "" {
		if raw := fieldString(record, cfg.UpdatedAtPath); raw != "" {
			// 解析失败只是没有时间戳，不影响入库：留空即可，乱序保护会跳过
			// 无时间戳的来件。
			if parsed, parseErr := parseGenericTime(raw); parseErr == nil {
				item.UpdatedAt = parsed
			}
		}
	}
	return item, nil
}

// fieldString reads a dotted path out of a record as a string.
func fieldString(record map[string]interface{}, path string) string {
	if record == nil || path == "" {
		return ""
	}
	var node interface{} = record
	for _, part := range strings.Split(path, ".") {
		object, ok := node.(map[string]interface{})
		if !ok {
			return ""
		}
		node, ok = object[part]
		if !ok {
			return ""
		}
	}
	switch typed := node.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		// JSON 数字统一是 float64；整数要还原成 "12" 而不是 "12.000000"。
		if typed == float64(int64(typed)) {
			return fmt.Sprintf("%d", int64(typed))
		}
		return fmt.Sprintf("%v", typed)
	case bool:
		return fmt.Sprintf("%v", typed)
	default:
		return ""
	}
}

// parseGenericTime accepts the timestamp formats business APIs commonly emit.
func parseGenericTime(raw string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02",
	} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp %q", raw)
}

// genericFingerprint is the change detector for one record.
func genericFingerprint(title, content string) string {
	sum := sha256.Sum256([]byte(title + "\x00" + content))
	return hex.EncodeToString(sum[:])
}

// sanitizeGenericFileName turns a title into a safe file name.
func sanitizeGenericFileName(title string) string {
	cleaned := strings.TrimSpace(title)
	if cleaned == "" {
		cleaned = "record"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_", "\n", " ", "\r", " ")
	cleaned = replacer.Replace(cleaned)
	if runes := []rune(cleaned); len(runes) > 120 {
		cleaned = string(runes[:120])
	}
	return cleaned
}

// channelOrDefault keeps the knowledge source label meaningful.
func channelOrDefault(channel string) string {
	if strings.TrimSpace(channel) != "" {
		return strings.TrimSpace(channel)
	}
	return types.ConnectorTypeBusinessAPI
}

// truncateBody keeps an error message readable.
func truncateBody(body []byte) string {
	const limit = 300
	text := string(body)
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
