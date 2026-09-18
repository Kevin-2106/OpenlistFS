package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
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

// WithMetadataTTL sets how long listings and file metadata are cached.
func WithMetadataTTL(ttl time.Duration) SourceOption {
	return func(cfg *sourceConfig) { cfg.metaTTL = ttl }
}

// WithSourceTransport makes every range reader the source creates share
// one transport (connection pool) — pass rangeio.NewTransport(proxyFn) to
// route downloads through a proxy.
func WithSourceTransport(rt http.RoundTripper) SourceOption {
	return func(cfg *sourceConfig) { cfg.transport = rt }
}

// presignTTL is how long an Open-minted GET URL stays valid; S3 caps
// presigning at 7 days.
const presignTTL = 7 * 24 * time.Hour

// negativeTTL bounds how long a not-found answer is served from cache.
const negativeTTL = 30 * time.Second

// cachedDir is a memoized one-level listing (children only).
type cachedDir struct {
	entries []filesystem.Entry
	at      time.Time
}

// cachedFile memoizes one HEAD, including misses (the shell probes
// desktop.ini / Thumbs.db constantly).
type cachedFile struct {
	entry    filesystem.Entry
	at       time.Time
	notFound bool
}

// source adapts the client to filesystem.Source + Writer. Reads are
// presigned GETs + HTTP Range, so no credentials ride on the content
// requests and each URL outlives any reasonable open.
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

// keyOf maps a canonical slash path to an object key ("/" = "").
func keyOf(p string) string { return path.Clean(p)[1:] }

// prefixOf maps a canonical slash path to its list prefix ("/" = "").
func prefixOf(p string) string {
	k := keyOf(p)
	if k == "" {
		return ""
	}
	return k + "/"
}

func (s *source) List(ctx context.Context, dir string) ([]filesystem.Entry, error) {
	s.mu.RLock()
	cd, ok := s.dirs[dir]
	fresh := ok && time.Since(cd.at) <= s.cfg.metaTTL
	s.mu.RUnlock()
	if fresh {
		return append([]filesystem.Entry(nil), cd.entries...), nil
	}
	prefix := prefixOf(dir)
	entries, err := s.client.List(ctx, prefix)
	if err != nil {
		return nil, convertErr(err)
	}
	children := make([]filesystem.Entry, 0, len(entries))
	for _, e := range entries {
		// The listing prefix itself can come back as a key (the zero-byte
		// directory marker PUT by Mkdir); it is not a child.
		if e.Key == prefix || (!e.IsDir && e.Key+"/" == prefix) {
			continue
		}
		children = append(children, toFSEntry(e, prefix))
	}
	s.mu.Lock()
	s.dirs[dir] = &cachedDir{entries: children, at: time.Now()}
	s.mu.Unlock()
	return append([]filesystem.Entry(nil), children...), nil
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
		return cf.entry, nil
	}
	key := keyOf(p)
	if head, err := s.client.Head(ctx, key); err == nil {
		e := filesystem.Entry{Name: path.Base(p), Size: head.Size, ModTime: head.ModTime}
		s.remember(p, e)
		return e, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return filesystem.Entry{}, convertErr(err)
	}
	// Not a plain object — an implicit directory exists when any key
	// carries its prefix (or a Mkdir marker does).
	if exists, err := s.client.PrefixHasKeys(ctx, prefixOf(p)); err != nil {
		return filesystem.Entry{}, convertErr(err)
	} else if exists {
		e := filesystem.Entry{Name: path.Base(p), IsDir: true}
		s.remember(p, e)
		return e, nil
	}
	s.rememberNotFound(p)
	return filesystem.Entry{}, errNotFound(p)
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
		if e.Name == base {
			return e, true
		}
	}
	return filesystem.Entry{}, false
}

// Open returns the entry and a range reader over a presigned GET.
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
		URL:  s.client.PresignGet(keyOf(p), presignTTL),
		Size: entry.Size,
	}, ropts...)
	if s.cfg.metrics != nil {
		reader.Metrics = s.cfg.metrics
	}
	return entry, reader, nil
}

func errNotFound(p string) error {
	return fmt.Errorf("s3: stat %s: %w", p, os.ErrNotExist)
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

func (s *source) remember(p string, e filesystem.Entry) {
	s.mu.Lock()
	s.files[p] = &cachedFile{entry: e, at: time.Now()}
	s.mu.Unlock()
}

func (s *source) rememberNotFound(p string) {
	s.mu.Lock()
	s.files[p] = &cachedFile{at: time.Now(), notFound: true}
	s.mu.Unlock()
}

// ---- filesystem.Writer ----

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

// Mkdir creates the zero-byte "key/" directory marker.
func (s *source) Mkdir(ctx context.Context, p string) error {
	if err := s.client.Put(ctx, keyOf(p)+"/", 0, nil); err != nil {
		return convertErr(err)
	}
	s.invalidate(path.Dir(p))
	return nil
}

// Remove deletes the object (or the directory marker; S3 has no recursive
// delete, so a non-empty directory keeps its children).
func (s *source) Remove(ctx context.Context, p string) error {
	key := keyOf(p)
	if e, err := s.Stat(ctx, p); err == nil && e.IsDir {
		key += "/"
	}
	if err := s.client.Delete(ctx, key); err != nil {
		return convertErr(err)
	}
	s.invalidate(path.Dir(p))
	s.invalidateFile(p)
	return nil
}

// Rename is a server-side copy plus delete — atomic enough for files.
// Renaming a directory would mean rewriting every key under it, so it is
// rejected instead of silently half-working.
func (s *source) Rename(ctx context.Context, p, newName string) error {
	e, err := s.Stat(ctx, p)
	if err != nil {
		return err
	}
	if e.IsDir {
		return fmt.Errorf("s3: rename %s: renaming directories is not supported on S3", p)
	}
	dst := path.Join(path.Dir(p), newName)
	if err := s.client.Copy(ctx, keyOf(p), keyOf(dst)); err != nil {
		return convertErr(err)
	}
	if err := s.client.Delete(ctx, keyOf(p)); err != nil {
		return convertErr(err)
	}
	s.invalidate(path.Dir(p))
	s.invalidateFile(p)
	s.invalidateFile(dst)
	return nil
}

// Upload stores exactly size bytes from r as a single PUT.
func (s *source) Upload(ctx context.Context, p string, size int64, r io.Reader) error {
	if err := s.client.Put(ctx, keyOf(p), size, r); err != nil {
		return convertErr(err)
	}
	s.invalidate(path.Dir(p))
	s.invalidateFile(p)
	return nil
}

// InvalidateDir drops the cached listing of dir.
func (s *source) InvalidateDir(dir string) { s.invalidate(dir) }

// toFSEntry derives the child's display name by stripping the listing
// prefix (and the trailing slash of a CommonPrefix).
func toFSEntry(e Entry, prefix string) filesystem.Entry {
	name := strings.TrimPrefix(e.Key, prefix)
	if e.IsDir {
		name = strings.TrimSuffix(name, "/")
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
