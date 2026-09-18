package s3

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"openlistfs/internal/filesystem"
)

func newTestSource(t *testing.T) filesystem.Source {
	_, c := seedStore(t)
	return c.Source(WithMetadataTTL(time.Minute))
}

func TestSourceListRoot(t *testing.T) {
	src := newTestSource(t)
	entries, err := src.List(context.Background(), "/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	if strings.Join(names, ",") != "docs,root.txt,videos" {
		t.Errorf("root names = %v", names)
	}
}

func TestSourceStatVariants(t *testing.T) {
	src := newTestSource(t)
	ctx := context.Background()

	if e, err := src.Stat(ctx, "/videos"); err != nil || !e.IsDir || e.Name != "videos" {
		t.Errorf("stat dir = %+v %v", e, err)
	}
	if e, err := src.Stat(ctx, "/videos/hello.txt"); err != nil || e.IsDir || e.Size != 10 {
		t.Errorf("stat file = %+v %v", e, err)
	}
	if e, err := src.Stat(ctx, "/videos/空 目录"); err != nil || !e.IsDir {
		t.Errorf("stat unicode dir = %+v %v", e, err)
	}
	if _, err := src.Stat(ctx, "/nope.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat missing = %v, want ErrNotExist", err)
	}
}

func TestSourceOpenReadsRange(t *testing.T) {
	src := newTestSource(t)
	entry, reader, err := src.Open(context.Background(), "/videos/hello.txt")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if entry.Size != 10 || reader == nil {
		t.Fatalf("entry = %+v, reader = %v", entry, reader)
	}
	defer reader.Close()
	buf := make([]byte, 4)
	if _, err := reader.ReadAt(buf, 6); err != nil || string(buf) != "6789" {
		t.Errorf("ReadAt(6) = %q %v", buf, err)
	}
	if _, err := reader.ReadAt(buf, 0); err != nil || string(buf) != "0123" {
		t.Errorf("ReadAt(0) = %q %v", buf, err)
	}
}

func TestSourceOpenDirYieldsNilReader(t *testing.T) {
	src := newTestSource(t)
	entry, reader, err := src.Open(context.Background(), "/videos")
	if err != nil || !entry.IsDir || reader != nil {
		t.Errorf("open dir = %+v %v %v", entry, reader, err)
	}
}

func TestSourceWriter(t *testing.T) {
	src := newTestSource(t)
	w := src.(filesystem.Writer)
	ctx := context.Background()

	if err := w.Mkdir(ctx, "/newdir"); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if e, err := src.Stat(ctx, "/newdir"); err != nil || !e.IsDir {
		t.Fatalf("stat newdir = %+v %v", e, err)
	}
	if err := w.Upload(ctx, "/newdir/file.bin", 4, strings.NewReader("abcd")); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if e, err := src.Stat(ctx, "/newdir/file.bin"); err != nil || e.Size != 4 {
		t.Fatalf("stat uploaded = %+v %v", e, err)
	}
	if err := w.Rename(ctx, "/newdir/file.bin", "renamed.bin"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if e, err := src.Stat(ctx, "/newdir/renamed.bin"); err != nil || e.Size != 4 {
		t.Fatalf("stat renamed = %+v %v", e, err)
	}
	if _, err := src.Stat(ctx, "/newdir/file.bin"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old name should be gone, got %v", err)
	}
	if err := w.Rename(ctx, "/newdir", "moveddir"); err == nil {
		t.Error("renaming a directory should be rejected")
	}
	if err := w.Remove(ctx, "/newdir/renamed.bin"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := src.Stat(ctx, "/newdir/renamed.bin"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("removed file should be gone, got %v", err)
	}
}

func TestSourceListCacheInvalidatedByMkdir(t *testing.T) {
	src := newTestSource(t)
	w := src.(filesystem.Writer)
	ctx := context.Background()

	if _, err := src.List(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if err := w.Mkdir(ctx, "/fresh"); err != nil {
		t.Fatal(err)
	}
	entries, err := src.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name == "fresh" {
			return
		}
	}
	t.Errorf("fresh dir missing after mkdir: %+v", entries)
}
