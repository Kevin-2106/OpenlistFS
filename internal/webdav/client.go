// Package webdav talks to a WebDAV server (any RFC 4918 server; developed
// against OpenList's /dav endpoint). It is the alternative backend for the
// mounter: PROPFIND for metadata, plain GET + HTTP Range for content
// (served by rangeio), MKCOL/DELETE/MOVE/PUT for mutations.
package webdav

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// Entry is one PROPFIND result, addressed by canonical slash path.
type Entry struct {
	Path    string // canonical slash path, "/" = root
	Size    int64  // bytes; 0 for directories
	IsDir   bool
	ModTime time.Time
}

// Client talks to one WebDAV server. It is safe for concurrent use.
type Client struct {
	BaseURL string // no trailing slash, e.g. https://host/dav
	HTTP    *http.Client
	auth    string // precomputed Authorization header; "" = anonymous
}

type Option func(*Client)

// WithTransport overrides the HTTP transport (pass NewTransport(proxyFn)
// to route through a proxy).
func WithTransport(rt http.RoundTripper) Option {
	return func(c *Client) { c.HTTP.Transport = rt }
}

// WithHTTPClient overrides the HTTP client (tests inject their own).
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.HTTP = hc }
}

// NewTransport returns the tuned API transport; proxy (nil = direct)
// routes every request through the given proxy.
func NewTransport(proxy func(*http.Request) (*url.URL, error)) *http.Transport {
	return &http.Transport{
		Proxy:                 proxy,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// New creates a client for the DAV collection at baseURL. username/
// password (either may be empty) are sent as HTTP Basic credentials — for
// OpenList that is the account's WebDAV login.
func New(baseURL, username, password string, opts ...Option) *Client {
	c := &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP: &http.Client{
			Timeout:   60 * time.Second,
			Transport: NewTransport(nil),
		},
	}
	if username != "" || password != "" {
		c.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// AuthHeader returns the headers every content GET must carry.
func (c *Client) AuthHeader() map[string]string {
	if c.auth == "" {
		return nil
	}
	return map[string]string{"Authorization": c.auth}
}

func (c *Client) authz(req *http.Request) {
	if c.auth != "" {
		req.Header.Set("Authorization", c.auth)
	}
}

// urlFor builds the request URL for a canonical slash path. Segments are
// raw (decoded) names; URL escaping happens exactly once here.
func (c *Client) urlFor(p string) string {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return c.BaseURL + p
	}
	return u.JoinPath(strings.Split(strings.Trim(p, "/"), "/")...).String()
}

// basePath is the URL path of the DAV root ("/dav" for OpenList).
func (c *Client) basePath() string {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return ""
	}
	return u.Path
}

// pathOf maps a response href (server-absolute, percent-encoded, possibly
// a full URL) back to a canonical slash path.
func (c *Client) pathOf(href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return "/"
	}
	p := u.Path
	if base := c.basePath(); base != "" && base != "/" && strings.HasPrefix(p, base) {
		p = strings.TrimPrefix(p, base)
	}
	if p == "" {
		p = "/"
	}
	return path.Clean(p)
}

// statusErr maps HTTP statuses onto the filesystem's sentinel errors.
func statusErr(op, p string, code int) error {
	switch code {
	case http.StatusNotFound:
		return fmt.Errorf("webdav: %s %s: %w", op, p, os.ErrNotExist)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("webdav: %s %s: %w", op, p, os.ErrPermission)
	}
	return fmt.Errorf("webdav: %s %s: HTTP %d", op, p, code)
}

// Propfind returns properties for path itself (depth 0) or itself plus
// its children (depth 1); the path's own entry is always first. Transport
// failures and 502/503/504 are retried — a dropped connection should not
// make browsing flaky.
func (c *Client) Propfind(ctx context.Context, p string, depth int) ([]Entry, error) {
	const body = `<?xml version="1.0" encoding="utf-8"?>` +
		`<d:propfind xmlns:d="DAV:">` +
		`<d:prop><d:resourcetype/><d:getcontentlength/><d:getlastmodified/></d:prop>` +
		`</d:propfind>`
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("webdav: propfind %s: %w", p, ctx.Err())
			case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, "PROPFIND", c.urlFor(p), strings.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("webdav: propfind %s: %w", p, err)
		}
		req.Header.Set("Depth", strconv.Itoa(depth))
		req.Header.Set("Content-Type", "application/xml")
		c.authz(req)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("webdav: propfind %s: %w", p, err)
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("webdav: propfind %s: reading response: %w", p, err)
			continue
		}
		if resp.StatusCode == http.StatusMultiStatus {
			entries, err := parseMultistatus(raw, c.pathOf)
			if err != nil {
				return nil, fmt.Errorf("webdav: propfind %s: %w", p, err)
			}
			return entries, nil
		}
		apiErr := statusErr("propfind", p, resp.StatusCode)
		if resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusGatewayTimeout {
			lastErr = apiErr
			continue
		}
		return nil, apiErr
	}
	return nil, lastErr
}

// Mkdir creates a directory (MKCOL).
func (c *Client) Mkdir(ctx context.Context, p string) error {
	return c.mutate(ctx, "mkdir", "MKCOL", p, nil, nil)
}

// Remove deletes one object (DELETE).
func (c *Client) Remove(ctx context.Context, p string) error {
	return c.mutate(ctx, "remove", "DELETE", p, nil, nil)
}

// Move renames p to newName within the same directory (MOVE).
func (c *Client) Move(ctx context.Context, p, newName string) error {
	dest := c.urlFor(path.Join(path.Dir(p), newName))
	return c.mutate(ctx, "rename", "MOVE", p, nil, func(req *http.Request) {
		req.Header.Set("Destination", dest)
		req.Header.Set("Overwrite", "T")
	})
}

// Upload streams r (exactly size bytes) to p (PUT). Unlike the OpenList
// native API this is a true streaming upload.
func (c *Client) Upload(ctx context.Context, p string, size int64, r io.Reader) error {
	return c.mutate(ctx, "upload", "PUT", p, io.NopCloser(io.LimitReader(r, size)), func(req *http.Request) {
		req.ContentLength = size
		req.Header.Set("Content-Type", "application/octet-stream")
	})
}

// mutate performs a mutation; extra may set headers after the standard
// ones. No retries — mutations must stay single-shot.
func (c *Client) mutate(ctx context.Context, op, method, p string, body io.ReadCloser, extra func(*http.Request)) error {
	req, err := http.NewRequestWithContext(ctx, method, c.urlFor(p), body)
	if err != nil {
		return fmt.Errorf("webdav: %s %s: %w", op, p, err)
	}
	c.authz(req)
	if extra != nil {
		extra(req)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("webdav: %s %s: %w", op, p, err)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		return nil
	}
	return statusErr(op, p, resp.StatusCode)
}

// ---- multistatus parsing ----
//
// Servers pick arbitrary namespace prefixes for the DAV: namespace, so
// elements are matched by local name only.

type rawProp struct {
	href    string
	isDir   bool
	size    int64
	hasSize bool
	modRaw  string
}

func parseMultistatus(raw []byte, pathOf func(string) string) ([]Entry, error) {
	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	var entries []Entry
	var cur *rawProp
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "response":
				cur = &rawProp{}
			case "href":
				if cur != nil {
					var s string
					if err := dec.DecodeElement(&s, &t); err == nil {
						cur.href = s
					}
				}
			case "collection":
				if cur != nil {
					cur.isDir = true
				}
			case "getcontentlength":
				if cur != nil {
					var s string
					if err := dec.DecodeElement(&s, &t); err == nil {
						cur.hasSize = true
						cur.size, _ = strconv.ParseInt(strings.TrimSpace(s), 10, 64)
					}
				}
			case "getlastmodified":
				if cur != nil {
					var s string
					if err := dec.DecodeElement(&s, &t); err == nil {
						cur.modRaw = s
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local == "response" && cur != nil {
				entries = append(entries, Entry{
					Path:    pathOf(cur.href),
					Size:    cur.size,
					IsDir:   cur.isDir,
					ModTime: parseTime(cur.modRaw),
				})
				cur = nil
			}
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("empty multistatus")
	}
	return entries, nil
}

// parseTime accepts the date shapes seen across DAV servers: RFC 1123
// (the RFC 4918 format), RFC 3339 and its fractional-second variant.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC1123,
		time.RFC1123Z,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z07:00",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
