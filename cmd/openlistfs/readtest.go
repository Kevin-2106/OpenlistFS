package main

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"openlistfs/internal/openlist"
	"openlistfs/internal/rangeio"
	"openlistfs/internal/vpath"
)

// cmdReadtest benchmarks the hot path: resolve link → HTTP Range → discard.
//
// One bare probe reports HTTP status / Content-Range / TTFB; the remaining
// iterations go through rangeio.Reader.ReadAt — the exact code the WinFsp
// mount will call — to measure sustainable throughput.
func cmdReadtest(ctx context.Context, args []string) error {
	// Allow both "readtest <path> --flags" and "readtest --flags <path>":
	// Go's flag package stops at the first positional argument.
	pathArg := ""
	var rest []string
	for _, a := range args {
		if pathArg == "" && !strings.HasPrefix(a, "-") {
			pathArg = a
			continue
		}
		rest = append(rest, a)
	}
	fs := flag.NewFlagSet("readtest", flag.ExitOnError)
	offset := fs.String("offset", "0", "start offset (bytes, accepts KiB/MiB/GiB)")
	length := fs.String("length", "1MiB", "bytes per read (accepts KiB/MiB/GiB)")
	stride := fs.String("stride", "", "distance between successive reads (default: same as length = sequential)")
	count := fs.Int("count", 1, "number of reads")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if pathArg == "" {
		return fmt.Errorf("usage: openlistfs readtest <path> [--offset N] [--length N] [--stride N] [--count N]")
	}
	if *count < 1 {
		return fmt.Errorf("--count must be >= 1")
	}
	off, err := parseSize(*offset)
	if err != nil {
		return fmt.Errorf("--offset: %v", err)
	}
	readLen, err := parseSize(*length)
	if err != nil {
		return fmt.Errorf("--length: %v", err)
	}
	step := readLen
	if *stride != "" {
		step, err = parseSize(*stride)
		if err != nil {
			return fmt.Errorf("--stride: %v", err)
		}
	}

	p := vpath.Clean(pathArg)
	c, err := newClient(ctx)
	if err != nil {
		return err
	}

	tResolve := time.Now()
	link, err := c.ResolveLink(ctx, p)
	if err != nil {
		return fmt.Errorf("resolve link: %w", err)
	}
	entry, err := c.Stat(ctx, p)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	fmt.Printf("path:        %s\n", p)
	fmt.Printf("size:        %d (%s)\n", entry.Size, fmtBytes(entry.Size))
	fmt.Printf("link source: %s (%s)\n", link.Source, time.Since(tResolve).Round(time.Millisecond))
	fmt.Printf("url:         %s\n", openlist.SanitizeURL(link.URL))
	fmt.Printf("read plan:   %d x %s at offset %s, stride %s\n\n",
		*count, fmtBytes(readLen), fmtBytes(off), fmtBytes(step))

	rLink := rangeio.Link{URL: link.URL, Header: link.Header, Size: entry.Size}
	res := rangeio.ProbeRange(ctx, nil, rLink, off, readLen)
	if res.Err != nil {
		return fmt.Errorf("probe: %v", res.Err)
	}
	fmt.Printf("probe:       HTTP %d  Content-Range %q  TTFB %s  %s in %s\n",
		res.Status, res.ContentRange, res.TTFB.Round(time.Millisecond),
		fmtBytes(res.Received), res.Total.Round(time.Millisecond))

	// Throughput via the product path.
	reader := rangeio.New(ctx, rLink,
		rangeio.WithLinkResolver(func(ctx context.Context) (rangeio.Link, error) {
			fresh, err := c.ResolveLink(ctx, p)
			if err != nil {
				return rangeio.Link{}, err
			}
			return rangeio.Link{URL: fresh.URL, Header: fresh.Header, Size: entry.Size}, nil
		}))
	defer reader.Close()

	buf := make([]byte, readLen)
	tStart := time.Now()
	var total int64
	failed := 0
	for i := 0; i < *count; i++ {
		at := off + int64(i)*step
		if at >= entry.Size {
			fmt.Printf("read %2d:     offset %s beyond EOF, stopping\n", i+1, fmtBytes(at))
			break
		}
		t0 := time.Now()
		n, err := reader.ReadAt(buf, at)
		el := time.Since(t0)
		total += int64(n)
		if err != nil && !(i == *count-1) {
			// EOF on the last read is fine (partial tail); anything else fails.
			failed++
			fmt.Printf("read %2d:     offset %13s  %s in %s  err: %v\n",
				i+1, fmtBytes(at), fmtBytes(int64(n)), el.Round(time.Millisecond), err)
			continue
		}
		fmt.Printf("read %2d:     offset %13s  %s in %s  %s/s\n",
			i+1, fmtBytes(at), fmtBytes(int64(n)), el.Round(time.Millisecond),
			fmtBytes(throughput(int64(n), el)))
	}
	el := time.Since(tStart)
	snap := reader.Metrics.Snapshot()

	fmt.Printf("\nsummary:\n")
	fmt.Printf("  read:            %s in %s (%s/s)\n",
		fmtBytes(total), el.Round(time.Millisecond), fmtBytes(throughput(total, el)))
	fmt.Printf("  http requests:   %d (retries %d)\n", snap.Requests, snap.Retries)
	fmt.Printf("  avg TTFB:        %s\n", snap.AvgTTFB().Round(time.Millisecond))
	fmt.Printf("  link resolves:   %d\n", snap.LinkResolves)
	if failed > 0 {
		return fmt.Errorf("%d of %d reads failed", failed, *count)
	}
	return nil
}

func throughput(n int64, d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64(float64(n) / d.Seconds())
}

// parseSize accepts "1073741824", "1KiB", "64MiB", "2GiB" …
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	mult := int64(1)
	body := s
	switch {
	case strings.HasSuffix(s, "KiB"):
		mult, body = 1<<10, strings.TrimSuffix(s, "KiB")
	case strings.HasSuffix(s, "MiB"):
		mult, body = 1<<20, strings.TrimSuffix(s, "MiB")
	case strings.HasSuffix(s, "GiB"):
		mult, body = 1<<30, strings.TrimSuffix(s, "GiB")
	case strings.HasSuffix(s, "K"), strings.HasSuffix(s, "KB"):
		mult, body = 1<<10, strings.TrimRight(strings.TrimSuffix(s, "KB"), "K")
	case strings.HasSuffix(s, "M"), strings.HasSuffix(s, "MB"):
		mult, body = 1<<20, strings.TrimRight(strings.TrimSuffix(s, "MB"), "M")
	case strings.HasSuffix(s, "G"), strings.HasSuffix(s, "GB"):
		mult, body = 1<<30, strings.TrimRight(strings.TrimSuffix(s, "GB"), "G")
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(body), 64)
	if err != nil {
		return 0, fmt.Errorf("cannot parse size %q", s)
	}
	return int64(v * float64(mult)), nil
}
