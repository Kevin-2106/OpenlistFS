package readahead

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
)

// Inner is the range reader being decorated. ReadAtContext lets prefetch
// fetches be cancelled per stream without touching the foreground path.
type Inner interface {
	io.ReaderAt
	io.Closer
	ReadAtContext(ctx context.Context, p []byte, off int64) (int, error)
}

// Config holds the tunables; defaults come from NewReader.
type Config struct {
	BlockSize   int64 // bytes per cached block
	Window      int64 // read-ahead distance beyond the stream cursor
	Concurrency int   // parallel block fetches within one stream
}

// Reader decorates one open file with a seek-aware prefetch stream over a
// shared Cache.
//
// Serving rules:
//   - A read fully covered by loaded blocks never touches the network and
//     never disturbs the prefetch stream (this is what makes alternating
//     access patterns — MKV clusters ↔ Cues — cheap after first touch).
//   - A miss loads the *whole containing block* synchronously (shared with
//     any in-flight prefetch of the same block) and re-anchors the stream
//     if the miss landed far from it. The foreground is served by its own
//     load, never queued behind a prefetch.
type Reader struct {
	inner Inner
	cache *Cache
	file  string
	size  int64
	cfg   Config
	base  context.Context

	mu      sync.Mutex
	cursor  int64              // next byte the active stream will ensure
	cancel  context.CancelFunc // cancels the active stream
	notify  chan struct{}      // signalled (non-blocking) when cursor advances
	started bool
}

// NewReader decorates inner. base governs the Reader's lifetime (mount
// context); individual prefetch streams derive from it and are cancelled on
// seek.
func NewReader(base context.Context, file string, size int64, inner Inner, cache *Cache, cfg Config) *Reader {
	if cfg.BlockSize <= 0 {
		cfg.BlockSize = 4 << 20
	}
	if cfg.Window <= 0 {
		cfg.Window = 64 << 20
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 3
	}
	return &Reader{
		inner:  inner,
		cache:  cache,
		file:   file,
		size:   size,
		cfg:    cfg,
		base:   base,
		notify: make(chan struct{}, 1),
	}
}

// ReadAt serves p from loaded blocks; anything missing is served by a
// synchronous whole-block load (cached for later).
func (r *Reader) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, errors.New("readahead: negative offset")
	}
	if off >= r.size {
		return 0, io.EOF
	}

	filled := 0
	for filled < len(p) {
		pos := off + int64(filled)
		bi := pos / r.cfg.BlockSize

		if data, ok := r.cache.Get(r.file, bi); ok && int64(len(data)) > pos-bi*r.cfg.BlockSize {
			filled += copy(p[filled:], data[pos-bi*r.cfg.BlockSize:])
			continue
		}

		// Miss: load the containing block synchronously and re-anchor the
		// prefetch stream at this position.
		slog.Debug("readahead: miss", "file", r.file, "off", pos, "block", bi)
		r.noteMiss(pos)
		data, err := r.cache.Load(r.file, bi, false, func() ([]byte, error) {
			return r.loadBlock(r.base, bi)
		})
		if errors.Is(err, context.Canceled) {
			// A cancelled prefetch stream owned the load we joined; retry
			// once under the mount context.
			data, err = r.cache.Load(r.file, bi, false, func() ([]byte, error) {
				return r.loadBlock(r.base, bi)
			})
		}
		if err != nil {
			return filled, err
		}
		n := copy(p[filled:], data[pos-bi*r.cfg.BlockSize:])
		if n == 0 {
			break // block empty: at EOF
		}
		r.cache.Metrics.MissBytes.Add(int64(n))
		filled += n
	}

	if filled < len(p) {
		return filled, io.EOF
	}
	return filled, nil
}

// noteMiss re-anchors the prefetch stream when a miss lands far from it.
// Misses near the cursor (sequential playback, small interleaves) just
// advance the cursor.
func (r *Reader) noteMiss(off int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	sequential := r.started && r.cancel != nil &&
		off >= r.cursor-r.cfg.BlockSize && off <= r.cursor+r.cfg.BlockSize

	if off > r.cursor {
		r.cursor = off
		select {
		case r.notify <- struct{}{}:
		default:
		}
	}
	if sequential {
		return
	}

	if r.cancel != nil {
		r.cache.Metrics.SeekCancels.Add(1)
		slog.Debug("readahead: re-anchoring prefetch stream",
			"file", r.file, "cursor", r.cursor, "miss_at", off)
		r.cancel()
		r.cancel = nil
	}

	streamCtx, cancel := context.WithCancel(r.base)
	r.cancel = cancel
	r.started = true
	go r.runPrefetch(streamCtx, r.cursor)
}

// runPrefetch keeps the window [cursor, cursor+Window) filled for the life
// of the stream: blocks are fetched in order, at most Concurrency at a
// time; when caught up it waits for the cursor to advance. It only exits
// on cancellation (seek / close) or end of file.
func (r *Reader) runPrefetch(ctx context.Context, from int64) {
	sem := make(chan struct{}, r.cfg.Concurrency)
	bi := from / r.cfg.BlockSize
	for {
		if ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		windowEnd := r.cursor + r.cfg.Window
		r.mu.Unlock()
		if windowEnd > r.size {
			windowEnd = r.size
		}
		blockStart := bi * r.cfg.BlockSize
		if blockStart >= windowEnd {
			if windowEnd >= r.size {
				return // whole file ahead is covered
			}
			// Caught up: wait for the cursor to advance.
			select {
			case <-ctx.Done():
				return
			case <-r.notify:
			}
			continue
		}
		if !r.cache.Has(r.file, bi) {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			go func(bi int64) {
				defer func() { <-sem }()
				_, err := r.cache.Load(r.file, bi, true, func() ([]byte, error) {
					return r.loadBlock(ctx, bi)
				})
				if err != nil && ctx.Err() == nil {
					slog.Debug("readahead: prefetch block failed", "file", r.file,
						"block", bi, "err", err)
				}
			}(bi)
		}
		bi++
	}
}

func (r *Reader) loadBlock(ctx context.Context, bi int64) ([]byte, error) {
	start := bi * r.cfg.BlockSize
	end := start + r.cfg.BlockSize
	if start >= r.size {
		return nil, io.EOF
	}
	if end > r.size {
		end = r.size
	}
	buf := make([]byte, end-start)
	if _, err := r.inner.ReadAtContext(ctx, buf, start); err != nil {
		return nil, err
	}
	return buf, nil
}

// Close cancels the prefetch stream and the inner reader.
func (r *Reader) Close() error {
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	r.mu.Unlock()
	return r.inner.Close()
}
