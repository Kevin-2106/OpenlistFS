package openlist

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func postJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func TestListPaginationAndDecoding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "tok123" {
			t.Errorf("Authorization = %q", got)
		}
		var req struct {
			Path    string `json:"path"`
			Page    int    `json:"page"`
			PerPage int    `json:"per_page"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Path != "/Movies" {
			t.Errorf("path = %q", req.Path)
		}
		// Two pages: first full (2 entries), second partial (1 entry).
		if req.Page == 1 {
			postJSON(t, w, map[string]any{"code": 200, "message": "success", "data": map[string]any{
				"total": 3,
				"content": []map[string]any{
					{"name": "A.mkv", "size": 1073741824, "is_dir": false, "modified": 1700000000, "sign": "s1"},
					{"name": "Sub", "size": 0, "is_dir": true},
				},
			}})
			return
		}
		postJSON(t, w, map[string]any{"code": 200, "message": "success", "data": map[string]any{
			"total":   3,
			"content": []map[string]any{{"name": "B.mkv", "size": 5, "is_dir": false}},
		}})
	}))
	defer srv.Close()

	c := New(srv.URL, WithToken("tok123"))
	entries, err := c.List(context.Background(), "/Movies")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 || entries[0].Name != "A.mkv" || entries[0].Size != 1073741824 ||
		entries[1].IsDir != true || entries[2].Name != "B.mkv" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}

func TestStatNotFoundMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		postJSON(t, w, map[string]any{"code": 500, "message": "failed get objects: object not found"})
	}))
	defer srv.Close()

	c := New(srv.URL, WithToken("tok123"))
	_, err := c.Stat(context.Background(), "/Nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Op != "stat" {
		t.Fatalf("want *Error with Op=stat, got %T %v", err, err)
	}
}

func TestResolveLinkFallsBackToFsGet(t *testing.T) {
	fsLinkCalls := 0
	fsGetCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/fs/link":
			fsLinkCalls++
			postJSON(t, w, map[string]any{"code": 403, "message": "You are not an admin"})
		case "/api/fs/get":
			fsGetCalls++
			postJSON(t, w, map[string]any{"code": 200, "message": "success", "data": map[string]any{
				"name":    "A.mkv",
				"size":    10,
				"is_dir":  false,
				"raw_url": srvUnreachableURL(t),
				"header":  "",
			}})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, WithToken("tok123"))
	link, err := c.ResolveLink(context.Background(), "/A.mkv")
	if err != nil {
		t.Fatalf("ResolveLink: %v", err)
	}
	if link.Source != "fs/get" {
		t.Errorf("link.Source = %q", link.Source)
	}
	if fsLinkCalls != 1 || fsGetCalls != 1 {
		t.Fatalf("calls: fs/link=%d fs/get=%d", fsLinkCalls, fsGetCalls)
	}

	// Second resolve must skip fs/link entirely (permanent fallback).
	if _, err := c.ResolveLink(context.Background(), "/A.mkv"); err != nil {
		t.Fatalf("second ResolveLink: %v", err)
	}
	if fsLinkCalls != 1 || fsGetCalls != 2 {
		t.Fatalf("after retry: fs/link=%d fs/get=%d", fsLinkCalls, fsGetCalls)
	}
}

func srvUnreachableURL(t *testing.T) string {
	t.Helper()
	// Not fetched during the test; only decoded.
	return "https://cdn.example.com/file.mkv?sign=secret"
}

func TestResolveLinkViaFsLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		postJSON(t, w, map[string]any{"code": 200, "message": "success", "data": map[string]any{
			"url":    "https://cdn.example.com/f.mkv?sig=abc",
			"header": map[string]string{"Cookie": "k=v"},
		}})
	}))
	defer srv.Close()

	c := New(srv.URL, WithToken("tok123"))
	link, err := c.ResolveLink(context.Background(), "/A.mkv")
	if err != nil {
		t.Fatalf("ResolveLink: %v", err)
	}
	if link.Source != "fs/link" || link.Header["Cookie"] != "k=v" {
		t.Fatalf("unexpected link: %+v", link)
	}
}

func TestReLoginOnAuthError(t *testing.T) {
	logins := 0
	lists := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			logins++
			var req map[string]string
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req["username"] != "u" || req["password"] != "p" {
				t.Errorf("bad login request: %v", err)
			}
			postJSON(t, w, map[string]any{"code": 200, "message": "success", "data": map[string]any{"token": "fresh"}})
		case "/api/fs/list":
			lists++
			if got := r.Header.Get("Authorization"); got != "fresh" {
				postJSON(t, w, map[string]any{"code": 401, "message": "unauthorized"})
				return
			}
			postJSON(t, w, map[string]any{"code": 200, "message": "success", "data": map[string]any{
				"total": 0, "content": []any{},
			}})
		}
	}))
	defer srv.Close()

	c := New(srv.URL, WithToken("stale"), WithCredentials("u", "p"))
	if _, err := c.List(context.Background(), "/"); err != nil {
		t.Fatalf("List: %v", err)
	}
	if logins != 1 || lists != 2 {
		t.Fatalf("logins=%d lists=%d", logins, lists)
	}
}

func TestDecodeHeaderBothShapes(t *testing.T) {
	if h := decodeHeader(json.RawMessage(`""`)); h != nil {
		t.Errorf("empty string shape: %v", h)
	}
	if h := decodeHeader(nil); h != nil {
		t.Errorf("nil: %v", h)
	}
	h := decodeHeader(json.RawMessage(`{"Referer":"https://x"}`))
	if h == nil || h["Referer"] != "https://x" {
		t.Errorf("object shape: %v", h)
	}
}

func TestSanitizeURL(t *testing.T) {
	got := SanitizeURL("https://cdn.example.com/a b.mkv?sign=SECRET:0")
	if got != "https://cdn.example.com/a b.mkv?<redacted>" {
		t.Errorf("SanitizeURL = %q", got)
	}
	if got := SanitizeURL("https://cdn.example.com/plain.mkv"); got != "https://cdn.example.com/plain.mkv" {
		t.Errorf("SanitizeURL without query = %q", got)
	}
}

func TestStatDecode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		postJSON(t, w, map[string]any{"code": 200, "message": "success", "data": map[string]any{
			"name": "A.mkv", "size": 123, "is_dir": false, "modified": 1700000000,
			"raw_url": "https://x/y?sign=z", "header": "",
			"sign": "z", "type": strconv.Itoa(0),
		}})
	}))
	defer srv.Close()

	c := New(srv.URL, WithToken("t"))
	e, err := c.Stat(context.Background(), "/A.mkv")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if e.Name != "A.mkv" || e.Size != 123 || e.Modified != 1700000000 {
		t.Fatalf("entry = %+v", e)
	}
}
