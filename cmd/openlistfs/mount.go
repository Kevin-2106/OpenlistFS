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
	"openlistfs/internal/readahead"
)

// cmdMount mounts the read-only OpenList view at a drive letter via WinFsp.
// An optional RAM read-ahead layer turns WinFsp's small reads into large
// sequential fetches; --read-ahead 0 disables it (Phase 2 baseline).
func cmdMount(ctx context.Context, args []string) error {
	// Allow both "mount O: --flags" and "mount --flags O:".
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
	window := fs.String("read-ahead", "64MiB", "read-ahead window per open file (0 disables caching)")
	blockSize := fs.String("block-size", "4MiB", "cache block size")
	cacheSize := fs.String("cache-size", "256MiB", "total RAM cache cap")
	concurrency := fs.Int("prefetch-concurrency", 3, "parallel prefetch fetches per file")
	metaTTL := fs.Duration("metadata-ttl", 30*time.Second, "how long directory listings and file metadata are cached")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if drive == "" {
		return fmt.Errorf("usage: openlistfs mount <drive> [--read-ahead N] [--block-size N] [--cache-size N] [--prefetch-concurrency N] [--debug]\n   e.g. openlistfs mount O:")
	}
	mountpoint := drive

	windowBytes, err := parseSize(*window)
	if err != nil {
		return fmt.Errorf("--read-ahead: %v", err)
	}
	blockBytes, err := parseSize(*blockSize)
	if err != nil {
		return fmt.Errorf("--block-size: %v", err)
	}
	cacheBytes, err := parseSize(*cacheSize)
	if err != nil {
		return fmt.Errorf("--cache-size: %v", err)
	}

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
	src := c.Source(openlist.WithSourceMetrics(metrics), openlist.WithMetadataTTL(*metaTTL))

	var cache *readahead.Cache
	if windowBytes > 0 {
		cache = readahead.NewCache(cacheBytes)
	}
	decorate := func(p string, size int64, r filesystem.RangeReader) filesystem.RangeReader {
		if cache == nil {
			return r
		}
		inner, ok := r.(readahead.Inner)
		if !ok {
			slog.Error("readahead: decorator assertion failed", "type", fmt.Sprintf("%T", r))
			return r
		}
		slog.Debug("readahead: wrapping reader", "path", p, "size", size)
		return readahead.NewReader(mctx, p, size, inner, cache, readahead.Config{
			BlockSize:   blockBytes,
			Window:      windowBytes,
			Concurrency: *concurrency,
		})
	}

	fsys := filesystem.New(mctx, src, filesystem.WithDecorator(decorate))
	behaviour, err := gofs.NewOptions(fsys, gofs.WithAttribReadOnlyTransMode(gofs.AttribReadOnlyAlways))
	if err != nil {
		return fmt.Errorf("filesystem options: %w", err)
	}

	mounted, err := winfsp.Mount(behaviour, mountpoint, winfsp.FileSystemName("OpenListFS"))
	if err != nil {
		return fmt.Errorf("mount %s: %w (is WinFsp installed and the drive letter free?)", mountpoint, err)
	}
	mode := fmt.Sprintf("read-ahead %s (blocks %s, cache %s, x%d)",
		fmtBytes(windowBytes), fmtBytes(blockBytes), fmtBytes(cacheBytes), *concurrency)
	if cache == nil {
		mode = "disabled (baseline)"
	}
	fmt.Printf("mounted %s — Ctrl+C to unmount\n", mountpoint)
	fmt.Printf("server:     %s\n", cfg.URL)
	fmt.Printf("read-ahead: %s\n", mode)

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	<-ch

	fmt.Println("\nunmounting…")
	cancel() // stop all in-flight remote reads first
	mounted.Unmount()
	printSessionStats(metrics, cache)
	return nil
}

func printSessionStats(m *rangeio.Metrics, cache *readahead.Cache) {
	s := m.Snapshot()
	fmt.Println("Session statistics:")
	fmt.Printf("  ReadAt calls:      %d\n", s.RangeRequests)
	fmt.Printf("  HTTP requests:     %d (retries %d, link re-resolves %d)\n",
		s.Requests, s.Retries, s.LinkResolves)
	fmt.Printf("  Downloaded:        %s\n", fmtBytes(s.BytesFetched))
	fmt.Printf("  Avg TTFB:          %s\n", s.AvgTTFB().Round(time.Millisecond))
	if cache != nil {
		cm := cache.Metrics
		served := cm.HitBytes.Load() + cm.MissBytes.Load()
		ratio := 0.0
		if served > 0 {
			ratio = float64(cm.HitBytes.Load()) / float64(served) * 100
		}
		fmt.Printf("  Cache served:      %s (%.1f%% hit, %s RAM in use)\n",
			fmtBytes(cm.HitBytes.Load()), ratio, fmtBytes(cache.Used()))
		fmt.Printf("  Foreground fetch:  %s\n", fmtBytes(cm.MissBytes.Load()))
		fmt.Printf("  Prefetch:          %s fetched, %s wasted unread, %d seek cancels\n",
			fmtBytes(cm.PrefetchBytes.Load()), fmtBytes(cm.PrefetchWaste.Load()),
			cm.SeekCancels.Load())
	}
}
