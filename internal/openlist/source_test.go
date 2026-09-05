package openlist

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestSourceMetadataCache(t *testing.T) {
	var lists, gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/list":
			lists.Add(1)
			postJSON(t, w, map[string]any{"code": 200, "message": "success", "data": map[string]any{
				"total": 1, "content": []map[string]any{
					{"name": "A.mkv", "size": 10, "is_dir": false, "modified": 1700000000},
				},
			}})
		case "/api/fs/get":
			gets.Add(1)
			postJSON(t, w, map[string]any{"code": 200, "message": "success", "data": map[string]any{
				"name": "A.mkv", "size": 10, "is_dir": false, "modified": 1700000000,
				"raw_url": "https://cdn.example.com/a.mkv?sign=x", "header": "",
			}})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	src := New(srv.URL, WithToken("t")).Source(WithMetadataTTL(80 * time.Millisecond))
	ctx := context.Background()

	if _, err := src.List(ctx, "/D"); err != nil {
		t.Fatal(err)
	}
	if _, err := src.List(ctx, "/D"); err != nil {
		t.Fatal(err)
	}
	if lists.Load() != 1 {
		t.Fatalf("lists = %d, want 1 (second must be cached)", lists.Load())
	}

	if _, err := src.Stat(ctx, "/D/A.mkv"); err != nil {
		t.Fatal(err)
	}
	if _, r, err := src.Open(ctx, "/D/A.mkv"); err != nil || r == nil {
		t.Fatalf("Open: %v", err)
	}
	if gets.Load() != 1 {
		t.Fatalf("gets = %d, want 1 (Stat+Open must share one fs/get)", gets.Load())
	}

	// TTL expiry triggers a refetch.
	time.Sleep(120 * time.Millisecond)
	if _, err := src.List(ctx, "/D"); err != nil {
		t.Fatal(err)
	}
	if lists.Load() != 2 {
		t.Fatalf("lists after TTL = %d, want 2", lists.Load())
	}
}

func TestSourceOpenDirHasNoReader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		postJSON(t, w, map[string]any{"code": 200, "message": "success", "data": map[string]any{
			"name": "D", "size": 0, "is_dir": true, "raw_url": "", "header": "",
		}})
	}))
	defer srv.Close()

	src := New(srv.URL, WithToken("t")).Source()
	entry, r, err := src.Open(context.Background(), "/D")
	if err != nil {
		t.Fatal(err)
	}
	if !entry.IsDir || r != nil {
		t.Fatalf("dir open: isDir=%v reader=%v", entry.IsDir, r)
	}
}
