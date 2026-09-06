package filesystem

import (
	"errors"
	"io"
	"log/slog"
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
//
// In writable mode the handle becomes an overlay: writes land in a local
// spool file, reads serve written ranges from the spool and the rest from
// the remote, and the merged content is uploaded on Close when dirty.
type fileHandle struct {
	path   string // canonical slash path, for diagnostics
	entry  Entry
	reader RangeReader

	mu  sync.Mutex
	pos int64

	// writable overlay state (zeroed in read-only mode)
	fs         *FS
	writeMode  bool
	spool      *os.File
	spoolPath  string
	written    ranges
	localSize  int64
	truncSet   bool
	truncSize  int64
	hasRemote  bool
	remoteSize int64
	dirty      bool
}

var _ gofs.File = (*fileHandle)(nil)

func newFileHandle(path string, entry Entry, r RangeReader) *fileHandle {
	return &fileHandle{path: path, entry: entry, reader: r}
}

func newOverlayHandle(fs *FS, path string, entry Entry, remote RangeReader, hasRemote, truncate bool, spool *os.File, w Writer) *fileHandle {
	h := &fileHandle{
		path:       path,
		entry:      entry,
		reader:     remote,
		fs:         fs,
		writeMode:  true,
		spool:      spool,
		spoolPath:  spool.Name(),
		hasRemote:  hasRemote,
		remoteSize: entry.Size,
		localSize:  entry.Size,
	}
	if !hasRemote {
		h.localSize = 0
	}
	h.dirty = truncate && hasRemote // truncating an existing file must upload
	if truncate {
		h.truncSet, h.truncSize = true, 0
		_ = h.spool.Truncate(0)
		h.localSize = 0
	}
	return h
}

func (h *fileHandle) ReadAt(p []byte, off int64) (int, error) {
	if !h.writeMode {
		return h.reader.ReadAt(p, off)
	}
	return h.overlayReadAt(p, off)
}

// overlayReadAt serves written ranges from the spool and holes from the
// remote reader. Reads past EOF return io.EOF per the ReaderAt contract.
func (h *fileHandle) overlayReadAt(p []byte, off int64) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	end := off + int64(len(p))
	filled := 0
	pos := off
	for pos < end {
		// find the next written range starting at or after pos
		var wr *[2]int64
		for i := range h.written {
			if h.written[i][1] > pos {
				wr = &h.written[i]
				break
			}
		}
		var segEnd int64
		fromSpool := false
		if wr != nil && wr[0] <= pos {
			segEnd = wr[1]
			fromSpool = true
		} else if wr != nil {
			segEnd = wr[0] // hole up to the next written range
		} else {
			segEnd = end
		}
		if segEnd > end {
			segEnd = end
		}
		if segEnd <= pos {
			break
		}

		if fromSpool {
			n, err := h.spool.ReadAt(p[filled:int64(filled)+(segEnd-pos)], pos)
			filled += n
			if err != nil && !errors.Is(err, io.EOF) {
				return filled, err
			}
		} else {
			if !h.hasRemote || pos >= h.remoteSize {
				if filled > 0 {
					return filled, io.EOF
				}
				return 0, io.EOF
			}
			readEnd := segEnd
			if readEnd > h.remoteSize {
				readEnd = h.remoteSize
			}
			n, err := h.reader.ReadAt(p[filled:int64(filled)+(readEnd-pos)], pos)
			filled += n
			if err != nil && !errors.Is(err, io.EOF) {
				return filled, err
			}
			if filled < len(p) && pos+int64(n) >= h.remoteSize {
				return filled, io.EOF
			}
		}
		pos = segEnd
	}
	if filled < len(p) {
		return filled, io.EOF
	}
	return filled, nil
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

func (h *fileHandle) Write(p []byte) (int, error) {
	h.mu.Lock()
	off := h.pos
	h.mu.Unlock()
	n, err := h.WriteAt(p, off)
	h.mu.Lock()
	h.pos += int64(n)
	h.mu.Unlock()
	return n, err
}

func (h *fileHandle) WriteAt(p []byte, off int64) (int, error) {
	if !h.writeMode {
		return 0, syscall.EPERM
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	n, err := h.spool.WriteAt(p, off)
	if err == nil && n > 0 {
		h.written.add(off, off+int64(n))
		if off+int64(n) > h.localSize {
			h.localSize = off + int64(n)
		}
		h.dirty = true
	}
	return n, err
}

func (h *fileHandle) Truncate(size int64) error {
	if !h.writeMode {
		return syscall.EPERM
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.spool.Truncate(size); err != nil {
		return err
	}
	if size > h.localSize {
		// extension: the extended region must not leak remote bytes
		h.written.add(h.localSize, size)
	}
	h.localSize = size
	h.truncSet, h.truncSize = true, size
	h.dirty = true
	return nil
}

func (h *fileHandle) Sync() error { return nil }

// finalSize computes the size the uploaded file must have.
func (h *fileHandle) finalSize() int64 {
	if h.truncSet {
		return h.truncSize
	}
	size := h.localSize
	if h.hasRemote && h.remoteSize > size {
		size = h.remoteSize
	}
	return size
}

// flushToRemote assembles the merged content and uploads it.
func (h *fileHandle) flushToRemote() error {
	final := h.finalSize()
	var remote io.ReaderAt
	if h.hasRemote {
		remote = h.reader
	}
	rd := overlayReader(h.spool, h.written, remote, h.remoteSize, final)
	w, ok := h.fs.src.(Writer)
	if !ok {
		return syscall.EPERM
	}
	if err := w.Upload(h.fs.ctx, h.path, final, rd); err != nil {
		// Preserve the spool for forensics/recovery; do not lose data.
		fail := h.spoolPath + ".failed"
		_ = os.Rename(h.spoolPath, fail)
		slog.Error("openlistfs: upload failed; spool kept", "path", h.path,
			"spool", fail, "err", err)
		return err
	}
	return nil
}

func (h *fileHandle) Readdir(count int) ([]os.FileInfo, error) { return nil, syscall.ENOTDIR }

// Stat answers from the metadata cached at open time — no network call.
func (h *fileHandle) Stat() (os.FileInfo, error) {
	return fileInfo{name: vpath.Base(h.path), e: h.entry}, nil
}

func (h *fileHandle) Close() error {
	if h.writeMode && h.dirty {
		if err := h.flushToRemote(); err != nil {
			if h.reader != nil {
				h.reader.Close()
			}
			h.spool.Close()
			return err
		}
	}
	if h.spool != nil {
		_ = h.spool.Close()
		_ = os.Remove(h.spoolPath)
	}
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
		if d.fs.listHook != nil {
			files := make([]Entry, 0, len(list))
			for _, e := range list {
				if !e.IsDir {
					files = append(files, e)
				}
			}
			hookPath := d.path
			go d.fs.listHook(hookPath, files)
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
