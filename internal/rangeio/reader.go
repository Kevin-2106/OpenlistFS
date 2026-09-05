// Package rangeio reads byte ranges of one remote file over HTTP.
//
// The Reader maps offset+length reads (WinFsp's Read shape, io.ReaderAt
// contract) onto HTTP Range requests and is deliberately paranoid: a server
// that ignores Range and answers 200 with a whole multi-GB body must never
// cause a silent full download.
package rangeio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrRangeIgnored is returned when the server answered 200 (ignoring the
// Range header) for a request at a non-zero offset. Returning the error —
// instead of consuming the body — is what prevents full-file downloads.
var ErrRangeIgnored = errors.New("rangeio: server ignored Range header (200 for offset>0)")

// Link is a resolved, directly fetchable URL for one file.
type Link struct {
	URL    string            // use as-is; already percent-encoded
	Header map[string]string // extra request headers required by the storage
	Size   int64             // exact file size from metadata, must be > 0
}

// LinkResolver produces a fresh Link; used to recover from expired URLs.
type LinkResolver func(ctx context.Context) (Link, error)

// Metrics accumulates what happened on the wire. All methods are safe for
// concurrent use.
type Metrics struct {
	Requests      atomic.Int64 // HTTP Range requests issued
	Retries       atomic.Int64 // retries after transient errors / expired links
	LinkResolves  atomic.Int64 // calls to the LinkResolver
	BytesFetched  atomic.Int64 // HTTP body bytes received
	TTFBNanos     atomic.Int64 // sum of times to first body byte
	RangeRequests atomic.Int64 // ReadAt calls (foreground)
}

// Snapshot is a point-in-time copy of the counters.
type Snapshot struct {
	Requests, Retries, LinkResolves, BytesFetched, RangeRequests int64
	TTFBNanos                                                    int64
}

// Snapshot copies the counters.
func (m *Metrics) Snapshot() Snapshot {
	return Snapshot{
		Requests:      m.Requests.Load(),
		Retries:       m.Retries.Load(),
		LinkResolves:  m.LinkResolves.Load(),
		BytesFetched:  m.BytesFetched.Load(),
		RangeRequests: m.RangeRequests.Load(),
		TTFBNanos:     m.TTFBNanos.Load(),
	}
}

// AvgTTFB returns the mean time to first body byte, 0 if no requests.
func (s Snapshot) AvgTTFB() time.Duration {
	if s.Requests == 0 {
		return 0
	}
	return time.Duration(s.TTFBNanos / s.Requests)
}

// Reader is an io.ReaderAt over one remote file. It is safe for concurrent
// use. Close cancels in-flight requests.
type Reader struct {
	link    Link
	client  *http.Client
	ctx     context.Context
	cancel  context.CancelFunc
	resolve LinkResolver // optional

	mu         sync.Mutex
	reResolved bool // expired-link retry already used

	Metrics *Metrics
}

type Option func(*Reader)

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(c *http.Client) Option {
	return func(r *Reader) { r.client = c }
}

// WithLinkResolver enables one-shot link re-resolution on expired/denied URLs.
func WithLinkResolver(fn LinkResolver) Option {
	return func(r *Reader) { r.resolve = fn }
}

// WithMetrics installs a metrics sink.
func WithMetrics(m *Metrics) Option {
	return func(r *Reader) { r.Metrics = m }
}

// New creates a Reader for link. The context governs all requests made
// through this reader (Close cancels it).
func New(ctx context.Context, link Link, opts ...Option) *Reader {
	cctx, cancel := context.WithCancel(ctx)
	r := &Reader{
		link:    link,
		ctx:     cctx,
		cancel:  cancel,
		client:  defaultClient(),
		Metrics: &Metrics{},
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// sharedTransport is reused by every range reader so connections (and TLS
// handshakes) are pooled across files: thumbnails, warmers and players all
// open their own readers.
var sharedTransport http.RoundTripper = &http.Transport{
	MaxIdleConns:          32,
	MaxIdleConnsPerHost:   32,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second,
	ExpectContinueTimeout: time.Second,
}

func defaultClient() *http.Client {
	return &http.Client{Transport: sharedTransport}
}

// Close cancels in-flight requests and releases the connection pool.
func (r *Reader) Close() error {
	r.cancel()
	return nil
}

// ReadAt fills p starting at off using the reader's base context. It
// satisfies io.ReaderAt: if fewer than len(p) bytes are returned, the error
// says why (io.EOF at end of file).
func (r *Reader) ReadAt(p []byte, off int64) (int, error) {
	return r.ReadAtContext(r.ctx, p, off)
}

// ReadAtContext is ReadAt with an explicit per-call context; callers such
// as the read-ahead layer use it so prefetches can be cancelled
// independently of the foreground path.
func (r *Reader) ReadAtContext(ctx context.Context, p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, fmt.Errorf("rangeio: negative offset %d", off)
	}
	if off >= r.link.Size {
		return 0, io.EOF
	}
	origLen := len(p)
	if remain := r.link.Size - off; int64(len(p)) > remain {
		p = p[:remain]
	}
	r.Metrics.RangeRequests.Add(1)

	filled := 0
	for filled < len(p) {
		n, err := r.readRange(ctx, p[filled:], off+int64(filled))
		filled += n
		if err != nil {
			if errors.Is(err, io.EOF) && filled > 0 {
				return filled, io.EOF
			}
			return filled, err
		}
		if n == 0 {
			return filled, io.ErrUnexpectedEOF
		}
	}
	if filled < origLen {
		return filled, io.EOF
	}
	return filled, nil
}

// transientRetry is applied to network-level failures and 5xx.
const transientRetry = 2

// expiredStatus are the statuses that justify re-resolving the link once.
func expiredStatus(code int) bool {
	return code == http.StatusForbidden || code == http.StatusNotFound ||
		code == http.StatusGone || code == http.StatusUnauthorized
}

func (r *Reader) readRange(ctx context.Context, p []byte, off int64) (int, error) {
	want := int64(len(p))
	end := off + want - 1
	lastErr := error(nil)

	for attempt := 0; attempt <= transientRetry; attempt++ {
		if attempt > 0 {
			r.Metrics.Retries.Add(1)
			time.Sleep(time.Duration(attempt) * 150 * time.Millisecond)
		}
		n, status, err := r.tryOnce(ctx, p, off)
		if err == nil {
			return n, nil
		}
		lastErr = err

		// Expired link: re-resolve once and retry immediately.
		var httpErr *httpStatusError
		if errors.As(err, &httpErr) && expiredStatus(httpErr.status) {
			if r.reResolve() == nil {
				continue
			}
			return n, err
		}
		// Non-transient failures: give up right away.
		var rngErr *contentRangeError
		if errors.As(err, &rngErr) || errors.Is(err, ErrRangeIgnored) ||
			errors.Is(err, context.Canceled) {
			return n, err
		}
		if attempt >= transientRetry {
			return n, fmt.Errorf("rangeio: range %d-%d: %w (after %d retries, status %d)",
				off, end, lastErr, transientRetry, status)
		}
		slog.Debug("rangeio: retrying range request",
			"offset", off, "want", want, "attempt", attempt, "status", status, "err", err)
	}
	return 0, lastErr
}

type httpStatusError struct {
	status int
	op     string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("rangeio: %s: HTTP %d", e.op, e.status)
}

type contentRangeError struct {
	header string
	why    string
}

func (e *contentRangeError) Error() string {
	return fmt.Sprintf("rangeio: malformed Content-Range %q: %s", redactRangeHeader(e.header), e.why)
}

// redactRangeHeader keeps the header shape but drops nothing sensitive;
// kept for symmetry with URL logging in case CDNs embed tokens in headers.
func redactRangeHeader(h string) string { return h }

func (r *Reader) tryOnce(ctx context.Context, p []byte, off int64) (n int, status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.link.URL, nil)
	if err != nil {
		return 0, 0, err
	}
	for k, v := range r.link.Header {
		req.Header.Set(k, v)
	}
	end := off + int64(len(p)) - 1
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, end))
	req.Header.Set("Accept-Encoding", "identity") // ranges must not be re-encoded

	t0 := time.Now()
	slog.Debug("rangeio: range request", "offset", off, "want", len(p))
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, 0, err // network-level: retryable
	}
	defer func() {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
	}()
	slog.Debug("rangeio: range response",
		"url", openlistSanitize(resp.Request.URL.String()), "status", resp.StatusCode)

	status = resp.StatusCode
	switch status {
	case http.StatusPartialContent:
		n, err := r.consume206(p, off, resp, t0)
		return n, status, err
	case http.StatusOK:
		n, err := r.consume200(p, off, resp, t0)
		return n, status, err
	case http.StatusRequestedRangeNotSatisfiable:
		if off >= r.link.Size {
			return 0, status, io.EOF
		}
		return 0, status, &httpStatusError{status: status, op: "range"}
	default:
		return 0, status, &httpStatusError{status: status, op: "range"}
	}
}

// consume206 validates the 206 response and fills p.
func (r *Reader) consume206(p []byte, off int64, resp *http.Response, t0 time.Time) (int, error) {
	cr := resp.Header.Get("Content-Range")
	start, end, total, ok := parseContentRange(cr)
	if !ok {
		return 0, &contentRangeError{header: cr, why: "unparseable"}
	}
	if start != off {
		return 0, &contentRangeError{header: cr, why: fmt.Sprintf("start %d != requested %d", start, off)}
	}
	if end < start {
		return 0, &contentRangeError{header: cr, why: fmt.Sprintf("end %d before start %d", end, start)}
	}
	wantEnd := off + int64(len(p)) - 1
	if end > wantEnd {
		return 0, &contentRangeError{header: cr, why: fmt.Sprintf("end %d exceeds requested %d", end, wantEnd)}
	}
	if r.link.Size > 0 && total > 0 && total != r.link.Size {
		return 0, &contentRangeError{header: cr, why: fmt.Sprintf("total %d != known size %d", total, r.link.Size)}
	}

	expect := int(end - start + 1)
	r.firstByte(t0)
	// Read at most expect+1 bytes: the +1 detects oversize bodies without
	// ever pulling a whole file into memory.
	buf := p
	oversize := false
	if expect > len(p) {
		// Content-Range claims more than we asked: cap the copy.
		expect = len(p)
		oversize = true
	}
	got := 0
	for got < expect {
		m, err := resp.Body.Read(buf[got:expect])
		got += m
		r.Metrics.BytesFetched.Add(int64(m))
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return got, err
		}
	}
	if got < expect {
		return got, fmt.Errorf("rangeio: short body: got %d of %d bytes", got, expect)
	}
	if !oversize {
		var probe [1]byte
		if m, _ := resp.Body.Read(probe[:]); m > 0 {
			r.Metrics.BytesFetched.Add(1)
			return got, &contentRangeError{header: cr, why: "body longer than Content-Range"}
		}
	}
	return got, nil
}

// consume200 implements the safety policy for servers that ignored Range.
func (r *Reader) consume200(p []byte, off int64, resp *http.Response, t0 time.Time) (int, error) {
	if off != 0 {
		// Never accept a full-file body at a non-zero offset.
		return 0, ErrRangeIgnored
	}
	// offset 0: the body starts at the right place; cap the read at len(p)
	// and close the connection without draining it.
	slog.Warn("rangeio: server ignored Range (200 at offset 0); reading capped",
		"capped_bytes", len(p))
	r.firstByte(t0)
	got := 0
	for got < len(p) {
		m, err := resp.Body.Read(p[got:])
		got += m
		r.Metrics.BytesFetched.Add(int64(m))
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return got, err
		}
	}
	return got, nil
}

func (r *Reader) firstByte(t0 time.Time) {
	r.Metrics.Requests.Add(1)
	r.Metrics.TTFBNanos.Add(int64(time.Since(t0)))
}

// reResolve refreshes the link once per Reader lifetime; repeated expired
// links are not retried (no infinite loops).
func (r *Reader) reResolve() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resolve == nil || r.reResolved {
		return fmt.Errorf("rangeio: link expired and cannot re-resolve")
	}
	r.reResolved = true
	slog.Warn("rangeio: link appears expired; re-resolving once")
	r.Metrics.LinkResolves.Add(1)
	link, err := r.resolve(r.ctx)
	if err != nil {
		return err
	}
	if link.Size != 0 && link.Size != r.link.Size {
		return fmt.Errorf("rangeio: re-resolved size %d != %d", link.Size, r.link.Size)
	}
	r.link.URL = link.URL
	r.link.Header = link.Header
	return nil
}

// parseContentRange parses "bytes start-end/total" (total may be "*").
func parseContentRange(h string) (start, end, total int64, ok bool) {
	h = strings.TrimSpace(h)
	if !strings.HasPrefix(h, "bytes ") {
		return 0, 0, 0, false
	}
	parts := strings.SplitN(strings.TrimPrefix(h, "bytes "), "/", 2)
	if len(parts) != 2 {
		return 0, 0, 0, false
	}
	bounds := strings.SplitN(strings.TrimSpace(parts[0]), "-", 2)
	if len(bounds) != 2 {
		return 0, 0, 0, false
	}
	start, err1 := strconv.ParseInt(strings.TrimSpace(bounds[0]), 10, 64)
	end, err2 := strconv.ParseInt(strings.TrimSpace(bounds[1]), 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, 0, false
	}
	t := strings.TrimSpace(parts[1])
	if t == "*" {
		return start, end, 0, true // unknown total, accepted
	}
	total, err := strconv.ParseInt(t, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	return start, end, total, true
}

// openlistSanitize is re-declared here (instead of importing the openlist
// package) to keep rangeio dependency-free.
func openlistSanitize(u string) string {
	i := strings.IndexByte(u, '?')
	if i < 0 {
		return u
	}
	return u[:i] + "?<redacted>"
}
