// Package readahead adds a RAM block cache with seek-aware read-ahead on
// top of a range reader. It exists because WinFsp issues small (32 KiB)
// reads: serving them from large prefetched blocks turns per-request HTTP
// latency back into line-rate throughput.
//
// Semantics: the foreground read is never queued behind a prefetch. A seek
// cancels the old prefetch stream and re-anchors it at the new position.
package readahead

import (
	"container/list"
	"sync"
	"sync/atomic"
	"time"
)

// blockKey identifies one cached block of one file.
type blockKey struct {
	file  string
	block int64
}

// block is an immutable byte range once loaded; concurrent readers share
// the same slice. ready is closed when data/err are set.
type block struct {
	ready chan struct{}
	data  []byte
	err   error

	byPrefetch bool
	hits       atomic.Int64
}

// Metrics accumulates cache effectiveness.
type Metrics struct {
	HitBytes      atomic.Int64 // served from cache
	MissBytes     atomic.Int64 // served by a foreground exact fetch
	PrefetchBytes atomic.Int64 // fetched by the prefetch stream
	PrefetchWaste atomic.Int64 // prefetched bytes never served (evicted unread)
	SeekCancels   atomic.Int64 // prefetch streams cancelled by seek detection
	BlocksEvicted atomic.Int64
}

// Cache is a byte-capped LRU store shared across files. Safe for
// concurrent use.
type Cache struct {
	max int64

	// foregroundActive records when a foreground miss last happened, so
	// background warmers can yield the connection to the player.
	foregroundActive atomic.Int64

	mu     sync.Mutex
	blocks map[blockKey]*block
	elems  map[blockKey]*list.Element
	sizes  map[blockKey]int64
	order  *list.List // front = most recently used; values are blockKey
	used   int64

	Metrics *Metrics
}

// NewCache creates a cache capped at maxBytes of stored data.
func NewCache(maxBytes int64) *Cache {
	return &Cache{
		max:     maxBytes,
		blocks:  make(map[blockKey]*block),
		elems:   make(map[blockKey]*list.Element),
		sizes:   make(map[blockKey]int64),
		order:   list.New(),
		Metrics: &Metrics{},
	}
}

// Has reports whether the block is present and fully loaded (no waiting).
func (c *Cache) Has(file string, blockIdx int64) bool {
	b := c.peek(blockKey{file, blockIdx})
	if b == nil {
		return false
	}
	select {
	case <-b.ready:
		return b.err == nil
	default:
		return false
	}
}

// Get returns the block if it is already loaded, without triggering a load
// and without waiting.
func (c *Cache) Get(file string, blockIdx int64) ([]byte, bool) {
	b := c.peek(blockKey{file, blockIdx})
	if b == nil {
		return nil, false
	}
	select {
	case <-b.ready:
		if b.err != nil {
			return nil, false
		}
		b.hits.Add(1)
		c.Metrics.HitBytes.Add(int64(len(b.data)))
		return b.data, true
	default:
		return nil, false
	}
}

// Load ensures the block is loaded, loading it with load() if no one else
// is already doing so; concurrent callers for the same key share one load.
// byPrefetch only feeds metrics.
func (c *Cache) Load(file string, blockIdx int64, byPrefetch bool, load func() ([]byte, error)) ([]byte, error) {
	k := blockKey{file, blockIdx}

	c.mu.Lock()
	if b, ok := c.blocks[k]; ok {
		c.mu.Unlock()
		<-b.ready
		if b.err != nil {
			return nil, b.err
		}
		b.hits.Add(1)
		c.Metrics.HitBytes.Add(int64(len(b.data)))
		c.touch(k)
		return b.data, nil
	}
	b := &block{ready: make(chan struct{}), byPrefetch: byPrefetch}
	c.blocks[k] = b
	c.mu.Unlock()

	data, err := load()
	c.mu.Lock()
	b.data, b.err = data, err
	close(b.ready)
	if err == nil {
		c.sizes[k] = int64(len(data))
		c.used += int64(len(data))
		c.elems[k] = c.order.PushFront(k)
	}
	c.mu.Unlock()

	if err != nil {
		c.remove(k) // failed blocks must not stick around
		return nil, err
	}
	if byPrefetch {
		c.Metrics.PrefetchBytes.Add(int64(len(data)))
	}
	c.evict()
	return data, nil
}

func (c *Cache) peek(k blockKey) *block {
	c.mu.Lock()
	c.touchLocked(k)
	b := c.blocks[k]
	c.mu.Unlock()
	return b
}

func (c *Cache) touch(k blockKey) {
	c.mu.Lock()
	c.touchLocked(k)
	c.mu.Unlock()
}

// touchLocked moves k to the front if present. Caller holds mu.
func (c *Cache) touchLocked(k blockKey) {
	if e, ok := c.elems[k]; ok {
		c.order.MoveToFront(e)
	}
}

func (c *Cache) remove(k blockKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.blocks[k]
	if !ok {
		return
	}
	// Only drop blocks whose load already finished; in-flight removals are
	// handled by the loader itself via this same call.
	select {
	case <-b.ready:
	default:
		return
	}
	c.dropLocked(k, b)
}

// dropLocked removes k from all indexes and accounts its bytes. Caller
// holds mu and guarantees the block is ready.
func (c *Cache) dropLocked(k blockKey, b *block) {
	if e, ok := c.elems[k]; ok {
		c.order.Remove(e)
		delete(c.elems, k)
	}
	if b.err == nil {
		size := c.sizes[k]
		c.used -= size
		delete(c.sizes, k)
		if b.byPrefetch && b.hits.Load() == 0 {
			c.Metrics.PrefetchWaste.Add(size)
		}
	}
	delete(c.blocks, k)
	c.Metrics.BlocksEvicted.Add(1)
}

// evict drops least-recently-used blocks until used <= max. Blocks already
// handed out stay valid: slices are immutable.
func (c *Cache) evict() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.used > c.max {
		e := c.order.Back()
		if e == nil {
			return
		}
		k := e.Value.(blockKey)
		b, ok := c.blocks[k]
		if !ok {
			c.order.Remove(e)
			continue
		}
		select {
		case <-b.ready: // finished: safe to drop
			c.dropLocked(k, b)
		default: // still loading: skip, try the next one
			c.order.MoveToFront(e)
			return
		}
	}
}

// Used returns the currently stored byte count (for tests and stats).
func (c *Cache) Used() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}

// NoteForeground records that a foreground miss just happened.
func (c *Cache) NoteForeground() {
	c.foregroundActive.Store(time.Now().UnixNano())
}

// ForegroundBusy reports whether a foreground miss occurred within idle:
// background warmers should yield while it is true.
func (c *Cache) ForegroundBusy(idle time.Duration) bool {
	return time.Since(time.Unix(0, c.foregroundActive.Load())) < idle
}
