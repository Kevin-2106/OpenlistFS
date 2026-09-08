package webdav

// Live smoke against a real server's WebDAV endpoint. Skipped unless
// OPENLISTFS_LIVE=1; credentials and URL come from the standard .env
// (OPENLISTFS_URL / OPENLISTFS_USERNAME / OPENLISTFS_PASSWORD). The DAV
// base mirrors the mounter's rule: server URL + /dav.
//
// Run: OPENLISTFS_LIVE=1 go test ./internal/webdav/ -run Live -v

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"openlistfs/internal/config"
)

func TestLiveOpenListDav(t *testing.T) {
	if os.Getenv("OPENLISTFS_LIVE") != "1" {
		t.Skip("set OPENLISTFS_LIVE=1 to run the live /dav smoke test")
	}
	// go test's CWD is the package dir; .env lives at the repo root.
	config.LoadDotEnv("../../.env")
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("no live config: %v", err)
	}
	base := strings.TrimRight(cfg.URL, "/") + "/dav"
	c := New(base, cfg.Username, cfg.Password)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Logf("server: %s (user %q)", base, cfg.Username)
	entries, err := c.Propfind(ctx, "/", 1)
	if err != nil {
		t.Fatalf("propfind root: %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("root has no children: %+v", entries)
	}
	for _, e := range entries[1:] {
		t.Logf("  %s dir=%v size=%d", e.Path, e.IsDir, e.Size)
	}

	// Find any file under /videos and range-read it through the source.
	if _, err := c.Propfind(ctx, "/videos", 1); err != nil {
		t.Skipf("no /videos on this server: %v", err)
	}
	videos, err := c.Propfind(ctx, "/videos", 1)
	if err != nil {
		t.Fatalf("propfind /videos: %v", err)
	}
	var pick string
	for _, e := range videos[1:] {
		if !e.IsDir && e.Size > 1024 {
			pick = e.Path
			break
		}
	}
	if pick == "" {
		t.Skip("no file >1KiB under /videos")
	}

	src := c.Source()
	entry, reader, err := src.Open(ctx, pick)
	if err != nil {
		t.Fatalf("open %s: %v", pick, err)
	}
	defer reader.Close()
	buf := make([]byte, 1024)
	n, err := reader.ReadAt(buf, entry.Size/2)
	if err != nil && n == 0 {
		t.Fatalf("readat: %v", err)
	}
	t.Logf("read %d bytes at offset %d of %s (%d bytes) — OK", n, entry.Size/2, pick, entry.Size)
}
