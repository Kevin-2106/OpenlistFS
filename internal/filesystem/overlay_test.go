package filesystem

import (
	"bytes"
	"io"
	"os"
	"testing"
)

func TestRangesAddMerge(t *testing.T) {
	var rs ranges
	rs.add(10, 20)
	rs.add(30, 40)
	rs.add(20, 30) // bridges both
	if len(rs) != 1 || rs[0] != [2]int64{10, 40} {
		t.Fatalf("bridge merge = %v, want [10,40]", rs)
	}
	rs.add(50, 60) // separate
	rs.add(0, 5)   // before
	if len(rs) != 3 || rs[0] != [2]int64{0, 5} || rs[2] != [2]int64{50, 60} {
		t.Fatalf("sorted add = %v", rs)
	}
	rs.add(45, 55) // overlaps the last
	if len(rs) != 3 || rs[2] != [2]int64{45, 60} {
		t.Fatalf("overlap merge = %v", rs)
	}
}

func TestRangesCoveredAndClip(t *testing.T) {
	var rs ranges
	rs.add(0, 100)
	rs.add(200, 300)
	if !rs.covered(0, 100) || !rs.covered(50, 100) || !rs.covered(200, 300) {
		t.Error("covered ranges reported uncovered")
	}
	if rs.covered(99, 101) || rs.covered(100, 150) || rs.covered(250, 350) {
		t.Error("holes reported covered")
	}
	clipped := rs.clip(150)
	if len(clipped) != 1 || clipped[0] != [2]int64{0, 100} {
		t.Fatalf("clip = %v", clipped)
	}
	clipped = rs.clip(250)
	if len(clipped) != 2 || clipped[1] != [2]int64{200, 250} {
		t.Fatalf("clip partial = %v", clipped)
	}
}

// Overlay reader must splice spool-written ranges and remote holes into the
// exact final content.
func TestOverlayReaderSplice(t *testing.T) {
	remoteContent := bytes.Repeat([]byte("R"), 100)
	remote := bytes.NewReader(remoteContent)

	spool, err := os.CreateTemp("", "overlay-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(spool.Name())
	defer spool.Close()

	// local writes: [10,20) and [90,100)
	if _, err := spool.WriteAt(bytes.Repeat([]byte("W"), 10), 10); err != nil {
		t.Fatal(err)
	}
	if _, err := spool.WriteAt(bytes.Repeat([]byte("w"), 10), 90); err != nil {
		t.Fatal(err)
	}
	var written ranges
	written.add(10, 20)
	written.add(90, 100)

	rd := overlayReader(spool, written, remote, 100, 100)
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte{}, remoteContent[:10]...), bytes.Repeat([]byte("W"), 10)...)
	want = append(want, remoteContent[20:90]...)
	want = append(want, bytes.Repeat([]byte("w"), 10)...)
	if !bytes.Equal(got, want) {
		t.Fatalf("spliced content mismatch:\n got %q\nwant %q", got, want)
	}

	// truncated final size clips the tail
	rd = overlayReader(spool, written, remote, 100, 50)
	got, err = io.ReadAll(rd)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 50 {
		t.Fatalf("truncated overlay len = %d, want 50", len(got))
	}
}
