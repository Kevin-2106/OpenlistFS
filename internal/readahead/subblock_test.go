package readahead

import (
	"context"
	"testing"
	"time"
)

// First touch must cost sub-block granularity, not a whole block, so that
// shell thumbnail extraction across many files stays cheap.
func TestFirstTouchUsesSubBlock(t *testing.T) {
	data := newData(512 * 1024)
	inner := &fakeInner{data: data}
	cache := NewCache(64 << 20)
	r := NewReader(context.Background(), "f", int64(len(data)), inner, cache, Config{
		BlockSize:    256 * 1024,
		SubBlockSize: 32 * 1024,
		Window:       512 * 1024,
		Concurrency:  2,
	})
	defer r.Close()

	before := inner.bytes
	buf := make([]byte, 4096)
	if _, err := r.ReadAt(buf, 70000); err != nil {
		t.Fatal(err)
	}
	fetched := inner.bytes - before
	if fetched > 64*1024 {
		t.Fatalf("first touch fetched %d bytes, want ~sub-block (32KiB), not a whole block", fetched)
	}
	if string(buf) != string(data[70000:74096]) {
		t.Fatal("content mismatch")
	}
	// The prefetch stream still fills full blocks in the background.
	waitFor(t, 2*time.Second, func() bool { return cache.Metrics.PrefetchBytes.Load() > 0 })
}
