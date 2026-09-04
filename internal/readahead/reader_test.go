package readahead

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeInner serves a static buffer, counting calls; optionally slow.
type fakeInner struct {
	data      []byte
	mu        sync.Mutex
	calls     int
	bytes     int64
	honorsCtx bool
}

func (f *fakeInner) ReadAt(p []byte, off int64) (int, error) {
	return f.ReadAtContext(context.Background(), p, off)
}

func (f *fakeInner) ReadAtContext(ctx context.Context, p []byte, off int64) (int, error) {
	f.mu.Lock()
	f.calls++
	f.bytes += int64(len(p))
	f.mu.Unlock()
	if f.honorsCtx {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
		}
	}
	if off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *fakeInner) Close() error { return nil }

func newData(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestServesContentWithCache(t *testing.T) {
	data := newData(64 * 1024)
	inner := &fakeInner{data: data}
	cache := NewCache(1 << 20)
	r := NewReader(context.Background(), "f", int64(len(data)), inner, cache, Config{
		BlockSize: 1024, Window: 64 * 1024, Concurrency: 2,
	})
	defer r.Close()

	// First read: miss on blocks, exact fetch.
	buf := make([]byte, 100)
	n, err := r.ReadAt(buf, 5000)
	if err != nil || n != 100 {
		t.Fatalf("ReadAt = %d, %v", n, err)
	}
	if string(buf) != string(data[5000:5100]) {
		t.Fatal("content mismatch on first read")
	}

	// Prefetch should fill blocks; later reads must be cache hits.
	waitFor(t, 2*time.Second, func() bool { return cache.Metrics.PrefetchBytes.Load() > 4096 })

	before := cache.Metrics.HitBytes.Load()
	for off := int64(2000); off < 30000; off += 500 {
		n, err := r.ReadAt(buf, off)
		if err != nil || n != 100 {
			t.Fatalf("ReadAt(%d) = %d, %v", off, n, err)
		}
		if string(buf) != string(data[off:off+100]) {
			t.Fatalf("content mismatch at %d", off)
		}
	}
	if cache.Metrics.HitBytes.Load() <= before {
		t.Fatal("expected cache hits once prefetch filled blocks")
	}
}

func TestSeekCancelsStream(t *testing.T) {
	data := newData(256 * 1024)
	inner := &fakeInner{data: data, honorsCtx: true}
	cache := NewCache(1 << 20)
	r := NewReader(context.Background(), "f", int64(len(data)), inner, cache, Config{
		BlockSize: 1024, Window: 128 * 1024, Concurrency: 1,
	})
	defer r.Close()

	buf := make([]byte, 64)
	if _, err := r.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadAt(buf, 200*1024); err != nil {
		t.Fatal(err)
	}
	if cache.Metrics.SeekCancels.Load() != 1 {
		t.Fatalf("SeekCancels = %d, want 1", cache.Metrics.SeekCancels.Load())
	}
	// The seek read must still be correct.
	if !strings.Contains(string(data[200*1024:200*1024+64]), string(buf)) {
		t.Fatal("content mismatch after seek")
	}
}

func TestEOFAcrossEnd(t *testing.T) {
	data := newData(5000)
	inner := &fakeInner{data: data}
	cache := NewCache(1 << 20)
	r := NewReader(context.Background(), "f", int64(len(data)), inner, cache, Config{
		BlockSize: 1024, Window: 4096, Concurrency: 1,
	})
	defer r.Close()

	if n, err := r.ReadAt(make([]byte, 10), 5000); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt at EOF = %d, %v", n, err)
	}
	buf := make([]byte, 2000)
	n, err := r.ReadAt(buf, 4000)
	if n != 1000 || !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt crossing EOF = %d, %v; want 1000, io.EOF", n, err)
	}
	if string(buf[:n]) != string(data[4000:]) {
		t.Fatal("EOF content mismatch")
	}
}

func TestLRUEvictionUnderCap(t *testing.T) {
	data := newData(200 * 1024)
	inner := &fakeInner{data: data}
	// Cap fits ~2 blocks of 1024B… well below the prefetch window.
	cache := NewCache(3 * 1024)
	r := NewReader(context.Background(), "f", int64(len(data)), inner, cache, Config{
		BlockSize: 1024, Window: 128 * 1024, Concurrency: 1,
	})
	defer r.Close()

	buf := make([]byte, 100)
	if _, err := r.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return cache.Used() > 0 })
	if cache.Used() > 3*1024 {
		t.Fatalf("cache used %d exceeds cap %d", cache.Used(), 3*1024)
	}
}

func TestPrefetchWasteAccounted(t *testing.T) {
	data := newData(200 * 1024)
	inner := &fakeInner{data: data}
	// Tiny cache: prefetched blocks get evicted before being read.
	cache := NewCache(1024)
	r := NewReader(context.Background(), "f", int64(len(data)), inner, cache, Config{
		BlockSize: 1024, Window: 128 * 1024, Concurrency: 1,
	})
	defer r.Close()

	buf := make([]byte, 64)
	if _, err := r.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return cache.Metrics.PrefetchWaste.Load() > 0 })
	if cache.Metrics.PrefetchWaste.Load() == 0 {
		t.Fatal("expected eviction waste for unread prefetch blocks")
	}
}
