package main

import (
	"context"
	"sync"
	"time"

	cfgpkg "openlistfs/internal/config"
	"openlistfs/internal/mounter"
)

// MountOptions are the tunables the UI exposes.
type MountOptions struct {
	Drive        string `json:"drive"`
	Network      bool   `json:"network"`
	ReadAheadMiB int    `json:"readAheadMiB"`
	ChunkKiB     int    `json:"chunkKiB"`
	CacheMiB     int    `json:"cacheMiB"`
	Workers      int    `json:"workers"`
	TTLMinutes   int    `json:"ttlMinutes"`
}

// StatusView is what the UI polls once a second.
type StatusView struct {
	Mounted    bool    `json:"mounted"`
	Mountpoint string  `json:"mountpoint"`
	Server     string  `json:"server"`
	Error      string  `json:"error"`
	UptimeSec  float64 `json:"uptimeSec"`

	ReadAtCalls  int64   `json:"readAtCalls"`
	HTTPReqs     int64   `json:"httpReqs"`
	Retries      int64   `json:"retries"`
	BytesFetched int64   `json:"bytesFetched"`
	AvgTTFBms    float64 `json:"avgTtfbMs"`

	HitBytes      int64   `json:"hitBytes"`
	MissBytes     int64   `json:"missBytes"`
	HitRatio      float64 `json:"hitRatio"`
	RAMInUse      int64   `json:"ramInUse"`
	PrefetchBytes int64   `json:"prefetchBytes"`
	PrefetchWaste int64   `json:"prefetchWaste"`
	SeekCancels   int64   `json:"seekCancels"`
}

// App is the Wails-bound backend.
type App struct {
	ctx context.Context

	mu      sync.Mutex
	session *mounter.Session
	err     string
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// GetConfig returns the saved credentials (from env / .env).
func (a *App) GetConfig() cfgpkg.Config {
	cfg, _ := cfgpkg.Load()
	return cfg
}

// SaveConfig persists credentials to .env. Returns an error message or "".
func (a *App) SaveConfig(cfg cfgpkg.Config) string {
	if err := cfgpkg.Save(cfg); err != nil {
		return err.Error()
	}
	return ""
}

// Mount brings up the drive. Returns an error message or "".
func (a *App) Mount(opts MountOptions) string {
	a.mu.Lock()
	if a.session != nil {
		a.mu.Unlock()
		return "already mounted"
	}
	a.mu.Unlock()

	cfg, err := cfgpkg.Load()
	if err != nil {
		return err.Error()
	}
	s, err := mounter.Start(a.ctx, mounter.Config{
		URL:         cfg.URL,
		Token:       cfg.Token,
		Username:    cfg.Username,
		Password:    cfg.Password,
		Mountpoint:  opts.Drive,
		Network:     opts.Network,
		Window:      int64(opts.ReadAheadMiB) << 20,
		ChunkSize:   int64(opts.ChunkKiB) << 10,
		CacheSize:   int64(opts.CacheMiB) << 20,
		Concurrency: opts.Workers,
		MetaTTL:     time.Duration(opts.TTLMinutes) * time.Minute,
	})
	if err != nil {
		a.mu.Lock()
		a.err = err.Error()
		a.mu.Unlock()
		return err.Error()
	}
	a.mu.Lock()
	a.session = s
	a.err = ""
	a.mu.Unlock()
	return ""
}

// Unmount tears the drive down. Returns an error message or "".
func (a *App) Unmount() string {
	a.mu.Lock()
	s := a.session
	a.session = nil
	a.mu.Unlock()
	if s == nil {
		return ""
	}
	s.Unmount()
	return ""
}

// GetStatus polls the live session and counters.
func (a *App) GetStatus() StatusView {
	a.mu.Lock()
	s := a.session
	lastErr := a.err
	a.mu.Unlock()
	v := StatusView{Error: lastErr}
	if s != nil {
		st := s.Stats()
		v.Mounted = st.Mounted
		v.Mountpoint = st.Mountpoint
		v.Server = st.Server
		v.UptimeSec = st.UptimeSec
		v.ReadAtCalls = st.ReadAtCalls
		v.HTTPReqs = st.HTTPReqs
		v.Retries = st.Retries
		v.BytesFetched = st.BytesFetched
		v.AvgTTFBms = st.AvgTTFBms
		v.HitBytes = st.HitBytes
		v.MissBytes = st.MissBytes
		v.HitRatio = st.HitRatio
		v.RAMInUse = st.RAMInUse
		v.PrefetchBytes = st.PrefetchBytes
		v.PrefetchWaste = st.PrefetchWaste
		v.SeekCancels = st.SeekCancels
	}
	return v
}
