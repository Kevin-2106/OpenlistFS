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
	BlockSize    int64 // bytes per cached block (prefetch granularity)
	SubBlockSize int64 // foreground-miss granularity; 0 disables sub-blocks
	Window       int64 // read-ahead distance beyond the stream cursor
	Concurrency  int   // parallel block fetches within one stream
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
//
// stream is one prefetch front. Real files routinely have two hot regions
// (MKV: clusters at the read position, Cues near the end), so the Reader
// keeps up to two streams alive instead of cancelling on every alternation.
type stream struct {
	anchor int64 // where the stream was anchored
	cursor int64 // next byte this stream will ensure
	cancel context.CancelFunc
	notify chan struct{}
}

type Reader struct {
	inner Inner
	cache *Cache
	file  string
	size  int64
	sub   int64 // sub-block size (0 = disabled)
	cfg   Config
	base  context.Context

	mu      sync.Mutex
	streams [2]*stream // ring; nil slots are free
	next    int        // next slot to replace
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
	sub := cfg.SubBlockSize
	if sub >= cfg.BlockSize {
		sub = 0 // sub-blocks pointless at or above block granularity
	}
	return &Reader{
		inner: inner,
		cache: cache,
		file:  file,
		size:  size,
		sub:   sub,
		cfg:   cfg,
		base:  base,
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

		// L1: sub-blocks. First touches (shell thumbnails, index probes)
		// pay sub-block granularity instead of a whole block; the prefetch
		// stream keeps filling full blocks underneath.
		if r.sub > 0 {
			si := pos / r.sub
			if data, ok := r.cache.Get(r.subKey(), si); ok && int64(len(data)) > pos-si*r.sub {
				filled += copy(p[filled:], data[pos-si*r.sub:])
				continue
			}
		}

		// L2: full blocks from the prefetch stream.
		bi := pos / r.cfg.BlockSize
		if data, ok := r.cache.Get(r.file, bi); ok && int64(len(data)) > pos-bi*r.cfg.BlockSize {
			filled += copy(p[filled:], data[pos-bi*r.cfg.BlockSize:])
			continue
		}

		// Miss: load the containing sub-block synchronously and re-anchor
		// the prefetch stream at this position.
		slog.Debug("readahead: miss", "file", r.file, "off", pos, "block", bi)
		r.noteMiss(pos)
		loader, key := r.subLoader(pos), r.subKey()
		if r.sub <= 0 {
			loader, key = func() ([]byte, error) { return r.loadBlock(r.base, bi) }, r.file
		}
		data, err := r.cache.Load(key, pos/r.granularity(), false, loader)
		if errors.Is(err, context.Canceled) {
			// A cancelled prefetch stream owned the load we joined; retry
			// once under the mount context.
			data, err = r.cache.Load(key, pos/r.granularity(), false, loader)
		}
		if err != nil {
			return filled, err
		}
		g := r.granularity()
		n := copy(p[filled:], data[pos-(pos/g)*g:])
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

// granularity returns the unit the foreground loads for pos: the
// sub-block size when enabled, the block size otherwise.
func (r *Reader) granularity() int64 {
	if r.sub > 0 {
		return r.sub
	}
	return r.cfg.BlockSize
}

func (r *Reader) subKey() string { return r.file + "\x00sub" }

// subLoader loads the sub-block containing pos.
func (r *Reader) subLoader(pos int64) func() ([]byte, error) {
	return func() ([]byte, error) {
		start := (pos / r.sub) * r.sub
		end := start + r.sub
		if end > r.size {
			end = r.size
		}
		buf := make([]byte, end-start)
		if _, err := r.inner.ReadAtContext(r.base, buf, start); err != nil {
			return nil, err
		}
		return buf, nil
	}
}

// noteMiss feeds the miss position to the prefetch streams. A miss within
// a stream's reach nudges that stream's cursor; a far miss anchors a new
// stream, replacing the oldest one once both slots are taken.
func (r *Reader) noteMiss(off int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, s := range r.streams {
		if s == nil {
			continue
		}
		if off >= s.anchor-r.cfg.BlockSize && off <= s.cursor+r.cfg.BlockSize {
			if off > s.cursor {
				s.cursor = off
				select {
				case s.notify <- struct{}{}:
				default:
				}
			}
			return
		}
	}

	// Far miss: adopt a slot. If both slots hold live streams, replace the
	// one whose anchor is farthest from this miss.
	slot := -1
	for i, s := range r.streams {
		if s == nil {
			slot = i
			break
		}
	}
	if slot < 0 {
		if r.streams[0] == nil || (r.streams[1] != nil &&
			abs64(r.streams[0].anchor-off) >= abs64(r.streams[1].anchor-off)) {
			slot = 0
		} else {
			slot = 1
		}
	}
	if old := r.streams[slot]; old != nil {
		r.cache.Metrics.SeekCancels.Add(1)
		slog.Debug("readahead: re-anchoring prefetch stream",
			"file", r.file, "anchor", old.anchor, "miss_at", off)
		old.cancel()
	}
	streamCtx, cancel := context.WithCancel(r.base)
	s := &stream{
		anchor: off,
		cursor: off,
		cancel: cancel,
		notify: make(chan struct{}, 1),
	}
	r.streams[slot] = s
	r.started = true
	go r.runPrefetch(streamCtx, s)
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// runPrefetch keeps the window [cursor, cursor+Window) filled for the life
// of the stream: blocks are fetched in order, at most Concurrency at a
// time; when caught up it waits for the cursor to advance. It only exits
// on cancellation (seek / close) or end of file.
func (r *Reader) runPrefetch(ctx context.Context, s *stream) {
	sem := make(chan struct{}, r.cfg.Concurrency)
	bi := s.cursor / r.cfg.BlockSize
	for {
		if ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		windowEnd := s.cursor + r.cfg.Window
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
			case <-s.notify:
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

// Close cancels all prefetch streams and the inner reader.
func (r *Reader) Close() error {
	r.mu.Lock()
	for _, s := range r.streams {
		if s != nil {
			s.cancel()
		}
	}
	r.mu.Unlock()
	return r.inner.Close()
}
