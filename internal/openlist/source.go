package openlist

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"openlistfs/internal/filesystem"
	"openlistfs/internal/rangeio"
)

// sourceOption configures the filesystem adapter.
type sourceConfig struct {
	metrics *rangeio.Metrics
}

// SourceOption configures the filesystem Source returned by Source.
type SourceOption func(*sourceConfig)

// WithSourceMetrics shares one metrics sink across all readers the source
// creates, giving session-wide statistics.
func WithSourceMetrics(m *rangeio.Metrics) SourceOption {
	return func(cfg *sourceConfig) { cfg.metrics = m }
}

// Source adapts the client to filesystem.Source. The filesystem layer sees
// entries and opaque readers only — never URLs or API endpoints.
func (c *Client) Source(opts ...SourceOption) filesystem.Source {
	var cfg sourceConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	return &source{client: c, cfg: cfg}
}

type source struct {
	client *Client
	cfg    sourceConfig
}

func (s *source) List(ctx context.Context, dir string) ([]filesystem.Entry, error) {
	list, err := s.client.List(ctx, dir)
	if err != nil {
		return nil, convertErr(err)
	}
	out := make([]filesystem.Entry, len(list))
	for i, e := range list {
		out[i] = toFSEntry(e)
	}
	return out, nil
}

func (s *source) Stat(ctx context.Context, p string) (filesystem.Entry, error) {
	entry, err := s.client.Stat(ctx, p)
	if err != nil {
		return filesystem.Entry{}, convertErr(err)
	}
	return toFSEntry(entry), nil
}

// Open resolves entry + direct link in one fs/get round trip and returns a
// range reader wired for expired-link recovery.
func (s *source) Open(ctx context.Context, p string) (filesystem.Entry, filesystem.RangeReader, error) {
	entry, link, err := s.client.GetWithLink(ctx, p)
	if err != nil {
		return filesystem.Entry{}, nil, convertErr(err)
	}
	if entry.IsDir || link == nil {
		return toFSEntry(entry), nil, nil
	}
	reader := rangeio.New(ctx, rangeio.Link{
		URL:    link.URL,
		Header: link.Header,
		Size:   entry.Size,
	}, rangeio.WithLinkResolver(func(ctx context.Context) (rangeio.Link, error) {
		fresh, freshLink, err := s.client.GetWithLink(ctx, p)
		if err != nil {
			return rangeio.Link{}, err
		}
		if freshLink == nil {
			return rangeio.Link{}, fmt.Errorf("openlist: re-resolved link vanished for %s", p)
		}
		return rangeio.Link{URL: freshLink.URL, Header: freshLink.Header, Size: fresh.Size}, nil
	}))
	if s.cfg.metrics != nil {
		// Replace the reader's private sink with the shared one.
		reader.Metrics = s.cfg.metrics
	}
	return toFSEntry(entry), reader, nil
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
