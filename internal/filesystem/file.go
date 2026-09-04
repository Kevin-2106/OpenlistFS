package filesystem

import (
	"io"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/winfsp/go-winfsp/gofs"

	"openlistfs/internal/vpath"
)

// fileHandle is an open regular file. Reads go straight to the range
// reader — this is the hot path; it must stay free of extra locking and
// copying beyond the position mutex.
type fileHandle struct {
	path   string // canonical slash path, for diagnostics
	entry  Entry
	reader RangeReader

	mu  sync.Mutex
	pos int64
}

var _ gofs.File = (*fileHandle)(nil)

func newFileHandle(path string, entry Entry, r RangeReader) *fileHandle {
	return &fileHandle{path: path, entry: entry, reader: r}
}

func (h *fileHandle) ReadAt(p []byte, off int64) (int, error) {
	return h.reader.ReadAt(p, off)
}

func (h *fileHandle) Read(p []byte) (int, error) {
	h.mu.Lock()
	off := h.pos
	h.mu.Unlock()
	n, err := h.ReadAt(p, off)
	h.mu.Lock()
	h.pos += int64(n)
	h.mu.Unlock()
	return n, err
}

func (h *fileHandle) Seek(offset int64, whence int) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch whence {
	case io.SeekStart:
		h.pos = offset
	case io.SeekCurrent:
		h.pos += offset
	case io.SeekEnd:
		h.pos = h.entry.Size + offset
	default:
		return 0, syscall.EINVAL
	}
	return h.pos, nil
}

func (h *fileHandle) Write(p []byte) (int, error)              { return 0, syscall.EPERM }
func (h *fileHandle) WriteAt(p []byte, off int64) (int, error) { return 0, syscall.EPERM }
func (h *fileHandle) Truncate(size int64) error                { return syscall.EPERM }
func (h *fileHandle) Sync() error                              { return nil }

func (h *fileHandle) Readdir(count int) ([]os.FileInfo, error) { return nil, syscall.ENOTDIR }

// Stat answers from the metadata cached at open time — no network call.
func (h *fileHandle) Stat() (os.FileInfo, error) {
	return fileInfo{name: vpath.Base(h.path), e: h.entry}, nil
}

func (h *fileHandle) Close() error {
	if h.reader == nil {
		return nil
	}
	return h.reader.Close()
}

// dirHandle is an open directory.
type dirHandle struct {
	fs    *FS
	path  string
	entry Entry

	mu      sync.Mutex
	listing []os.FileInfo
	offset  int
}

var _ gofs.File = (*dirHandle)(nil)

func newDirHandle(path string, entry Entry, fs *FS) *dirHandle {
	return &dirHandle{fs: fs, path: path, entry: entry}
}

func (d *dirHandle) Readdir(count int) ([]os.FileInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.listing == nil {
		list, err := d.fs.src.List(d.fs.ctx, d.path)
		if err != nil {
			return nil, mapErr(err)
		}
		d.listing = make([]os.FileInfo, len(list))
		for i, e := range list {
			d.listing[i] = fileInfo{name: e.Name, e: e}
		}
	}
	rest := d.listing[d.offset:]
	if count <= 0 {
		d.offset = len(d.listing)
		return rest, nil
	}
	if len(rest) > count {
		rest = rest[:count]
	}
	d.offset += len(rest)
	return rest, nil
}

func (d *dirHandle) Read(p []byte) (int, error) { return 0, syscall.EISDIR }

func (d *dirHandle) ReadAt(p []byte, off int64) (int, error) { return 0, syscall.EISDIR }

func (d *dirHandle) Seek(offset int64, whence int) (int64, error) { return 0, nil }

func (d *dirHandle) Write(p []byte) (int, error)              { return 0, syscall.EPERM }
func (d *dirHandle) WriteAt(p []byte, off int64) (int, error) { return 0, syscall.EPERM }
func (d *dirHandle) Truncate(size int64) error                { return syscall.EPERM }
func (d *dirHandle) Sync() error                              { return nil }
func (d *dirHandle) Close() error                             { return nil }

func (d *dirHandle) Stat() (os.FileInfo, error) {
	return fileInfo{name: vpath.Base(d.path), e: d.entry}, nil
}

// fileInfo implements os.FileInfo. Sys() is nil on purpose: gofs then uses
// ModTime for all timestamps and derives attributes from Mode().
type fileInfo struct {
	name string
	e    Entry
}

var _ os.FileInfo = fileInfo{}

func (f fileInfo) Name() string       { return f.name }
func (f fileInfo) Size() int64        { return f.e.Size }
func (f fileInfo) ModTime() time.Time { return f.e.ModTime }
func (f fileInfo) IsDir() bool        { return f.e.IsDir }
func (f fileInfo) Sys() any           { return nil }

func (f fileInfo) Mode() os.FileMode {
	if f.e.IsDir {
		return os.ModeDir | 0555
	}
	return 0444 // no write bits: Windows sees FILE_ATTRIBUTE_READONLY
}
