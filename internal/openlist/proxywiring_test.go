package openlist

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingTransport records every request URL that flows through it.
type recordingTransport struct {
	inner http.RoundTripper
	mu    sync.Mutex
	urls  []string
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.urls = append(r.urls, req.URL.String())
	r.mu.Unlock()
	return r.inner.RoundTrip(req)
}

func (r *recordingTransport) saw(urlPrefix string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, u := range r.urls {
		if strings.HasPrefix(u, urlPrefix) {
			return true
		}
	}
	return false
}

// TestWithTransportInstallsTransport proves the option reaches the client.
func TestWithTransportInstallsTransport(t *testing.T) {
	rt := NewTransport(nil)
	c := New("http://example.com", WithTransport(rt))
	if c.HTTP.Transport != rt {
		t.Fatal("WithTransport did not replace the client transport")
	}
	if c.HTTP.Timeout != 60*time.Second {
		t.Errorf("client timeout = %v, want unchanged 60s", c.HTTP.Timeout)
	}
}

// TestSourceTransportPlumbedToReaders proves WithSourceTransport routes
// direct-URL reads through the shared transport: fs/get answers with a
// raw_url on the same test server, and the ReadAt must appear at the
// recording transport.
func TestSourceTransportPlumbedToReaders(t *testing.T) {
	var fileSrv *httptest.Server
	fileSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Range", "bytes 2-5/10")
		w.WriteHeader(http.StatusPartialContent)
		io.WriteString(w, "2345")
	}))
	defer fileSrv.Close()

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "message": "success",
			"data": map[string]any{
				"name": "A.bin", "size": 10, "is_dir": false,
				"raw_url": fileSrv.URL + "/file",
			},
		})
	}))
	defer apiSrv.Close()

	rec := &recordingTransport{inner: http.DefaultTransport}
	c := New(apiSrv.URL, WithToken("t"))
	src := c.Source(WithSourceTransport(rec))

	entry, reader, err := src.Open(context.Background(), "/A.bin")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if entry.Size != 10 {
		t.Fatalf("entry size = %d", entry.Size)
	}
	if reader == nil {
		t.Fatal("no reader for regular file")
	}
	defer reader.Close()

	buf := make([]byte, 4)
	if _, err := reader.ReadAt(buf, 2); err != nil {
		t.Fatalf("readat: %v", err)
	}
	if string(buf) != "2345" {
		t.Fatalf("read = %q", buf)
	}
	if !rec.saw(fileSrv.URL + "/file") {
		t.Fatalf("range read did not go through the source transport; saw %v", rec.urls)
	}
	if rec.saw(apiSrv.URL) {
		t.Fatalf("API call unexpectedly routed through the reader transport; saw %v", rec.urls)
	}
}
