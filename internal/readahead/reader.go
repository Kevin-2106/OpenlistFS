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
	// ChunkSize is the fetch and cache granularity for everything:
	// foreground misses and prefetch streams load the same chunks, so
	// concurrent work is deduplicated by the cache's singleflight.
	// 0 means the 512 KiB default.
	ChunkSize int64
	// Window is the read-ahead distance beyond the stream cursor.
	Window int64
	// Concurrency is the number of parallel chunk fetches per stream.
	Concurrency int
}

// Reader decorates one open file with seek-aware prefetch streams over a
// shared Cache.
//
// Serving rules:
//   - Foreground misses and the prefetch streams fetch the SAME chunk
//     granularity through the same cache, so overlapping work is shared,
//     never duplicated.
//   - A read fully covered by loaded chunks never touches the network and
//     never disturbs the streams (MKV clusters ↔ Cues alternation is free
//     after first touch).
//   - The foreground is served by its own chunk load, never queued behind
//     a prefetch it did not join.
type Reader struct {
	inner Inner
	cache *Cache
	file  string
	size  int64
	chunk int64 // fetch/cache granularity
	cfg   Config
	base  context.Context

	mu         sync.Mutex
	streams    [2]*stream // ring; nil slots are free
	background bool
}

// stream is one prefetch front. Real files routinely have two hot regions
// (MKV: clusters at the read position, Cues near the end), so the Reader
// keeps up to two streams alive instead of cancelling on every alternation.
type stream struct {
	anchor int64 // where the stream was anchored
	cursor int64 // next byte this stream will ensure
	cancel context.CancelFunc
	notify chan struct{}
}

// NewReader decorates inner. base governs the Reader's lifetime (mount
// context); individual prefetch streams derive from it and are cancelled on
// seek.
func NewReader(base context.Context, file string, size int64, inner Inner, cache *Cache, cfg Config) *Reader {
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = 512 << 10
	}
	if cfg.Window <= 0 {
		cfg.Window = 64 << 20
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 3
	}
	return &Reader{
		inner: inner,
		cache: cache,
		file:  file,
		size:  size,
		chunk: cfg.ChunkSize,
		cfg:   cfg,
		base:  base,
	}
}

// SetBackground marks this reader as a cache warmer: it fills exactly the
// chunks it reads — no prefetch streams, no foreground-activity marking —
// so warming can never flood the connection the way a real player would.
func (r *Reader) SetBackground() { r.background = true }

// ReadAt serves p from cached chunks; anything missing is fetched
// synchronously at chunk granularity.
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
		ci := pos / r.chunk

		if data, ok := r.cache.Get(r.file, ci); ok && int64(len(data)) > pos-ci*r.chunk {
			n := copy(p[filled:], data[pos-ci*r.chunk:])
			r.cache.Metrics.HitBytes.Add(int64(n))
			r.advanceStream(pos + int64(n))
			filled += n
			continue
		}

		// Miss: load the containing chunk synchronously and feed the
		// prefetch streams.
		if !r.background {
			r.cache.NoteForeground()
		}
		slog.Debug("readahead: miss", "file", r.file, "off", pos, "chunk", ci)
		r.noteMiss(pos)
		data, err := r.cache.Load(r.file, ci, false, func() ([]byte, error) {
			return r.loadChunk(r.base, ci)
		})
		if errors.Is(err, context.Canceled) {
			// A cancelled prefetch stream owned the load we joined; retry
			// once under the mount context.
			data, err = r.cache.Load(r.file, ci, false, func() ([]byte, error) {
				return r.loadChunk(r.base, ci)
			})
		}
		if err != nil {
			return filled, err
		}
		n := copy(p[filled:], data[pos-ci*r.chunk:])
		if n == 0 {
			break // chunk empty: at EOF
		}
		r.cache.Metrics.MissBytes.Add(int64(n))
		filled += n
	}

	if filled < len(p) {
		return filled, io.EOF
	}
	return filled, nil
}

// advanceStream moves the owning stream's cursor forward when a cache HIT
// falls within its reach, so the read-ahead window slides with sequential
// consumption instead of stalling at the window edge. Hits outside every
// stream's reach (index probes, random access) disturb nothing.
func (r *Reader) advanceStream(end int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.streams {
		if s == nil {
			continue
		}
		if end >= s.anchor && end <= s.cursor+r.chunk {
			if end > s.cursor {
				s.cursor = end
				select {
				case s.notify <- struct{}{}:
				default:
				}
			}
			return
		}
	}
}

// noteMiss feeds the miss position to the prefetch streams. A miss within
// a stream's reach nudges that stream's cursor; a far miss anchors a new
// stream, replacing the oldest one once both slots are taken.
func (r *Reader) noteMiss(off int64) {
	if r.background {
		return // warmers fill only what they read
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, s := range r.streams {
		if s == nil {
			continue
		}
		if off >= s.anchor-r.chunk && off <= s.cursor+r.chunk {
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
	go r.runPrefetch(streamCtx, s)
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// runPrefetch keeps the window [cursor, cursor+Window) filled for the life
// of the stream: chunks are fetched in order, at most Concurrency at a
// time; when caught up it waits for the cursor to advance. It only exits
// on cancellation (seek / close) or end of file.
func (r *Reader) runPrefetch(ctx context.Context, s *stream) {
	ci := int64(-1) // next chunk to consider; persists across iterations
	for {
		if ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		windowEnd := s.cursor + r.cfg.Window
		if min := s.cursor / r.chunk; ci < min {
			// Never walk behind the playhead; chunks behind it are either
			// cached or evicted.
			ci = min
		}
		r.mu.Unlock()
		if windowEnd > r.size {
			windowEnd = r.size
		}
		chunkStart := ci * r.chunk
		if chunkStart >= windowEnd {
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
		if !r.cache.Has(r.file, ci) {
			// One cache-wide background budget: two streams must not
			// double the parallel fetches fighting the player.
			select {
			case r.cache.acquireBackground() <- struct{}{}:
			case <-ctx.Done():
				return
			}
			go func(ci int64) {
				defer func() { <-r.cache.releaseBackground() }()
				_, err := r.cache.Load(r.file, ci, true, func() ([]byte, error) {
					return r.loadChunk(ctx, ci)
				})
				if err != nil && ctx.Err() == nil {
					slog.Debug("readahead: prefetch chunk failed", "file", r.file,
						"chunk", ci, "err", err)
				}
			}(ci)
		}
		ci++
	}
}

func (r *Reader) loadChunk(ctx context.Context, ci int64) ([]byte, error) {
	start := ci * r.chunk
	end := start + r.chunk
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
