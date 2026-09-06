package filesystem

import (
	"io"
	"os"
	"sort"
)

// ranges tracks which byte ranges of an overlay file were written locally.
// Ranges are kept merged and sorted by start; end is exclusive.
type ranges [][2]int64

func (rs *ranges) add(start, end int64) {
	if end <= start {
		return
	}
	merged := make([][2]int64, 0, len(*rs)+1)
	inserted := false
	for _, r := range *rs {
		switch {
		case r[1] < start: // entirely before the new range
			merged = append(merged, r)
		case r[0] > end: // entirely after
			if !inserted {
				merged = append(merged, [2]int64{start, end})
				inserted = true
			}
			merged = append(merged, r)
		default: // overlapping or adjacent: absorb
			if r[0] < start {
				start = r[0]
			}
			if r[1] > end {
				end = r[1]
			}
		}
	}
	if !inserted {
		merged = append(merged, [2]int64{start, end})
	}
	*rs = merged
}

// covered reports whether [start, end) lies entirely within written ranges.
func (rs ranges) covered(start, end int64) bool {
	need := end
	for _, r := range rs {
		if r[1] <= start {
			continue
		}
		if r[0] > start {
			return false // hole before this range
		}
		if r[1] >= need {
			return true
		}
		start = r[1]
		if start >= need {
			return true
		}
	}
	return false
}

// clip returns the written ranges intersecting [0, limit), clipped to it.
func (rs ranges) clip(limit int64) [][2]int64 {
	out := make([][2]int64, 0, len(rs))
	for _, r := range rs {
		s, e := r[0], r[1]
		if s >= limit {
			continue
		}
		if e > limit {
			e = limit
		}
		if e > s {
			out = append(out, [2]int64{s, e})
		}
	}
	return out
}

func sortRanges(rs [][2]int64) { sort.Slice(rs, func(i, j int) bool { return rs[i][0] < rs[j][0] }) }

// overlayReader builds the final merged content of an overlay file: locally
// written ranges come from the spool, holes come from remote (when the file
// exists remotely). The returned reader yields exactly finalSize bytes.
func overlayReader(spool *os.File, written ranges, remote io.ReaderAt, remoteSize, finalSize int64) io.Reader {
	if remote == nil {
		// New file: the spool is the whole content (sparse regions are 0).
		_ = spool.Truncate(finalSize)
		_, _ = spool.Seek(0, io.SeekStart)
		return io.NewSectionReader(spool, 0, finalSize)
	}
	wr := written.clip(finalSize)
	sortRanges(wr)
	readers := make([]io.Reader, 0, 2*len(wr)+1)
	var pos int64
	for _, r := range wr {
		if r[0] > pos {
			readers = append(readers, io.NewSectionReader(remote, pos, r[0]-pos))
		}
		readers = append(readers, io.NewSectionReader(spool, r[0], r[1]-r[0]))
		pos = r[1]
	}
	if pos < finalSize {
		readers = append(readers, io.NewSectionReader(remote, pos, finalSize-pos))
	}
	return io.MultiReader(readers...)
}
