package webdav

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/net/webdav"
)

// newTestServer serves a real WebDAV filesystem (x/net/webdav) over a temp
// dir mounted under the /dav prefix, with Basic auth.
func newTestServer(t *testing.T, user, pass string) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	writeFile := func(rel, content string, modTime time.Time) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}
	mod := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	writeFile("videos/hello.txt", "0123456789", mod)
	// Pathological names: space, unicode, '#' (fragment delimiter when
	// unescaped), '%', '+' — the href round-trip must preserve them all.
	writeFile("videos/weird #1%+.txt", "weird", mod)
	if err := os.MkdirAll(filepath.Join(root, "videos/空 目录"), 0o755); err != nil {
		t.Fatal(err)
	}

	h := &webdav.Handler{
		FileSystem: webdav.Dir(root),
		LockSystem: webdav.NewMemLS(),
		Prefix:     "/dav/",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user != "" {
			want := "Basic " + basicAuth(user, pass)
			if r.Header.Get("Authorization") != want {
				w.Header().Set("WWW-Authenticate", `Basic realm="dav"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, srv.URL + "/dav"
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

func TestPropfindDepth1(t *testing.T) {
	_, base := newTestServer(t, "", "")
	c := New(base, "", "")
	entries, err := c.Propfind(context.Background(), "/videos", 1)
	if err != nil {
		t.Fatalf("propfind: %v", err)
	}
	if len(entries) == 0 || entries[0].Path != "/videos" || !entries[0].IsDir {
		t.Fatalf("first entry should be the collection itself, got %+v", entries[0])
	}
	byPath := map[string]Entry{}
	for _, e := range entries[1:] {
		byPath[e.Path] = e
	}
	hello, ok := byPath["/videos/hello.txt"]
	if !ok {
		t.Fatalf("hello.txt missing; paths = %v", keys(byPath))
	}
	if hello.IsDir || hello.Size != 10 {
		t.Errorf("hello.txt = %+v", hello)
	}
	if hello.ModTime.IsZero() {
		t.Errorf("hello.txt has no parsed mtime")
	}
	weird, ok := byPath["/videos/weird #1%+.txt"]
	if !ok {
		t.Fatalf("escaped name did not round-trip; paths = %v", keys(byPath))
	}
	if weird.Size != 5 {
		t.Errorf("weird file = %+v", weird)
	}
	if _, ok := byPath["/videos/空 目录"]; !ok {
		t.Errorf("unicode dir missing; paths = %v", keys(byPath))
	}
}

func TestPropfindNotFound(t *testing.T) {
	_, base := newTestServer(t, "", "")
	c := New(base, "", "")
	_, err := c.Propfind(context.Background(), "/nope", 0)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
}

func TestPropfindAuthAndHeaderShape(t *testing.T) {
	// A hand-rolled 207 response with nonstandard namespace prefixes —
	// parsing must match elements by local name and the client must send
	// Basic credentials.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			t.Errorf("method = %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Basic "+basicAuth("u", "p") {
			t.Errorf("Authorization = %q", got)
		}
		if r.Header.Get("Depth") != "0" {
			t.Errorf("Depth = %q", r.Header.Get("Depth"))
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(207)
		w.Write([]byte(`<?xml version="1.0"?>
<D:multistatus xmlns:D="DAV:" xmlns:z="urn:zig">
 <z:response>
  <z:href>/dav/dir/file%20name.bin</z:href>
  <z:propstat><z:prop>
   <z:resourcetype/>
   <z:getcontentlength>42</z:getcontentlength>
   <z:getlastmodified>Mon, 07 Sep 2026 12:00:00 GMT</z:getlastmodified>
  </z:prop></z:propstat>
 </z:response>
</D:multistatus>`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/dav", "u", "p")
	entries, err := c.Propfind(context.Background(), "/dir/file name.bin", 0)
	if err != nil {
		t.Fatalf("propfind: %v", err)
	}
	e := entries[0]
	if e.Path != "/dir/file name.bin" || e.Size != 42 || e.IsDir {
		t.Errorf("entry = %+v", e)
	}
	want := time.Date(2026, time.September, 7, 12, 0, 0, 0, time.UTC)
	if !e.ModTime.Equal(want) {
		t.Errorf("mtime = %v, want %v", e.ModTime, want)
	}
}

func TestMutations(t *testing.T) {
	_, base := newTestServer(t, "", "")
	c := New(base, "", "")
	ctx := context.Background()

	if err := c.Mkdir(ctx, "/newdir"); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := c.Upload(ctx, "/newdir/file.bin", 4, bytes.NewReader([]byte("abcd"))); err != nil {
		t.Fatalf("upload: %v", err)
	}
	entries, err := c.Propfind(ctx, "/newdir", 1)
	if err != nil {
		t.Fatalf("propfind: %v", err)
	}
	if len(entries) != 2 || entries[1].Path != "/newdir/file.bin" || entries[1].Size != 4 {
		t.Fatalf("after upload: %+v", entries)
	}
	if err := c.Move(ctx, "/newdir/file.bin", "renamed.bin"); err != nil {
		t.Fatalf("move: %v", err)
	}
	entries, err = c.Propfind(ctx, "/newdir", 1)
	if err != nil {
		t.Fatalf("propfind: %v", err)
	}
	if len(entries) != 2 || entries[1].Path != "/newdir/renamed.bin" {
		t.Fatalf("after move: %+v", entries)
	}
	if err := c.Remove(ctx, "/newdir/renamed.bin"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	entries, err = c.Propfind(ctx, "/newdir", 1)
	if err != nil {
		t.Fatalf("propfind: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("after remove: %+v", entries)
	}
	if _, err := c.Propfind(ctx, "/newdir/renamed.bin", 0); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist after remove, got %v", err)
	}
}

func TestAuthRejected(t *testing.T) {
	_, base := newTestServer(t, "u", "p")
	c := New(base, "u", "wrong")
	if _, err := c.Propfind(context.Background(), "/", 0); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("want ErrPermission, got %v", err)
	}
}

func TestParseTimeLayouts(t *testing.T) {
	cases := []string{
		"Mon, 07 Sep 2026 12:00:00 GMT",
		"Mon, 07 Sep 2026 12:00:00 +0000",
		"2026-09-07T12:00:00Z",
		"2026-09-07T12:00:00.000Z",
	}
	for _, s := range cases {
		if parseTime(s).IsZero() {
			t.Errorf("parseTime(%q) = zero", s)
		}
	}
	if !parseTime("garbage").IsZero() {
		t.Error("parseTime(garbage) should be zero")
	}
	if !parseTime("  ").IsZero() {
		t.Error("parseTime(blank) should be zero")
	}
}

func keys(m map[string]Entry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
