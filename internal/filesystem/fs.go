package filesystem

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/winfsp/go-winfsp/gofs"

	"openlistfs/internal/vpath"
)

// Decorator optionally wraps the range reader of an open file — used to
// attach the read-ahead/cache layer without touching the source stack.
type Decorator func(path string, size int64, r RangeReader) RangeReader

// ListHook fires after a cold directory listing, with the freshly listed
// canonical dir path and its file entries. Implementations use it to warm
// caches in the background; it must not block the listing.
type ListHook func(dir string, files []Entry)

// FS implements gofs.FileSystem as a strictly read-only view of a Source.
type FS struct {
	src       Source
	ctx       context.Context // governs all remote operations (cancelled on unmount)
	decorator Decorator
	listHook  ListHook
}

var _ gofs.FileSystem = (*FS)(nil)

// Option configures the filesystem.
type Option func(*FS)

// WithDecorator installs a reader decorator (e.g. read-ahead).
func WithDecorator(d Decorator) Option {
	return func(f *FS) { f.decorator = d }
}

// WithListHook installs a post-listing cache warmer.
func WithListHook(h ListHook) Option {
	return func(f *FS) { f.listHook = h }
}

// New creates a read-only filesystem over src.
func New(ctx context.Context, src Source, opts ...Option) *FS {
	f := &FS{src: src, ctx: ctx}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// OpenFile opens a file or directory read-only. Any write/create flag is
// rejected: this filesystem is strictly read-only.
func (f *FS) OpenFile(name string, flag int, perm os.FileMode) (gofs.File, error) {
	t0 := time.Now()
	defer func() {
		slog.Debug("fs: OpenFile", "path", name, "flag", flag, "dur", time.Since(t0).Round(time.Millisecond))
	}()
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND|os.O_EXCL) != 0 {
		return nil, syscall.EPERM
	}
	p := vpath.FromWin(name)
	entry, reader, err := f.src.Open(f.ctx, p)
	if err != nil {
		return nil, mapErr(err)
	}
	if entry.IsDir {
		if reader != nil {
			reader.Close()
		}
		return newDirHandle(p, entry, f), nil
	}
	if f.decorator != nil && reader != nil {
		reader = f.decorator(p, entry.Size, reader)
	}
	return newFileHandle(p, entry, reader), nil
}

// Stat returns metadata for one path (gofs calls this on every open).
func (f *FS) Stat(name string) (os.FileInfo, error) {
	t0 := time.Now()
	defer func() { slog.Debug("fs: Stat", "path", name, "dur", time.Since(t0).Round(time.Millisecond)) }()
	p := vpath.FromWin(name)
	entry, err := f.src.Stat(f.ctx, p)
	if err != nil {
		return nil, mapErr(err)
	}
	return fileInfo{name: vpath.Base(p), e: entry}, nil
}

func (f *FS) Mkdir(name string, perm os.FileMode) error { return syscall.EPERM }

func (f *FS) Remove(name string) error { return syscall.EPERM }

func (f *FS) Rename(source, target string) error { return syscall.EPERM }

// mapErr converts source errors to the syscall.Errno values gofs maps onto
// NTSTATUS; anything unrecognized stays as-is (STATUS_INTERNAL_ERROR in gofs).
func mapErr(err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, os.ErrPermission):
		return syscall.EPERM
	default:
		return err
	}
}
