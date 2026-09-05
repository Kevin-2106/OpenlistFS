package openlist

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"openlistfs/internal/filesystem"
	"openlistfs/internal/rangeio"
)

// sourceConfig carries adapter options.
type sourceConfig struct {
	metrics *rangeio.Metrics
	metaTTL time.Duration
}

// SourceOption configures the filesystem Source returned by Source.
type SourceOption func(*sourceConfig)

// WithSourceMetrics shares one metrics sink across all readers the source
// creates, giving session-wide statistics.
func WithSourceMetrics(m *rangeio.Metrics) SourceOption {
	return func(cfg *sourceConfig) { cfg.metrics = m }
}

// WithMetadataTTL sets how long List/Stat/link results are cached. Browsing
// a mount costs several API round trips per directory otherwise; the TTL
// trades freshness for responsiveness. Expired direct links remain safe:
// the range reader re-resolves them on 403/410.
func WithMetadataTTL(ttl time.Duration) SourceOption {
	return func(cfg *sourceConfig) { cfg.metaTTL = ttl }
}

// cachedFile is a memoized fs/get result.
type cachedFile struct {
	entry Entry
	url   string
	head  map[string]string
	at    time.Time
}

// cachedDir is a memoized fs/list result.
type cachedDir struct {
	entries []Entry
	at      time.Time
}

// source adapts the client to filesystem.Source with a TTL metadata cache.
// The filesystem layer sees entries and opaque readers only — never URLs
// or API endpoints.
type source struct {
	client *Client
	cfg    sourceConfig

	mu    sync.RWMutex
	dirs  map[string]*cachedDir  // canonical dir -> listing
	files map[string]*cachedFile // canonical path -> fs/get result
}

// Source adapts the client to filesystem.Source.
func (c *Client) Source(opts ...SourceOption) filesystem.Source {
	cfg := sourceConfig{metaTTL: 30 * time.Second}
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
	list, err := s.client.List(ctx, dir)
	if err != nil {
		return nil, convertErr(err)
	}
	s.mu.Lock()
	s.dirs[dir] = &cachedDir{entries: list, at: time.Now()}
	s.mu.Unlock()
	return toFSEntries(list), nil
}

func (s *source) Stat(ctx context.Context, p string) (filesystem.Entry, error) {
	cf, ok := s.cachedFile(p)
	if ok {
		return toFSEntry(cf.entry), nil
	}
	entry, rawURL, header, err := s.client.fsGet(ctx, "stat", p)
	if err != nil {
		return filesystem.Entry{}, convertErr(err)
	}
	s.remember(p, entry, rawURL, header)
	return toFSEntry(entry), nil
}

// Open resolves entry + direct link (one fs/get when cold) and returns a
// range reader wired for expired-link recovery.
func (s *source) Open(ctx context.Context, p string) (filesystem.Entry, filesystem.RangeReader, error) {
	cf, ok := s.cachedFile(p)
	if !ok {
		entry, rawURL, header, err := s.client.fsGet(ctx, "link", p)
		if err != nil {
			return filesystem.Entry{}, nil, convertErr(err)
		}
		cf = s.remember(p, entry, rawURL, header)
	}
	if cf.entry.IsDir || cf.url == "" {
		return toFSEntry(cf.entry), nil, nil
	}
	reader := rangeio.New(ctx, rangeio.Link{
		URL:    cf.url,
		Header: cf.head,
		Size:   cf.entry.Size,
	}, rangeio.WithLinkResolver(func(ctx context.Context) (rangeio.Link, error) {
		// Expired link: bypass the cache, refresh it on success.
		fresh, rawURL, header, err := s.client.fsGet(ctx, "link", p)
		if err != nil {
			return rangeio.Link{}, err
		}
		if rawURL == "" {
			return rangeio.Link{}, fmt.Errorf("openlist: re-resolved link vanished for %s", p)
		}
		s.remember(p, fresh, rawURL, header)
		return rangeio.Link{URL: rawURL, Header: header, Size: fresh.Size}, nil
	}))
	if s.cfg.metrics != nil {
		// Replace the reader's private sink with the shared one.
		reader.Metrics = s.cfg.metrics
	}
	return toFSEntry(cf.entry), reader, nil
}

// cachedFile returns the memoized fs/get result if present and fresh.
func (s *source) cachedFile(p string) (*cachedFile, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cf, ok := s.files[p]
	if !ok || time.Since(cf.at) > s.cfg.metaTTL {
		return nil, false
	}
	return cf, true
}

func (s *source) remember(p string, entry Entry, rawURL string, header map[string]string) *cachedFile {
	cf := &cachedFile{entry: entry, url: rawURL, head: header, at: time.Now()}
	s.mu.Lock()
	s.files[p] = cf
	s.mu.Unlock()
	return cf
}

func toFSEntries(list []Entry) []filesystem.Entry {
	out := make([]filesystem.Entry, len(list))
	for i, e := range list {
		out[i] = toFSEntry(e)
	}
	return out
}

func toFSEntry(e Entry) filesystem.Entry {
	return filesystem.Entry{
		Name:    e.Name,
		Size:    e.Size,
		IsDir:   e.IsDir,
		ModTime: time.Unix(e.Modified, 0),
	}
}

func convertErr(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return fmt.Errorf("%s: %w", err, os.ErrNotExist)
	case errors.Is(err, ErrPermission):
		return fmt.Errorf("%s: %w", err, os.ErrPermission)
	default:
		return err
	}
}
