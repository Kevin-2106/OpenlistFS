// Package s3 talks to one bucket on an S3-compatible object store (AWS S3,
// MinIO, Cloudflare R2, …) — the third backend for the mounter. Metadata
// comes from ListObjectsV2/HEAD, content from presigned GETs + HTTP Range
// (served by rangeio), mutations from PUT/DELETE/COPY. All requests use
// path-style addressing (https://endpoint/bucket/key) and AWS Signature
// Version 4; empty credentials mean anonymous access (public buckets).
package s3

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Entry is one listing result, addressed by object key. Directory entries
// come from CommonPrefixes and keep their trailing slash ("videos/").
type Entry struct {
	Key     string
	Size    int64
	IsDir   bool
	ModTime time.Time
}

// Client talks to one bucket. It is safe for concurrent use.
type Client struct {
	Endpoint string // scheme://host[:port], no path
	Bucket   string
	Region   string

	scheme    string
	host      string // host[:port], signed as-is
	accessKey string
	secretKey string

	HTTP *http.Client
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

// NewTransport mirrors the other backends' tuning (proxy nil = direct).
func NewTransport(proxy func(*http.Request) (*url.URL, error)) *http.Transport {
	return &http.Transport{
		Proxy:                 proxy,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// New creates a client for bucket at endpoint. accessKey/secretKey may be
// empty for anonymous access.
func New(endpoint, bucket, region, accessKey, secretKey string, opts ...Option) *Client {
	if region == "" {
		region = "us-east-1"
	}
	c := &Client{
		Endpoint:  strings.TrimRight(endpoint, "/"),
		Bucket:    bucket,
		Region:    region,
		accessKey: accessKey,
		secretKey: secretKey,
		HTTP:      &http.Client{Transport: NewTransport(nil)},
	}
	if u, err := url.Parse(c.Endpoint); err == nil && u.Host != "" {
		c.scheme, c.host = u.Scheme, u.Host
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// awsHostRe recognizes the AWS endpoint host shapes that embed a region.
var awsHostRe = regexp.MustCompile(`^s3[.-]([a-z0-9-]+)\.amazonaws\.com(\.cn)?$`)

// ParseTarget resolves a server URL into the pieces an S3 mount needs:
// bucket from the first path segment, region from a ?region= parameter,
// the s3.<region>.amazonaws.com host shape, or the us-east-1 default.
// https://minio.example:9000/media            → minio.example:9000, media, us-east-1
// https://s3.eu-west-1.amazonaws.com/media    → s3.eu-west-1.amazonaws.com, media, eu-west-1
func ParseTarget(serverURL string) (endpoint, bucket, region string, err error) {
	s := strings.TrimSpace(serverURL)
	if s == "" {
		return "", "", "", fmt.Errorf("s3: empty server URL")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, perr := url.Parse(s)
	if perr != nil || u.Host == "" {
		return "", "", "", fmt.Errorf("s3: invalid URL %q", serverURL)
	}
	region = u.Query().Get("region")
	if region == "" {
		if m := awsHostRe.FindStringSubmatch(u.Hostname()); m != nil {
			region = m[1]
		} else if u.Hostname() == "s3.amazonaws.com" {
			region = "us-east-1"
		}
	}
	if region == "" {
		region = "us-east-1"
	}
	bucket = strings.Split(strings.Trim(u.Path, "/"), "/")[0]
	if bucket == "" {
		return "", "", "", fmt.Errorf("s3: URL must include the bucket: https://endpoint/bucket (got %q)", serverURL)
	}
	return u.Scheme + "://" + u.Host, bucket, region, nil
}

// objectPath returns the escaped request path for an object key.
func (c *Client) objectPath(key string) string {
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = awsEscape(s, true)
	}
	return c.bucketPath() + "/" + strings.Join(segs, "/")
}

func (c *Client) bucketPath() string { return "/" + awsEscape(c.Bucket, true) }

// PresignGet returns a URL granting GET access to key for ttl (capped at
// 7 days by S3). Each Open mints a fresh one, matching how the OpenList
// backend consumes expiring raw URLs.
func (c *Client) PresignGet(key string, ttl time.Duration) string {
	return c.presignGet(key, ttl, time.Now())
}

// ---- request plumbing ----

// roundtrip sends one signed request. path/pairs are the escaped target
// the signature covers — the wire URL is rendered from the same strings.
func (c *Client) roundtrip(ctx context.Context, method, path string, pairs []queryPair,
	body io.Reader, length int64, payloadHash string, extra func(*http.Request)) (*http.Response, error) {
	target := c.scheme + "://" + c.host + path
	query := renderQuery(pairs)
	if query != "" {
		target += "?" + query
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	if length > 0 {
		req.ContentLength = length
	}
	if extra != nil {
		extra(req)
	}
	c.signRequest(req, path, query, payloadHash, time.Now())
	return c.HTTP.Do(req)
}

// statusErr maps HTTP statuses onto the filesystem's sentinel errors.
func statusErr(op, key string, code int) error {
	switch code {
	case http.StatusNotFound:
		return fmt.Errorf("s3: %s %s: %w", op, key, os.ErrNotExist)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("s3: %s %s: %w", op, key, os.ErrPermission)
	}
	return fmt.Errorf("s3: %s %s: HTTP %d", op, key, code)
}

func drain(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
}

// ---- metadata ----

type listResult struct {
	files     []Entry
	dirs      []Entry
	truncated bool
	nextToken string
}

type xmlList struct {
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	Contents              []struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
		Size         int64  `xml:"Size"`
	} `xml:"Contents"`
	CommonPrefixes []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
}

// listPage fetches one ListObjectsV2 page. Transport failures and
// 502/503/504 are retried — a dropped connection should not make browsing
// flaky.
func (c *Client) listPage(ctx context.Context, prefix, token string, maxKeys int, delimiter bool) (*listResult, error) {
	pairs := []queryPair{{"list-type", "2"}, {"prefix", prefix}, {"encoding-type", "url"}}
	if delimiter {
		pairs = append(pairs, queryPair{"delimiter", "/"})
	}
	if maxKeys > 0 {
		pairs = append(pairs, queryPair{"max-keys", strconv.Itoa(maxKeys)})
	}
	if token != "" {
		pairs = append(pairs, queryPair{"continuation-token", token})
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("s3: list %q: %w", prefix, ctx.Err())
			case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
			}
		}
		resp, err := c.roundtrip(ctx, http.MethodGet, c.bucketPath(), pairs, nil, 0, emptyPayload, nil)
		if err != nil {
			lastErr = fmt.Errorf("s3: list %q: %w", prefix, err)
			continue
		}
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if rerr != nil {
			lastErr = fmt.Errorf("s3: list %q: reading response: %w", prefix, rerr)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			apiErr := statusErr("list", prefix, resp.StatusCode)
			if resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable ||
				resp.StatusCode == http.StatusGatewayTimeout {
				lastErr = apiErr
				continue
			}
			return nil, apiErr
		}
		res, perr := parseList(raw)
		if perr != nil {
			return nil, fmt.Errorf("s3: list %q: %w", prefix, perr)
		}
		return res, nil
	}
	return nil, lastErr
}

func parseList(raw []byte) (*listResult, error) {
	var xl xmlList
	if err := xml.Unmarshal(raw, &xl); err != nil {
		return nil, err
	}
	res := &listResult{truncated: xl.IsTruncated, nextToken: xl.NextContinuationToken}
	for _, o := range xl.Contents {
		key, err := url.QueryUnescape(o.Key)
		if err != nil {
			return nil, fmt.Errorf("decode key %q: %w", o.Key, err)
		}
		res.files = append(res.files, Entry{Key: key, Size: o.Size, ModTime: parseTime(o.LastModified)})
	}
	for _, p := range xl.CommonPrefixes {
		prefix, err := url.QueryUnescape(p.Prefix)
		if err != nil {
			return nil, fmt.Errorf("decode prefix %q: %w", p.Prefix, err)
		}
		res.dirs = append(res.dirs, Entry{Key: prefix, IsDir: true})
	}
	return res, nil
}

// List returns one level under prefix ("", or "dir/"): plain keys as
// files, delimited prefixes as directories, merged in lexicographic order.
func (c *Client) List(ctx context.Context, prefix string) ([]Entry, error) {
	var out []Entry
	token := ""
	for page := 0; page < 10000; page++ {
		res, err := c.listPage(ctx, prefix, token, 0, true)
		if err != nil {
			return nil, err
		}
		out = append(out, res.files...)
		out = append(out, res.dirs...)
		if !res.truncated || res.nextToken == "" {
			sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
			return out, nil
		}
		token = res.nextToken
	}
	return nil, fmt.Errorf("s3: list %q: too many pages", prefix)
}

// PrefixHasKeys reports whether any object key starts with prefix — the
// cheap existence probe for implicit S3 directories (one key, no scan).
func (c *Client) PrefixHasKeys(ctx context.Context, prefix string) (bool, error) {
	res, err := c.listPage(ctx, prefix, "", 1, false)
	if err != nil {
		return false, err
	}
	return len(res.files) > 0, nil
}

// Head returns one object's metadata.
func (c *Client) Head(ctx context.Context, key string) (Entry, error) {
	resp, err := c.roundtrip(ctx, http.MethodHead, c.objectPath(key), nil, nil, 0, emptyPayload, nil)
	if err != nil {
		return Entry{}, fmt.Errorf("s3: head %s: %w", key, err)
	}
	drain(resp) // HEAD carries no body; this just closes
	if resp.StatusCode != http.StatusOK {
		return Entry{}, statusErr("head", key, resp.StatusCode)
	}
	size, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	return Entry{
		Key:     key,
		Size:    size,
		ModTime: parseTime(resp.Header.Get("Last-Modified")),
	}, nil
}

// ---- mutations ----

// Put stores exactly size bytes from r as key (single-shot upload; S3
// caps one PUT at 5 GiB, far beyond the spool files the overlay uploads).
func (c *Client) Put(ctx context.Context, key string, size int64, r io.Reader) error {
	resp, err := c.roundtrip(ctx, http.MethodPut, c.objectPath(key), nil, r, size, unsignedPayload,
		func(req *http.Request) { req.Header.Set("Content-Type", "application/octet-stream") })
	if err != nil {
		return fmt.Errorf("s3: put %s: %w", key, err)
	}
	drain(resp)
	if resp.StatusCode != http.StatusOK {
		return statusErr("put", key, resp.StatusCode)
	}
	return nil
}

// Delete removes key. S3 answers 204 for missing keys too, so this is
// idempotent.
func (c *Client) Delete(ctx context.Context, key string) error {
	resp, err := c.roundtrip(ctx, http.MethodDelete, c.objectPath(key), nil, nil, 0, emptyPayload, nil)
	if err != nil {
		return fmt.Errorf("s3: delete %s: %w", key, err)
	}
	drain(resp)
	if resp.StatusCode/100 != 2 {
		return statusErr("delete", key, resp.StatusCode)
	}
	return nil
}

// Copy server-side copies srcKey to dstKey.
func (c *Client) Copy(ctx context.Context, srcKey, dstKey string) error {
	resp, err := c.roundtrip(ctx, http.MethodPut, c.objectPath(dstKey), nil, nil, 0, emptyPayload,
		func(req *http.Request) {
			req.Header.Set("x-amz-copy-source", c.objectPath(srcKey))
		})
	if err != nil {
		return fmt.Errorf("s3: copy %s → %s: %w", srcKey, dstKey, err)
	}
	drain(resp)
	if resp.StatusCode != http.StatusOK {
		return statusErr("copy", dstKey, resp.StatusCode)
	}
	return nil
}

// parseTime accepts the date shapes S3 returns: RFC 3339 in listings,
// RFC 1123 in Last-Modified headers.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, time.RFC1123, time.RFC1123Z} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
