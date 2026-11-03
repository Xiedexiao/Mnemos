package wecom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/utils"
)

const (
	// requestTimeout bounds one WeCom API call.
	requestTimeout = 20 * time.Second
	// maxResponseSize caps a response body: a hostile or broken endpoint must
	// not be able to exhaust memory.
	maxResponseSize = 8 * 1024 * 1024
	// tokenRefreshSkew renews the access token slightly before it expires, so a
	// call never starts with a token that dies mid-flight.
	tokenRefreshSkew = 60 * time.Second
)

// wecomAPIError is a non-zero errcode returned by the WeCom platform.
type wecomAPIError struct {
	Code    int    `json:"errcode"`
	Message string `json:"errmsg"`
}

func (e *wecomAPIError) Error() string {
	return fmt.Sprintf("wecom api error %d: %s", e.Code, e.Message)
}

// authFailure reports whether the error means the token must be refreshed.
//
// 40014 invalid token / 42001 expired token / 40001 invalid credential are the
// documented auth failures; treating them as retryable-with-refresh avoids
// failing a whole sync over an expired token.
func (e *wecomAPIError) authFailure() bool {
	switch e.Code {
	case 40001, 40014, 42001:
		return true
	default:
		return false
	}
}

// client calls the WeCom document API with a cached access token.
type client struct {
	httpClient *http.Client
	baseURL    string
	corpID     string
	secret     string

	mu     sync.Mutex
	token  string
	expiry time.Time
	now    func() time.Time
}

// newClient builds a client bound to one datasource configuration.
func newClient(corpID, secret, baseURL string) *client {
	cfg := utils.DefaultSSRFSafeHTTPClientConfig()
	cfg.Timeout = requestTimeout
	return &client{
		httpClient: utils.NewSSRFSafeHTTPClient(cfg),
		baseURL:    baseURL,
		corpID:     corpID,
		secret:     secret,
		now:        time.Now,
	}
}

// accessToken returns a live token, refreshing it when needed.
func (c *client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.now().Before(c.expiry.Add(-tokenRefreshSkew)) {
		return c.token, nil
	}
	query := url.Values{}
	query.Set("corpid", c.corpID)
	query.Set("corpsecret", c.secret)
	endpoint := c.baseURL + "/cgi-bin/gettoken?" + query.Encode()

	body, err := c.get(ctx, endpoint)
	if err != nil {
		return "", fmt.Errorf("fetch wecom access token: %w", err)
	}
	var payload struct {
		wecomAPIError
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode wecom access token: %w", err)
	}
	if payload.Code != 0 {
		return "", &payload.wecomAPIError
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("wecom returned an empty access token")
	}
	lifetime := time.Duration(payload.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = 2 * time.Hour
	}
	c.token = payload.AccessToken
	c.expiry = c.now().Add(lifetime)
	return c.token, nil
}

// call performs one authenticated API call and decodes the JSON response.
// It retries exactly once on an auth failure after dropping the cached token,
// which covers a token revoked between refresh and use.
func (c *client) call(ctx context.Context, path string, query url.Values, out interface{}) error {
	for attempt := 0; ; attempt++ {
		token, err := c.accessToken(ctx)
		if err != nil {
			return err
		}
		if query == nil {
			query = url.Values{}
		}
		callQuery := url.Values{}
		for key, values := range query {
			callQuery[key] = values
		}
		callQuery.Set("access_token", token)
		body, err := c.get(ctx, c.baseURL+path+"?"+callQuery.Encode())
		if err != nil {
			return err
		}
		var envelope wecomAPIError
		if err := json.Unmarshal(body, &envelope); err != nil {
			return fmt.Errorf("decode wecom response: %w", err)
		}
		if envelope.Code != 0 {
			if envelope.authFailure() && attempt == 0 {
				c.invalidateToken()
				continue
			}
			return &envelope
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(body, out)
	}
}

// invalidateToken drops the cached token so the next call refreshes it.
func (c *client) invalidateToken() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
	c.expiry = time.Time{}
}

// get performs one HTTP GET with a bounded body.
func (c *client) get(ctx context.Context, endpoint string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("wecom http %d: %s", response.StatusCode, truncateForError(body))
	}
	return body, nil
}

// post performs one authenticated POST.
func (c *client) post(ctx context.Context, path string, payload interface{}, out interface{}) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode wecom request: %w", err)
	}
	query := url.Values{}
	query.Set("access_token", token)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+path+"?"+query.Encode(), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize))
	if err != nil {
		return err
	}
	var envelope wecomAPIError
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode wecom response: %w", err)
	}
	if envelope.Code != 0 {
		return &envelope
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// truncateForError keeps an error message readable.
func truncateForError(body []byte) string {
	const limit = 400
	text := string(body)
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
