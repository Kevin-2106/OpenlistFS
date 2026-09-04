package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/winfsp/go-winfsp"
	"github.com/winfsp/go-winfsp/gofs"

	"openlistfs/internal/filesystem"
	"openlistfs/internal/openlist"
	"openlistfs/internal/rangeio"
)

// cmdMount mounts the read-only OpenList view at a drive letter via WinFsp.
// Phase 2 baseline: no cache, no read-ahead — one HTTP Range per WinFsp read.
func cmdMount(ctx context.Context, args []string) error {
	// Allow both "mount O: --debug" and "mount --debug O:".
	drive := ""
	var rest []string
	for _, a := range args {
		if drive == "" && !strings.HasPrefix(a, "-") {
			drive = a
			continue
		}
		rest = append(rest, a)
	}
	fs := flag.NewFlagSet("mount", flag.ExitOnError)
	debug := fs.Bool("debug", false, "verbose per-request trace")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if drive == "" {
		return fmt.Errorf("usage: openlistfs mount <drive> [--debug]\n   e.g. openlistfs mount O:")
	}
	mountpoint := drive

	level := slog.LevelWarn
	if *debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	mctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	c, err := buildClient(mctx, cfg)
	if err != nil {
		return err
	}
	metrics := &rangeio.Metrics{}
	src := c.Source(openlist.WithSourceMetrics(metrics))
	fsys := filesystem.New(mctx, src)
	behaviour, err := gofs.NewOptions(fsys, gofs.WithAttribReadOnlyTransMode(gofs.AttribReadOnlyAlways))
	if err != nil {
		return fmt.Errorf("filesystem options: %w", err)
	}

	mounted, err := winfsp.Mount(behaviour, mountpoint, winfsp.FileSystemName("OpenListFS"))
	if err != nil {
		return fmt.Errorf("mount %s: %w (is WinFsp installed and the drive letter free?)", mountpoint, err)
	}
	fmt.Printf("mounted %s — Ctrl+C to unmount\n", mountpoint)
	fmt.Printf("server:  %s\n", cfg.URL)

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	<-ch

	fmt.Println("\nunmounting…")
	cancel() // stop all in-flight remote reads first
	mounted.Unmount()
	printSessionStats(metrics.Snapshot())
	return nil
}

func printSessionStats(s rangeio.Snapshot) {
	fmt.Println("Session statistics:")
	fmt.Printf("  ReadAt calls:    %d\n", s.RangeRequests)
	fmt.Printf("  HTTP requests:   %d (retries %d, link re-resolves %d)\n",
		s.Requests, s.Retries, s.LinkResolves)
	fmt.Printf("  Downloaded:      %s\n", fmtBytes(s.BytesFetched))
	fmt.Printf("  Avg TTFB:        %s\n", s.AvgTTFB().Round(time.Millisecond))
}
