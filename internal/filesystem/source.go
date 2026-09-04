// Package filesystem adapts a remote read-only Source to the gofs
// filesystem interface used by WinFsp.
//
// The filesystem layer knows nothing about OpenList endpoints or HTTP URLs:
// it sees entries, listings, and opaque range readers. All mutations are
// rejected with EPERM; nothing pretends to succeed.
package filesystem

import (
	"context"
	"io"
	"time"
)

// Entry is the metadata the filesystem needs about one object.
type Entry struct {
	Name    string // base name; mount point name at root
	Size    int64  // bytes; 0 for directories
	IsDir   bool
	ModTime time.Time
}

// RangeReader is the piece of an open file that serves data.
type RangeReader interface {
	io.ReaderAt
	io.Closer
}

// Source abstracts the remote storage behind the filesystem.
type Source interface {
	// List returns the entries of dir. dir is a canonical slash path ("/" = root).
	List(ctx context.Context, dir string) ([]Entry, error)
	// Stat returns metadata for one canonical slash path.
	Stat(ctx context.Context, path string) (Entry, error)
	// Open returns the entry's metadata and a reader for its contents.
	// Only valid for regular files.
	Open(ctx context.Context, path string) (Entry, RangeReader, error)
}
