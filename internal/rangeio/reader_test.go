package rangeio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

const testSize = 1 << 20 // 1 MiB of data

func testData() []byte {
	b := make([]byte, testSize)
	for i := range b {
		b[i] = byte(i * 31)
	}
	return b
}

// fullServer implements correct Range semantics over a static buffer.
func fullServer(t *testing.T, data []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rng := r.Header.Get("Range")
		if rng == "" {
			w.WriteHeader(http.StatusOK)
			w.Write(data)
			return
		}
		var start, end int64
		if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &start, &end); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if start >= int64(len(data)) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			w.Write([]byte("invalid range"))
			return
		}
		if end >= int64(len(data)) {
			end = int64(len(data)) - 1
		}
		w.Header().Set("Content-Range",
			fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusPartialContent)
		w.Write(data[start : end+1])
	}))
}

func newReader(t *testing.T, url string, size int64, opts ...Option) *Reader {
	t.Helper()
	return New(context.Background(), Link{URL: url, Size: size}, opts...)
}

func TestReadAtExactRanges(t *testing.T) {
	data := testData()
	srv := fullServer(t, data)
	defer srv.Close()

	r := newReader(t, srv.URL, int64(len(data)))
	defer r.Close()

	// Full-buffer reads at various offsets, including end-past-EOF clamping.
	for _, off := range []int64{0, 1, 65535, testSize - 100, testSize - 1} {
		buf := make([]byte, 4096)
		n, err := r.ReadAt(buf, off)
		want := int64(len(buf))
		if off+want > testSize {
			want = testSize - off
		}
		if n != int(want) {
			t.Fatalf("ReadAt(off=%d) = %d bytes, want %d (err %v)", off, n, want, err)
		}
		if want == int64(len(buf)) && err != nil {
			t.Fatalf("ReadAt(off=%d) err = %v, want nil", off, err)
		}
		if want < int64(len(buf)) && !errors.Is(err, io.EOF) {
			t.Fatalf("ReadAt(off=%d) clamped read err = %v, want io.EOF", off, err)
		}
		if want > 0 && string(buf[:n]) != string(data[off:off+want]) {
			t.Fatalf("ReadAt(off=%d) content mismatch", off)
		}
	}
	// At EOF exactly.
	if n, err := r.ReadAt(make([]byte, 10), testSize); n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("ReadAt(EOF) = %d, %v; want 0, io.EOF", n, err)
	}
}

func TestRangeHeaderSent(t *testing.T) {
	var gotRange, gotCustom, gotAcceptEnc string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		gotCustom = r.Header.Get("X-Custom")
		gotAcceptEnc = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Range", "bytes 100-199/1000")
		w.WriteHeader(http.StatusPartialContent)
		w.Write(make([]byte, 100))
	}))
	defer srv.Close()

	r := newReader(t, srv.URL, 1000, WithLinkResolver(nil))
	r.link.Header = map[string]string{"X-Custom": "yes"}
	defer r.Close()

	buf := make([]byte, 100)
	if _, err := r.ReadAt(buf, 100); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if gotRange != "bytes=100-199" {
		t.Errorf("Range = %q", gotRange)
	}
	if gotCustom != "yes" {
		t.Errorf("custom header missing")
	}
	if gotAcceptEnc != "identity" {
		t.Errorf("Accept-Encoding = %q", gotAcceptEnc)
	}
}

func TestServerIgnoresRange(t *testing.T) {
	data := testData()
	// Always answers 200 with the whole file.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
	defer srv.Close()

	r := newReader(t, srv.URL, int64(len(data)))
	defer r.Close()

	// offset > 0 must abort with ErrRangeIgnored — never download the file.
	n, err := r.ReadAt(make([]byte, 1024), 4096)
	if n != 0 || !errors.Is(err, ErrRangeIgnored) {
		t.Fatalf("ReadAt(off>0) with 200 = %d, %v; want ErrRangeIgnored", n, err)
	}

	// offset 0: accepted but strictly capped at the requested length.
	n, err = r.ReadAt(make([]byte, 1024), 0)
	if err != nil || n != 1024 {
		t.Fatalf("ReadAt(off=0) with 200 = %d, %v; want capped 1024, nil", n, err)
	}
	if fetched := r.Metrics.BytesFetched.Load(); fetched != 1024 {
		t.Fatalf("BytesFetched = %d, want 1024 (must not swallow the whole body)", fetched)
	}
}

func TestMalformedContentRange(t *testing.T) {
	cases := []struct {
		cr     string
		status int
	}{
		{"", http.StatusPartialContent},
		{"bytes 500-599/1000", http.StatusPartialContent},  // wrong start
		{"bytes 100-9999/1000", http.StatusPartialContent}, // end beyond request
		{"bytes abc/1000", http.StatusPartialContent},      // garbage
		{"items 100-199/1000", http.StatusPartialContent},  // wrong unit
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c.cr != "" {
				w.Header().Set("Content-Range", c.cr)
			}
			w.WriteHeader(c.status)
			w.Write(make([]byte, 100))
		}))
		r := newReader(t, srv.URL, 1000)
		n, err := r.ReadAt(make([]byte, 100), 100)
		if err == nil {
			t.Errorf("Content-Range %q: err=nil, n=%d; want error", c.cr, n)
		}
		srv.Close()
	}
}

func TestTotalMismatchRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 100-199/999")
		w.WriteHeader(http.StatusPartialContent)
		w.Write(make([]byte, 100))
	}))
	defer srv.Close()

	r := newReader(t, srv.URL, 1000)
	defer r.Close()
	if _, err := r.ReadAt(make([]byte, 100), 100); err == nil ||
		!strings.Contains(err.Error(), "total 999") {
		t.Fatalf("err = %v, want total mismatch", err)
	}
}

func TestShortBodyIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 100-199/1000")
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusPartialContent)
		w.Write(make([]byte, 50)) // lies: half the promised bytes
	}))
	defer srv.Close()

	r := newReader(t, srv.URL, 1000)
	defer r.Close()
	n, err := r.ReadAt(make([]byte, 100), 100)
	// The body lies about its length; the read must fail loudly, never
	// report success with short data. Retries may re-fetch, so n can be a
	// partial count from the failed attempt.
	if err == nil || n >= 100 {
		t.Fatalf("= %d, %v; want error and n<100", n, err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) && !strings.Contains(err.Error(), "short body") {
		t.Fatalf("err = %v, want an unexpected-EOF-shaped error", err)
	}
}

func TestRedirectFollowed(t *testing.T) {
	data := testData()
	target := fullServer(t, data)
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/real", http.StatusFound)
	}))
	defer srv.Close()

	r := newReader(t, srv.URL, int64(len(data)))
	defer r.Close()
	buf := make([]byte, 1000)
	if _, err := r.ReadAt(buf, 5000); err != nil {
		t.Fatalf("ReadAt through redirect: %v", err)
	}
}

func TestExpiredLinkReResolvedOnce(t *testing.T) {
	data := testData()
	var resolved atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Old URL denies; any URL with the refreshed token works.
		if r.Header.Get("X-Token") != "refreshed" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("expired"))
			return
		}
		startS := r.Header.Get("Range")
		var start, end int64
		fmt.Sscanf(startS, "bytes=%d-%d", &start, &end)
		w.Header().Set("Content-Range",
			fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(data[start : end+1])
	}))
	defer srv.Close()

	r := newReader(t, srv.URL, int64(len(data)), WithLinkResolver(func(ctx context.Context) (Link, error) {
		resolved.Add(1)
		return Link{URL: srv.URL, Header: map[string]string{"X-Token": "refreshed"}, Size: int64(len(data))}, nil
	}))
	defer r.Close()

	buf := make([]byte, 1024)
	if _, err := r.ReadAt(buf, 100); err != nil {
		t.Fatalf("ReadAt after re-resolve: %v", err)
	}
	if resolved.Load() != 1 {
		t.Fatalf("resolver called %d times, want exactly 1", resolved.Load())
	}
	// Further reads reuse the refreshed link; resolver must not run again.
	if _, err := r.ReadAt(buf, 5000); err != nil {
		t.Fatalf("second ReadAt: %v", err)
	}
	if resolved.Load() != 1 {
		t.Fatalf("resolver called %d times after second read, want 1", resolved.Load())
	}
}

func TestExpiredLinkNoResolverFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	r := newReader(t, srv.URL, 1000)
	defer r.Close()
	_, err := r.ReadAt(make([]byte, 100), 0)
	if err == nil {
		t.Fatal("want error for permanent 403")
	}
}

func TestMetricsCounting(t *testing.T) {
	data := testData()
	srv := fullServer(t, data)
	defer srv.Close()

	r := newReader(t, srv.URL, int64(len(data)))
	defer r.Close()
	r.ReadAt(make([]byte, 4096), 0)
	r.ReadAt(make([]byte, 4096), 8192)

	s := r.Metrics.Snapshot()
	if s.Requests != 2 || s.RangeRequests != 2 || s.BytesFetched != 8192 {
		t.Fatalf("snapshot = %+v", s)
	}
	if s.AvgTTFB() < 0 {
		t.Fatal("negative TTFB")
	}
}

func TestParseContentRange(t *testing.T) {
	ok := map[string][3]int64{
		"bytes 0-99/1000":                        {0, 99, 1000},
		"bytes 1073741824-1074266111/1188355609": {1073741824, 1074266111, 1188355609},
		"bytes 5-10/*":                           {5, 10, 0},
	}
	for h, want := range ok {
		s, e, tot, good := parseContentRange(h)
		if !good || s != want[0] || e != want[1] || tot != want[2] {
			t.Errorf("parseContentRange(%q) = %d,%d,%d,%v", h, s, e, tot, good)
		}
	}
	bad := []string{"", "bytes", "bytes 1-2", "0-99/1000", "bytes -1-5/10", "bytes x-y/10"}
	for _, h := range bad {
		if _, _, _, good := parseContentRange(h); good {
			t.Errorf("parseContentRange(%q) unexpectedly ok", h)
		}
	}
}

func TestConcurrentReadAt(t *testing.T) {
	data := testData()
	srv := fullServer(t, data)
	defer srv.Close()

	r := newReader(t, srv.URL, int64(len(data)))
	defer r.Close()

	const workers = 8
	done := make(chan error, workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			buf := make([]byte, 8192)
			off := int64(w) * 65536
			n, err := r.ReadAt(buf, off)
			if err != nil {
				done <- err
				return
			}
			if string(buf[:n]) != string(data[off:off+int64(n)]) {
				done <- fmt.Errorf("worker %d content mismatch", w)
				return
			}
			done <- nil
		}(w)
	}
	for w := 0; w < workers; w++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if got := r.Metrics.BytesFetched.Load(); got != workers*8192 {
		t.Fatalf("BytesFetched=%d want %d (strconv sanity %s)",
			got, workers*8192, strconv.FormatInt(got, 10))
	}
}
