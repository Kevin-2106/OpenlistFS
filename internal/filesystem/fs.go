package filesystem

import (
	"context"
	"errors"
	"os"
	"syscall"

	"github.com/winfsp/go-winfsp/gofs"

	"openlistfs/internal/vpath"
)

// FS implements gofs.FileSystem as a strictly read-only view of a Source.
type FS struct {
	src Source
	ctx context.Context // governs all remote operations (cancelled on unmount)
}

var _ gofs.FileSystem = (*FS)(nil)

// New creates a read-only filesystem over src.
func New(ctx context.Context, src Source) *FS {
	return &FS{src: src, ctx: ctx}
}

// OpenFile opens a file or directory read-only. Any write/create flag is
// rejected: this filesystem is strictly read-only.
func (f *FS) OpenFile(name string, flag int, perm os.FileMode) (gofs.File, error) {
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
	return newFileHandle(p, entry, reader), nil
}

// Stat returns metadata for one path (gofs calls this on every open).
func (f *FS) Stat(name string) (os.FileInfo, error) {
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
