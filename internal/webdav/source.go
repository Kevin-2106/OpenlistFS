package webdav

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sync"
	"time"

	"openlistfs/internal/filesystem"
	"openlistfs/internal/rangeio"
)

// sourceConfig carries adapter options.
type sourceConfig struct {
	metrics   *rangeio.Metrics
	metaTTL   time.Duration
	transport http.RoundTripper // range readers' shared transport
}

// SourceOption configures the filesystem Source returned by Source.
type SourceOption func(*sourceConfig)

// WithSourceMetrics shares one metrics sink across all readers the source
// creates, giving session-wide statistics.
func WithSourceMetrics(m *rangeio.Metrics) SourceOption {
	return func(cfg *sourceConfig) { cfg.metrics = m }
}

// WithMetadataTTL sets how long PROPFIND results are cached.
func WithMetadataTTL(ttl time.Duration) SourceOption {
	return func(cfg *sourceConfig) { cfg.metaTTL = ttl }
}

// WithSourceTransport makes every range reader the source creates share
// one transport (connection pool) — pass rangeio.NewTransport(proxyFn) to
// route downloads through a proxy.
func WithSourceTransport(rt http.RoundTripper) SourceOption {
	return func(cfg *sourceConfig) { cfg.transport = rt }
}

// cachedDir is a memoized depth-1 PROPFIND.
type cachedDir struct {
	entries []Entry // children only
	at      time.Time
}

// cachedFile memoizes a depth-0 PROPFIND, including misses (the shell
// probes desktop.ini / Thumbs.db constantly).
type cachedFile struct {
	entry    Entry
	at       time.Time
	notFound bool
}

// negativeTTL bounds how long a not-found answer is served from cache.
const negativeTTL = 30 * time.Second

// source adapts the client to filesystem.Source + Writer. Reads are plain
// authenticated GETs with HTTP Range — the file's DAV URL never expires,
// so no link resolver is needed.
type source struct {
	client *Client
	cfg    sourceConfig

	mu    sync.RWMutex
	dirs  map[string]*cachedDir
	files map[string]*cachedFile
}

// Source adapts the client to filesystem.Source.
func (c *Client) Source(opts ...SourceOption) filesystem.Source {
	cfg := sourceConfig{metaTTL: 2 * time.Minute}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &source{
		client: c,
		cfg:    cfg,
		dirs:   make(map[string]*cachedDir),
		files:  make(map[string]*cachedFile),
	}
}

func (s *source) List(ctx context.Context, dir string) ([]filesystem.Entry, error) {
	s.mu.RLock()
	cd, ok := s.dirs[dir]
	fresh := ok && time.Since(cd.at) <= s.cfg.metaTTL
	s.mu.RUnlock()
	if fresh {
		return toFSEntries(cd.entries), nil
	}
	props, err := s.client.Propfind(ctx, dir, 1)
	if err != nil {
		return nil, convertErr(err)
	}
	children := make([]Entry, 0, len(props))
	for i, e := range props {
		if i == 0 && e.Path == path.Clean(dir) {
			continue // the collection itself
		}
		children = append(children, e)
	}
	s.mu.Lock()
	s.dirs[dir] = &cachedDir{entries: children, at: time.Now()}
	s.mu.Unlock()
	return toFSEntries(children), nil
}

func (s *source) Stat(ctx context.Context, p string) (filesystem.Entry, error) {
	// A fresh parent listing already carries name/size/mtime.
	if e, ok := s.statFromListCache(p); ok {
		return e, nil
	}
	cf, ok := s.cachedFile(p)
	if ok {
		if cf.notFound {
			return filesystem.Entry{}, errNotFound(p)
		}
		return toFSEntry(cf.entry), nil
	}
	props, err := s.client.Propfind(ctx, p, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.rememberNotFound(p)
			return filesystem.Entry{}, errNotFound(p)
		}
		return filesystem.Entry{}, convertErr(err)
	}
	entry := props[0]
	if entry.Path != path.Clean(p) {
		// Server answered with a different path (redirect/cleaning);
		// trust the requested one but keep the properties.
		entry.Path = path.Clean(p)
	}
	s.remember(entry)
	return toFSEntry(entry), nil
}

// statFromListCache answers Stat from a cached parent listing.
func (s *source) statFromListCache(p string) (filesystem.Entry, bool) {
	dir, base := path.Split(p)
	dir = path.Clean("/" + dir)
	s.mu.RLock()
	defer s.mu.RUnlock()
	cd, ok := s.dirs[dir]
	if !ok || time.Since(cd.at) > s.cfg.metaTTL {
		return filesystem.Entry{}, false
	}
	for _, e := range cd.entries {
		if path.Base(e.Path) == base {
			return toFSEntry(e), true
		}
	}
	return filesystem.Entry{}, false
}

// Open returns the entry and a range reader over an authenticated GET.
func (s *source) Open(ctx context.Context, p string) (filesystem.Entry, filesystem.RangeReader, error) {
	entry, err := s.Stat(ctx, p)
	if err != nil {
		return filesystem.Entry{}, nil, err
	}
	if entry.IsDir {
		return entry, nil, nil
	}
	var ropts []rangeio.Option
	if s.cfg.transport != nil {
		ropts = append(ropts, rangeio.WithHTTPClient(&http.Client{Transport: s.cfg.transport}))
	}
	reader := rangeio.New(ctx, rangeio.Link{
		URL:    s.client.urlFor(p),
		Header: s.client.AuthHeader(),
		Size:   entry.Size,
	}, ropts...)
	if s.cfg.metrics != nil {
		reader.Metrics = s.cfg.metrics
	}
	return entry, reader, nil
}

func errNotFound(p string) error {
	return fmt.Errorf("webdav: stat %s: %w", p, os.ErrNotExist)
}

func (s *source) cachedFile(p string) (*cachedFile, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cf, ok := s.files[p]
	if !ok {
		return nil, false
	}
	ttl := s.cfg.metaTTL
	if cf.notFound {
		ttl = negativeTTL
	}
	if time.Since(cf.at) > ttl {
		return nil, false
	}
	return cf, true
}

func (s *source) remember(e Entry) {
	s.mu.Lock()
	s.files[e.Path] = &cachedFile{entry: e, at: time.Now()}
	s.mu.Unlock()
}

func (s *source) rememberNotFound(p string) {
	s.mu.Lock()
	s.files[p] = &cachedFile{at: time.Now(), notFound: true}
	s.mu.Unlock()
}

// ---- filesystem.Writer ----

func parentDir(p string) string { return path.Dir(p) }

func (s *source) invalidate(dir string) {
	s.mu.Lock()
	delete(s.dirs, dir)
	s.mu.Unlock()
}

func (s *source) invalidateFile(p string) {
	s.mu.Lock()
	delete(s.files, p)
	s.mu.Unlock()
}

func (s *source) Mkdir(ctx context.Context, p string) error {
	if err := s.client.Mkdir(ctx, p); err != nil {
		return convertErr(err)
	}
	s.invalidate(parentDir(p))
	return nil
}

func (s *source) Remove(ctx context.Context, p string) error {
	if err := s.client.Remove(ctx, p); err != nil {
		return convertErr(err)
	}
	s.invalidate(parentDir(p))
	s.invalidateFile(p)
	return nil
}

func (s *source) Rename(ctx context.Context, p, newName string) error {
	if err := s.client.Move(ctx, p, newName); err != nil {
		return convertErr(err)
	}
	s.invalidate(parentDir(p))
	s.invalidateFile(p)
	return nil
}

func (s *source) Upload(ctx context.Context, p string, size int64, r io.Reader) error {
	if err := s.client.Upload(ctx, p, size, r); err != nil {
		return convertErr(err)
	}
	s.invalidate(parentDir(p))
	s.invalidateFile(p)
	return nil
}

// InvalidateDir drops the cached listing of dir.
func (s *source) InvalidateDir(dir string) { s.invalidate(dir) }

func toFSEntries(list []Entry) []filesystem.Entry {
	out := make([]filesystem.Entry, len(list))
	for i, e := range list {
		out[i] = toFSEntry(e)
	}
	return out
}

func toFSEntry(e Entry) filesystem.Entry {
	name := path.Base(e.Path)
	if e.Path == "/" {
		name = "/"
	}
	return filesystem.Entry{
		Name:    name,
		Size:    e.Size,
		IsDir:   e.IsDir,
		ModTime: e.ModTime,
	}
}

func convertErr(err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%s: %w", err, os.ErrNotExist)
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("%s: %w", err, os.ErrPermission)
	default:
		return err
	}
}
