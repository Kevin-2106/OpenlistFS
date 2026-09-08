// Package mounter owns the OpenListFS mount lifecycle: build the client,
// source, cache and filesystem stack from a Config and mount it via WinFsp.
// It is shared by the CLI (cmd/openlistfs) and the GUI.
package mounter

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/winfsp/go-winfsp"
	"github.com/winfsp/go-winfsp/gofs"

	"openlistfs/internal/filesystem"
	"openlistfs/internal/openlist"
	"openlistfs/internal/proxy"
	"openlistfs/internal/rangeio"
	"openlistfs/internal/readahead"
	"openlistfs/internal/vpath"
)

// Config is everything needed to bring up one mount.
type Config struct {
	URL         string
	Token       string
	Username    string
	Password    string
	Mountpoint  string // e.g. "O:"
	Network     bool   // present as a network drive
	Window      int64  // read-ahead window per open file (bytes; 0 disables caching)
	ChunkSize   int64  // fetch/cache granularity
	CacheSize   int64  // total RAM cache cap
	Concurrency int    // background fetch budget
	MetaTTL     time.Duration
	Writable    bool   // enable write support (mutations + overlay uploads)
	Proxy       string // "none" (default), "system", or a proxy URL — see internal/proxy
}

// Session is one live mount.
type Session struct {
	cfg     Config
	client  *openlist.Client
	cache   *readahead.Cache
	metrics *rangeio.Metrics

	mu        sync.Mutex
	mounted   *winfsp.FileSystem
	cancel    context.CancelFunc
	started   time.Time
	unmounted bool
}

// buildClient mirrors the CLI's credential handling. transport may be nil
// (the client's default direct transport is used).
func buildClient(ctx context.Context, cfg Config, transport http.RoundTripper) (*openlist.Client, error) {
	var opts []openlist.Option
	if cfg.Token != "" {
		opts = append(opts, openlist.WithToken(cfg.Token))
	}
	if cfg.Username != "" {
		opts = append(opts, openlist.WithCredentials(cfg.Username, cfg.Password))
	}
	if transport != nil {
		opts = append(opts, openlist.WithTransport(transport))
	}
	c := openlist.New(cfg.URL, opts...)
	if cfg.Token == "" && cfg.Username != "" {
		if err := c.Login(ctx); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Start mounts cfg.Mountpoint. The returned Session keeps serving until
// Unmount; ctx cancels all remote activity when the owner shuts down.
func Start(ctx context.Context, cfg Config) (*Session, error) {
	if cfg.Mountpoint == "" {
		return nil, fmt.Errorf("mounter: empty mountpoint")
	}
	if cfg.Window > 0 && cfg.ChunkSize <= 0 {
		cfg.ChunkSize = 512 << 10
	}
	if cfg.CacheSize <= 0 {
		cfg.CacheSize = 256 << 20
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 3
	}
	if cfg.MetaTTL <= 0 {
		cfg.MetaTTL = 2 * time.Minute
	}

	mctx, cancel := context.WithCancel(ctx)
	proxyFn, proxyDesc, err := proxy.Parse(cfg.Proxy)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("mounter: %w", err)
	}
	// One proxy function, two tuned transports: API calls and direct-URL
	// downloads each keep their own connection pool but share the route.
	c, err := buildClient(mctx, cfg, openlist.NewTransport(proxyFn))
	if err != nil {
		cancel()
		return nil, err
	}
	metrics := &rangeio.Metrics{}
	src := c.Source(openlist.WithSourceMetrics(metrics), openlist.WithMetadataTTL(cfg.MetaTTL),
		openlist.WithSourceTransport(rangeio.NewTransport(proxyFn)))

	var cache *readahead.Cache
	if cfg.Window > 0 {
		cache = readahead.NewCache(cfg.CacheSize, cfg.Concurrency)
	}
	decorate := func(p string, size int64, r filesystem.RangeReader) filesystem.RangeReader {
		if cache == nil {
			return r
		}
		inner, ok := r.(readahead.Inner)
		if !ok {
			return r
		}
		return readahead.NewReader(mctx, p, size, inner, cache, readahead.Config{
			ChunkSize:   cfg.ChunkSize,
			Window:      cfg.Window,
			Concurrency: cfg.Concurrency,
		})
	}

	// Warm-on-list: fetch head+tail chunk of every file after a cold
	// listing, bounded and yielding to foreground reads.
	var warmMu sync.Mutex
	warmedDirs := make(map[string]time.Time)
	warmSem := make(chan struct{}, 2)
	listHook := func(dir string, files []filesystem.Entry) {
		if cache == nil || len(files) == 0 || len(files) > 32 {
			return
		}
		warmMu.Lock()
		if t, seen := warmedDirs[dir]; seen && time.Since(t) < cfg.MetaTTL {
			warmMu.Unlock()
			return
		}
		warmedDirs[dir] = time.Now()
		warmMu.Unlock()
		for _, e := range files {
			if e.Size <= 0 {
				continue
			}
			select {
			case warmSem <- struct{}{}:
			case <-mctx.Done():
				return
			}
			go func(e filesystem.Entry) {
				defer func() { <-warmSem }()
				for cache.ForegroundBusy(4 * time.Second) {
					select {
					case <-mctx.Done():
						return
					case <-time.After(500 * time.Millisecond):
					}
				}
				full := vpath.Join(dir, e.Name)
				_, reader, err := src.Open(mctx, full)
				if err != nil || reader == nil {
					return
				}
				wrapped := decorate(full, e.Size, reader)
				if bg, ok := wrapped.(interface{ SetBackground() }); ok {
					bg.SetBackground()
				}
				head := cfg.ChunkSize
				if e.Size < head {
					head = e.Size
				}
				buf := make([]byte, head)
				wrapped.ReadAt(buf, 0)
				for cache.ForegroundBusy(4 * time.Second) {
					select {
					case <-mctx.Done():
						wrapped.Close()
						return
					case <-time.After(500 * time.Millisecond):
					}
				}
				ci := (e.Size - 1) / cfg.ChunkSize
				tailOff := ci * cfg.ChunkSize
				buf = make([]byte, e.Size-tailOff)
				wrapped.ReadAt(buf, tailOff)
				wrapped.Close()
			}(e)
		}
	}

	fsOpts := []filesystem.Option{
		filesystem.WithDecorator(decorate),
		filesystem.WithListHook(listHook),
		filesystem.WithWritable(cfg.Writable),
	}
	fsys := filesystem.New(mctx, src, fsOpts...)
	behaviour, err := gofs.NewOptions(fsys, gofs.WithAttribReadOnlyTransMode(gofs.AttribReadOnlyAlways))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("mounter: filesystem options: %w", err)
	}

	var opts []winfsp.Option
	opts = append(opts, winfsp.FileSystemName("OpenListFS"))
	if cfg.Network {
		share := cfg.Mountpoint
		share = trimShare(share)
		if share == "" {
			share = "media"
		}
		opts = append(opts, winfsp.VolumePrefix(`\openlistfs\`+share))
	}

	mounted, err := winfsp.Mount(behaviour, cfg.Mountpoint, opts...)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("mounter: mount %s: %w (is WinFsp installed and the drive letter free?)", cfg.Mountpoint, err)
	}

	slog.Info("mounter: mounted", "drive", cfg.Mountpoint, "server", cfg.URL,
		"read-ahead", fmtBytes(cfg.Window), "chunk", fmtBytes(cfg.ChunkSize),
		"cache", fmtBytes(cfg.CacheSize), "workers", cfg.Concurrency, "network", cfg.Network,
		"proxy", proxyDesc)

	return &Session{
		cfg:     cfg,
		client:  c,
		cache:   cache,
		metrics: metrics,
		mounted: mounted,
		cancel:  cancel,
		started: time.Now(),
	}, nil
}

func trimShare(mountpoint string) string {
	for len(mountpoint) > 0 && (mountpoint[0] == '\\' || mountpoint[0] == '/') {
		mountpoint = mountpoint[1:]
	}
	for len(mountpoint) > 0 && mountpoint[len(mountpoint)-1] == ':' {
		mountpoint = mountpoint[:len(mountpoint)-1]
	}
	return mountpoint
}

// Unmount stops the session: cancel remote activity, unmount, release.
// Safe to call more than once.
func (s *Session) Unmount() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unmounted {
		return
	}
	s.unmounted = true
	s.cancel()
	if s.mounted != nil {
		s.mounted.Unmount()
	}
}

// WaitSignal blocks until Ctrl+C, then unmounts (CLI convenience).
func (s *Session) WaitSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	<-ch
	s.Unmount()
}

// Status is a snapshot for display.
type Status struct {
	Mounted       bool
	Mountpoint    string
	Server        string
	UptimeSec     float64
	ReadAtCalls   int64
	HTTPReqs      int64
	Retries       int64
	LinkResolves  int64
	BytesFetched  int64
	AvgTTFBms     float64
	HitBytes      int64
	MissBytes     int64
	HitRatio      float64
	RAMInUse      int64
	PrefetchBytes int64
	PrefetchWaste int64
	SeekCancels   int64
}

// Stats returns the current snapshot.
func (s *Session) Stats() Status {
	s.mu.Lock()
	unmounted := s.unmounted
	s.mu.Unlock()
	st := Status{
		Mounted:    !unmounted,
		Mountpoint: s.cfg.Mountpoint,
		Server:     s.cfg.URL,
		UptimeSec:  time.Since(s.started).Seconds(),
	}
	ms := s.metrics.Snapshot()
	st.ReadAtCalls = ms.RangeRequests
	st.HTTPReqs = ms.Requests
	st.Retries = ms.Retries
	st.LinkResolves = ms.LinkResolves
	st.BytesFetched = ms.BytesFetched
	st.AvgTTFBms = float64(ms.AvgTTFB().Microseconds()) / 1000.0
	if s.cache != nil {
		cm := s.cache.Metrics
		st.HitBytes = cm.HitBytes.Load()
		st.MissBytes = cm.MissBytes.Load()
		served := st.HitBytes + st.MissBytes
		if served > 0 {
			st.HitRatio = float64(st.HitBytes) / float64(served) * 100
		}
		st.RAMInUse = s.cache.Used()
		st.PrefetchBytes = cm.PrefetchBytes.Load()
		st.PrefetchWaste = cm.PrefetchWaste.Load()
		st.SeekCancels = cm.SeekCancels.Load()
	}
	return st
}

func fmtBytes(n int64) string {
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
