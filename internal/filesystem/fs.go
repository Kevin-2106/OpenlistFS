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
	writable  bool
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

// WithWritable enables write support: mutations map to the Source's Writer
// and file handles buffer writes locally, uploading on last close. Without
// it every mutation is EPERM (the default — this is a read-only PoC).
func WithWritable(w bool) Option {
	return func(f *FS) { f.writable = w }
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
	writeIntent := flag&(os.O_WRONLY|os.O_RDWR) != 0
	if !f.writable && flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND|os.O_EXCL) != 0 {
		return nil, syscall.EPERM
	}
	p := vpath.FromWin(name)

	if f.writable && writeIntent {
		return f.openWritable(p, flag)
	}
	if f.writable && flag&os.O_CREATE != 0 {
		// Read-only open with create disposition: behave as a plain open
		// when the file exists; create nothing.
		if _, err := f.src.Stat(f.ctx, p); errors.Is(err, os.ErrNotExist) {
			return nil, syscall.ENOENT
		}
	}
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

// openWritable implements the writable-mode open path: an overlay handle
// that serves reads from remote+spool and uploads on last close.
func (f *FS) openWritable(p string, flag int) (gofs.File, error) {
	entry, statErr := f.src.Stat(f.ctx, p)
	exists := statErr == nil
	if !exists {
		if !errors.Is(statErr, os.ErrNotExist) {
			return nil, mapErr(statErr)
		}
		if flag&os.O_CREATE == 0 {
			return nil, syscall.ENOENT
		}
	}
	if exists && flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0 {
		return nil, syscall.EEXIST
	}
	truncate := exists && flag&os.O_TRUNC != 0

	var remote RangeReader
	hasRemote := exists && !entry.IsDir
	if hasRemote {
		_, reader, err := f.src.Open(f.ctx, p)
		if err != nil {
			return nil, mapErr(err)
		}
		if f.decorator != nil && reader != nil {
			reader = f.decorator(p, entry.Size, reader)
		}
		remote = reader
	}

	spool, err := os.CreateTemp("", "openlistfs-w-*.spool")
	if err != nil {
		if remote != nil {
			remote.Close()
		}
		return nil, err
	}

	writer, ok := f.src.(Writer)
	if !ok {
		spool.Close()
		if remote != nil {
			remote.Close()
		}
		return nil, syscall.EPERM
	}
	return newOverlayHandle(f, p, entry, remote, hasRemote, truncate, spool, writer), nil
}

// Mkdir creates a remote directory when writable.
func (f *FS) Mkdir(name string, perm os.FileMode) error {
	if !f.writable {
		return syscall.EPERM
	}
	w, ok := f.src.(Writer)
	if !ok {
		return syscall.EPERM
	}
	return mapErr(w.Mkdir(f.ctx, vpath.FromWin(name)))
}

// Remove deletes a remote file or directory tree when writable.
func (f *FS) Remove(name string) error {
	if !f.writable {
		return syscall.EPERM
	}
	w, ok := f.src.(Writer)
	if !ok {
		return syscall.EPERM
	}
	return mapErr(w.Remove(f.ctx, vpath.FromWin(name)))
}

// Rename renames a remote object when writable.
func (f *FS) Rename(source, target string) error {
	if !f.writable {
		return syscall.EPERM
	}
	w, ok := f.src.(Writer)
	if !ok {
		return syscall.EPERM
	}
	return mapErr(w.Rename(f.ctx, vpath.FromWin(source), vpath.Base(vpath.FromWin(target))))
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
