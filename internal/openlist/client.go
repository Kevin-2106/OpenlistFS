package openlist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Sentinel errors the callers can test with errors.Is.
var (
	ErrAuth       = errors.New("openlist: authentication failed")
	ErrPermission = errors.New("openlist: permission denied")
	ErrNotFound   = errors.New("openlist: object not found")
)

// Error is an API-level failure. The Message never contains credentials, the
// Authorization header, or signed URL parameters.
type Error struct {
	Op      string // API operation, e.g. "list"
	Path    string // virtual path the operation targeted
	Code    int    // body "code" field from the server
	Message string // body "message" field
}

func (e *Error) Error() string {
	return fmt.Sprintf("openlist: %s %s: %s (code=%d)", e.Op, e.Path, e.Message, e.Code)
}

func (e *Error) Unwrap() error {
	msg := strings.ToLower(e.Message)
	switch {
	case e.Code == 401 || strings.Contains(msg, "unauthorized") || strings.Contains(msg, "token"):
		return ErrAuth
	case e.Code == 403 || strings.Contains(msg, "permission") || strings.Contains(msg, "not an admin"):
		return ErrPermission
	case strings.Contains(msg, "not found"):
		return ErrNotFound
	}
	return nil
}

// Client talks to one OpenList server. It is safe for concurrent use.
type Client struct {
	BaseURL string // no trailing slash
	HTTP    *http.Client

	mu        sync.Mutex
	token     string
	username  string
	password  string
	fsGetOnly bool // fs/link was denied; resolve via fs/get from now on
}

type Option func(*Client)

// WithToken sets a pre-obtained API token.
func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

// WithCredentials enables login and automatic re-login when the token
// expires. The credentials are never logged or embedded in errors.
func WithCredentials(username, password string) Option {
	return func(c *Client) { c.username, c.password = username, password }
}

// WithHTTPClient overrides the HTTP client (tests inject their own transport).
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.HTTP = hc }
}

// New creates a client for the server at baseURL.
func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          16,
				MaxIdleConnsPerHost:   16,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 10 * time.Second,
				ExpectContinueTimeout: time.Second,
			},
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// SanitizeURL renders u for logs: the query string (which carries signatures)
// is replaced by a placeholder.
func SanitizeURL(u string) string {
	i := strings.IndexByte(u, '?')
	if i < 0 {
		return u
	}
	return u[:i] + "?<redacted>"
}

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// call performs one API request. When out is non-nil the "data" field is
// decoded into it. Transport failures and 502/503/504 are retried — this
// server's connection resets frequently enough that a single attempt makes
// browsing unreliable.
func (c *Client) call(ctx context.Context, op, apiPath string, reqBody, out any) error {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("openlist: %s: %w", op, err)
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			slog.Debug("openlist: retrying api call", "op", op, "attempt", attempt, "err", lastErr)
			select {
			case <-ctx.Done():
				return fmt.Errorf("openlist: %s: %w", op, ctx.Err())
			case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+apiPath, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("openlist: %s: %w", op, err)
		}
		req.Header.Set("Content-Type", "application/json")
		if tok := c.currentToken(); tok != "" {
			req.Header.Set("Authorization", tok)
		}
		t0 := time.Now()
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("openlist: %s: %w", op, err)
			continue // transport-level: worth another attempt
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		dur := time.Since(t0)
		if err != nil {
			lastErr = fmt.Errorf("openlist: %s: reading response: %w", op, err)
			continue
		}
		slog.Debug("openlist: api call", "op", op, "status", resp.StatusCode, "dur", dur.Round(time.Millisecond))
		if resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusGatewayTimeout {
			lastErr = fmt.Errorf("openlist: %s: HTTP %d", op, resp.StatusCode)
			continue
		}
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return fmt.Errorf("openlist: %s: unexpected response (HTTP %d): %w", op, resp.StatusCode, err)
		}
		if env.Code == 200 {
			if out == nil || len(env.Data) == 0 {
				return nil
			}
			if err := json.Unmarshal(env.Data, out); err != nil {
				return fmt.Errorf("openlist: %s: decoding response: %w", op, err)
			}
			return nil
		}
		apiErr := &Error{Op: op, Code: env.Code, Message: env.Message}
		if errors.Is(apiErr, ErrAuth) && attempt == 0 && c.canReLogin() {
			// Token likely expired: re-login once and retry the call.
			if lerr := c.Login(ctx); lerr == nil {
				attempt--
				continue
			}
		}
		return apiErr
	}
	return lastErr
}

func (c *Client) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

func (c *Client) canReLogin() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.username != ""
}

func (c *Client) setToken(tok string) {
	c.mu.Lock()
	c.token = tok
	c.mu.Unlock()
}

func (c *Client) markFsGetOnly() {
	c.mu.Lock()
	c.fsGetOnly = true
	c.mu.Unlock()
}

func (c *Client) isFsGetOnly() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fsGetOnly
}

// Login obtains a fresh token using the configured credentials.
func (c *Client) Login(ctx context.Context) error {
	c.mu.Lock()
	user, pass := c.username, c.password
	c.mu.Unlock()
	if user == "" {
		return ErrAuth
	}
	var data struct {
		Token string `json:"token"`
	}
	err := c.post(ctx, "/api/auth/login",
		map[string]string{"username": user, "password": pass}, &data)
	if err != nil {
		return fmt.Errorf("openlist: login: %w", err)
	}
	if data.Token == "" {
		return fmt.Errorf("openlist: login: empty token")
	}
	c.setToken(data.Token)
	return nil
}

// post is call without the retry logic, used internally.
func (c *Client) post(ctx context.Context, apiPath string, reqBody, out any) error {
	// Login must not recurse into re-login handling.
	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+apiPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := c.currentToken(); tok != "" {
		req.Header.Set("Authorization", tok)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	resp.Body.Close()
	if err != nil {
		return err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("unexpected response (HTTP %d): %w", resp.StatusCode, err)
	}
	if env.Code != 200 {
		return &Error{Op: strings.TrimPrefix(apiPath, "/api/"), Code: env.Code, Message: env.Message}
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}

// Probe fetches anonymous server settings; it does not require a token.
func (c *Client) Probe(ctx context.Context) (info ServerInfo, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/public/settings", nil)
	if err != nil {
		return info, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return info, fmt.Errorf("openlist: probe: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return info, fmt.Errorf("openlist: probe: %w", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return info, fmt.Errorf("openlist: probe: unexpected response (HTTP %d): %w", resp.StatusCode, err)
	}
	if env.Code != 200 {
		return info, &Error{Op: "probe", Code: env.Code, Message: env.Message}
	}
	if err := json.Unmarshal(env.Data, &info); err != nil {
		return info, fmt.Errorf("openlist: probe: decoding response: %w", err)
	}
	return info, nil
}

// List returns the entries of one directory.
func (c *Client) List(ctx context.Context, p string) ([]Entry, error) {
	const perPage = 200
	var all []Entry
	for page := 1; ; page++ {
		var data struct {
			Content []Entry `json:"content"`
			Total   int     `json:"total"`
		}
		err := c.call(ctx, "list", "/api/fs/list", map[string]any{
			"path":     p,
			"page":     page,
			"per_page": perPage,
			"refresh":  false,
		}, &data)
		if err != nil {
			return nil, err
		}
		all = append(all, data.Content...)
		if data.Total > 0 {
			if len(all) >= data.Total {
				return all, nil
			}
		} else if len(data.Content) < perPage {
			// Server returned a short page and gave no total: exhausted.
			return all, nil
		}
		if len(data.Content) == 0 || page >= 10000 {
			return all, nil
		}
	}
}

// Stat returns metadata for one path.
func (c *Client) Stat(ctx context.Context, p string) (Entry, error) {
	entry, _, _, err := c.fsGet(ctx, "stat", p)
	return entry, err
}

// fsGet calls /api/fs/get once and returns the entry plus the raw direct
// link fields (empty URL for directories or drivers without a direct link).
func (c *Client) fsGet(ctx context.Context, op, p string) (Entry, string, map[string]string, error) {
	// Flat shadow struct: an embedded Entry would hijack the whole
	// object because Entry implements json.Unmarshaler.
	var data struct {
		entryShadow
		RawURL string          `json:"raw_url"`
		Header json.RawMessage `json:"header"`
	}
	if err := c.call(ctx, op, "/api/fs/get", map[string]any{"path": p}, &data); err != nil {
		return Entry{}, "", nil, err
	}
	return data.entryShadow.toEntry(), data.RawURL, decodeHeader(data.Header), nil
}

// GetWithLink returns the entry and a directly fetchable link in one API
// call. It is what the filesystem hot path uses. link is nil for
// directories or drivers that provide no direct link.
func (c *Client) GetWithLink(ctx context.Context, p string) (Entry, *Link, error) {
	entry, rawURL, header, err := c.fsGet(ctx, "link", p)
	if err != nil {
		return Entry{}, nil, err
	}
	if rawURL == "" {
		return entry, nil, nil
	}
	return entry, &Link{
		URL:        rawURL,
		Header:     header,
		Source:     "fs/get",
		ResolvedAt: time.Now(),
	}, nil
}

// ResolveLink returns a directly fetchable URL for one file.
//
// Current OpenList restricts /api/fs/link to admins; when the server denies
// it we remember that and use the raw_url from /api/fs/get instead.
func (c *Client) ResolveLink(ctx context.Context, p string) (*Link, error) {
	if !c.isFsGetOnly() {
		link, err := c.resolveViaFsLink(ctx, p)
		if err == nil {
			return link, nil
		}
		if !errors.Is(err, ErrPermission) {
			return nil, err
		}
		c.markFsGetOnly()
	}
	_, link, err := c.GetWithLink(ctx, p)
	if err != nil {
		return nil, err
	}
	if link == nil {
		return nil, &Error{Op: "link", Path: p, Message: "no direct link for path (directory or driver without one)"}
	}
	return link, nil
}
func (c *Client) resolveViaFsLink(ctx context.Context, p string) (*Link, error) {
	var data struct {
		URL    string          `json:"url"`
		Header json.RawMessage `json:"header"`
	}
	if err := c.call(ctx, "link", "/api/fs/link", map[string]any{"path": p}, &data); err != nil {
		return nil, err
	}
	if data.URL == "" {
		return nil, &Error{Op: "link", Path: p, Message: "empty url"}
	}
	return &Link{
		URL:        data.URL,
		Header:     decodeHeader(data.Header),
		Source:     "fs/link",
		ResolvedAt: time.Now(),
	}, nil
}

// Mkdir creates a directory.
func (c *Client) Mkdir(ctx context.Context, p string) error {
	return c.call(ctx, "mkdir", "/api/fs/mkdir", map[string]any{"path": p}, nil)
}

// Remove deletes names inside dir (files and/or directories).
func (c *Client) Remove(ctx context.Context, dir string, names []string) error {
	return c.call(ctx, "remove", "/api/fs/remove",
		map[string]any{"dir": dir, "names": names}, nil)
}

// Rename renames/moves one object within its directory.
func (c *Client) Rename(ctx context.Context, p, newName string) error {
	return c.call(ctx, "rename", "/api/fs/rename",
		map[string]any{"path": p, "name": newName}, nil)
}

// Upload streams r (exactly size bytes) to the remote path via PUT
// /api/fs/put. Overwrite is always enabled; the upload is synchronous.
func (c *Client) Upload(ctx context.Context, p string, size int64, r io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.BaseURL+"/api/fs/put", r)
	if err != nil {
		return fmt.Errorf("openlist: upload: %w", err)
	}
	req.ContentLength = size
	req.Header.Set("File-Path", url.PathEscape(p))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("As-Task", "false")
	req.Header.Set("Overwrite", "true")
	if tok := c.currentToken(); tok != "" {
		req.Header.Set("Authorization", tok)
	}
	t0 := time.Now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("openlist: upload: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("openlist: upload: reading response: %w", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("openlist: upload: unexpected response (HTTP %d): %w", resp.StatusCode, err)
	}
	if env.Code != 200 {
		slog.Debug("openlist: upload done", "path", p, "size", size,
			"dur", time.Since(t0).Round(time.Millisecond))
		return &Error{Op: "upload", Path: p, Code: env.Code, Message: env.Message}
	}
	slog.Debug("openlist: upload ok", "path", p, "size", size, "dur", time.Since(t0).Round(time.Millisecond))
	return nil
}

// decodeHeader accepts the "header" field in both shapes seen in the wild:
// an object {"Cookie": "..."} or an empty string.
func decodeHeader(raw json.RawMessage) map[string]string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == `""` || trimmed == "null" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}
