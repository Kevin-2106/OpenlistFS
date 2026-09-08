package webdav

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"openlistfs/internal/filesystem"
)

// The x/net/webdav handler serves Range requests on GET, so Open+ReadAt
// exercises the real wire path (including auth headers).

func TestSourceListStatCache(t *testing.T) {
	_, base := newTestServer(t, "", "")
	src := New(base, "", "").Source(WithMetadataTTL(time.Minute))

	entries, err := src.List(context.Background(), "/videos")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// hello.txt + weird + 空 目录
	if len(entries) != 3 {
		t.Fatalf("entries = %+v", entries)
	}
	// Stat answered from the list cache when present, else PROPFIND.
	e, err := src.Stat(context.Background(), "/videos/hello.txt")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if e.Name != "hello.txt" || e.Size != 10 || e.IsDir {
		t.Fatalf("stat entry = %+v", e)
	}
	if _, err := src.Stat(context.Background(), "/videos/nope.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
}

func TestSourceOpenReadRanges(t *testing.T) {
	_, base := newTestServer(t, "", "")
	src := New(base, "", "").Source()

	e, reader, err := src.Open(context.Background(), "/videos/hello.txt")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if reader == nil {
		t.Fatal("no reader")
	}
	defer reader.Close()
	if e.Size != 10 {
		t.Fatalf("size = %d", e.Size)
	}
	buf := make([]byte, 4)
	if _, err := reader.ReadAt(buf, 3); err != nil {
		t.Fatalf("readat: %v", err)
	}
	if string(buf) != "3456" {
		t.Fatalf("read = %q", buf)
	}
}

func TestSourceOpenDirAndAuthedServer(t *testing.T) {
	_, base := newTestServer(t, "u", "p")
	src := New(base, "u", "p").Source()

	if _, reader, err := src.Open(context.Background(), "/videos"); err != nil || reader != nil {
		t.Fatalf("open dir: reader=%v err=%v", reader, err)
	}
	_, reader, err := src.Open(context.Background(), "/videos/hello.txt")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	reader.Close()
}

func TestSourceWriterRoundtrip(t *testing.T) {
	_, base := newTestServer(t, "", "")
	src := New(base, "", "").Source()
	w, ok := src.(filesystem.Writer)
	if !ok {
		t.Fatal("webdav source does not implement filesystem.Writer")
	}
	ctx := context.Background()

	if err := w.Mkdir(ctx, "/writedir"); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := w.Upload(ctx, "/writedir/data.bin", int64(len("payload")), bytes.NewReader([]byte("payload"))); err != nil {
		t.Fatalf("upload: %v", err)
	}
	// Fresh listing must show the upload (cache invalidated).
	entries, err := src.List(ctx, "/writedir")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "data.bin" || entries[0].Size != 7 {
		t.Fatalf("entries = %+v", entries)
	}
	// Read it back through Open.
	_, reader, err := src.Open(ctx, "/writedir/data.bin")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reader.Close()
	got := make([]byte, 7)
	if _, err := reader.ReadAt(got, 0); err != nil {
		t.Fatalf("readat: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("content = %q", got)
	}
}
