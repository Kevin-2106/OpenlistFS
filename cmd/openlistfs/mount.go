package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"openlistfs/internal/mounter"
	"openlistfs/internal/proxy"
)

// cmdMount mounts the read-only OpenList view at a drive letter via WinFsp
// using the shared mounter package. `openlistfs-gui` uses the same package.
func cmdMount(ctx context.Context, args []string) error {
	// Allow both "mount O: --flags" and "mount --flags O:".
	drive := ""
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		drive, rest = args[0], args[1:]
	}
	fs := flag.NewFlagSet("mount", flag.ExitOnError)
	debug := fs.Bool("debug", false, "verbose per-request trace")
	window := fs.String("read-ahead", "64MiB", "read-ahead window per open file (0 disables caching)")
	chunkSize := fs.String("chunk-size", "512KiB", "fetch/cache chunk granularity")
	cacheSize := fs.String("cache-size", "256MiB", "total RAM cache cap")
	concurrency := fs.Int("prefetch-concurrency", 3, "background fetch budget")
	network := fs.Bool("network-mode", true, "mount as a network drive (RaiDrive-style; Explorer treats it as a remote share)")
	writable := fs.Bool("writable", false, "enable write support (local spool, upload on close; needs server write permissions)")
	webdavMode := fs.Bool("webdav", false, "speak WebDAV instead of the OpenList native API (server URL gets /dav appended when it has no path; uses username/password)")
	s3Mode := fs.Bool("s3", false, "mount one S3 bucket instead of the OpenList native API (server URL = https://endpoint/bucket; username/password = access key/secret; region defaults to us-east-1, override with ?region=)")
	proxySetting := fs.String("proxy", "", "proxy for all server traffic: \"none\", \"system\", or a URL like http://127.0.0.1:7897 / socks5://host:port (empty = OPENLISTFS_PROXY, then none)")
	ipFamily := fs.String("ip-family", "", "local TCP family: auto, ipv4 or ipv6 (empty = OPENLISTFS_IP_FAMILY)")
	metaTTL := fs.Duration("metadata-ttl", 2*time.Minute, "how long directory listings and file metadata are cached")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if drive == "" && fs.NArg() > 0 {
		drive = fs.Arg(0)
	} else if drive != "" && fs.NArg() > 0 {
		return fmt.Errorf("unexpected mount arguments: %v", fs.Args())
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("mount expects one drive letter")
	}
	if drive == "" {
		return fmt.Errorf("usage: openlistfs mount <drive> [--read-ahead N] [--chunk-size N] [--cache-size N] [--prefetch-concurrency N] [--network-mode=false] [--writable] [--webdav] [--s3] [--proxy none|system|URL] [--ip-family auto|ipv4|ipv6] [--debug]\n   e.g. openlistfs mount O:")
	}
	mountpoint := drive

	windowBytes, err := parseSize(*window)
	if err != nil {
		return fmt.Errorf("--read-ahead: %v", err)
	}
	chunkBytes, err := parseSize(*chunkSize)
	if err != nil {
		return fmt.Errorf("--chunk-size: %v", err)
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

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if *proxySetting != "" {
		cfg.Proxy = *proxySetting
	}
	if *ipFamily != "" {
		cfg.IPFamily = *ipFamily
	}
	backend := ""
	if *webdavMode {
		backend = "webdav"
	}
	if *s3Mode {
		backend = "s3"
	}
	session, err := mounter.Start(ctx, mounter.Config{
		URL:         cfg.URL,
		Token:       cfg.Token,
		Username:    cfg.Username,
		Password:    cfg.Password,
		Mountpoint:  mountpoint,
		Network:     *network,
		Window:      windowBytes,
		ChunkSize:   chunkBytes,
		CacheSize:   cacheBytes,
		Concurrency: *concurrency,
		MetaTTL:     *metaTTL,
		Writable:    *writable,
		Proxy:       cfg.Proxy,
		Backend:     backend,
		IPFamily:    cfg.IPFamily,
	})
	if err != nil {
		return err
	}
	mode := "disabled (baseline)"
	if windowBytes > 0 {
		mode = fmt.Sprintf("read-ahead %s (chunks %s, cache %s, x%d, network-mode %v)",
			fmtBytes2(windowBytes), fmtBytes2(chunkBytes), fmtBytes2(cacheBytes), *concurrency, *network)
	}
	fmt.Printf("mounted %s — Ctrl+C to unmount\n", mountpoint)
	fmt.Printf("server:     %s\n", cfg.URL)
	if *webdavMode {
		fmt.Printf("backend:    webdav (/dav appended to the server URL when it has no path)\n")
	}
	if *s3Mode {
		fmt.Printf("backend:    s3 (bucket from the URL path; SigV4 with username/password as access key/secret)\n")
	}
	if _, desc, err := proxy.Parse(cfg.Proxy); err == nil && desc != "direct" {
		fmt.Printf("proxy:      %s\n", desc)
	}
	fmt.Printf("read-ahead: %s\n", mode)

	session.WaitSignal()

	fmt.Println("\nunmounting…")
	session.Unmount()
	printStats(session.Stats())
	return nil
}

func printStats(st mounter.Status) {
	fmt.Println("Session statistics:")
	fmt.Printf("  ReadAt calls:      %d\n", st.ReadAtCalls)
	fmt.Printf("  HTTP requests:     %d (retries %d, link re-resolves %d)\n",
		st.HTTPReqs, st.Retries, st.LinkResolves)
	fmt.Printf("  Downloaded:        %s\n", fmtBytes2(st.BytesFetched))
	fmt.Printf("  Avg TTFB:          %.0f ms\n", st.AvgTTFBms)
	if st.HitBytes+st.MissBytes > 0 {
		fmt.Printf("  Cache served:      %s (%.1f%% hit, %s RAM in use)\n",
			fmtBytes2(st.HitBytes), st.HitRatio, fmtBytes2(st.RAMInUse))
		fmt.Printf("  Foreground fetch:  %s\n", fmtBytes2(st.MissBytes))
		fmt.Printf("  Prefetch:          %s fetched, %s wasted unread, %d seek cancels\n",
			fmtBytes2(st.PrefetchBytes), fmtBytes2(st.PrefetchWaste), st.SeekCancels)
	}
}

func fmtBytes2(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
