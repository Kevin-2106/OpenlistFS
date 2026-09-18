package main

import (
	"context"
	"sync"
	"time"

	systray "github.com/energye/systray"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"openlistfs/internal/autostart"
	cfgpkg "openlistfs/internal/config"
	"openlistfs/internal/mounter"
	"openlistfs/internal/profiles"
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
	Writable     bool   `json:"writable"`
	Proxy        string `json:"proxy"`   // "none", "system" or a proxy URL
	Backend      string `json:"backend"` // "api" (default), "webdav" or "s3"
}

// StatusView is what the UI polls once a second.
type StatusView struct {
	Mounted    bool    `json:"mounted"`
	Mountpoint string  `json:"mountpoint"`
	Server     string  `json:"server"`
	Error      string  `json:"error"`
	Profile    string  `json:"profile"`
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
	doc     profiles.Document
	opts    MountOptions
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if d, err := profiles.Load(); err == nil {
		a.doc = d
	}
	if a.doc.Options.Drive == "" {
		a.doc.Options = profiles.Options{
			Drive: "O:", Network: true,
			ReadAheadMiB: 64, ChunkKiB: 512, CacheMiB: 256,
			Workers: 3, TTLMinutes: 2, Proxy: "none",
		}
	}
	if a.doc.Options.Proxy == "" {
		a.doc.Options.Proxy = "none"
	}
	if a.doc.Options.Backend == "" {
		a.doc.Options.Backend = "api"
	}
	a.opts = fromStoreOptions(a.doc.Options)

	if automountRequested {
		go func() {
			// Give the tray a moment to appear first.
			time.Sleep(500 * time.Millisecond)
			if msg := a.Mount(a.currentOptions()); msg != "" {
				println("automount:", msg)
			}
		}()
	}

	go systray.Run(a.onTrayReady, nil)
}

// ---- profiles ----

// GetProfiles returns profile names, the active one and automount flag.
func (a *App) GetProfiles() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	names := make([]string, 0, len(a.doc.Profiles))
	for name := range a.doc.Profiles {
		names = append(names, name)
	}
	return map[string]any{
		"names":     names,
		"active":    a.doc.Active,
		"autoMount": a.doc.AutoMount,
	}
}

// GetProfile returns one profile's credentials.
func (a *App) GetProfile(name string) profiles.Profile {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.doc.Profiles[name]
}

// GetActiveConfig returns the credentials the mount would use right now:
// the active profile, or the legacy env/.env configuration when no
// profiles exist yet.
func (a *App) GetActiveConfig() cfgpkg.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p, ok := a.doc.ActiveProfile(); ok {
		return cfgpkg.Config{URL: p.URL, Token: p.Token, Username: p.Username, Password: p.Password}
	}
	cfg, _ := cfgpkg.Load()
	return cfg
}

// SaveProfile creates or updates a profile and makes it active. It also
// refreshes .env so the CLI stays in sync with the active profile.
func (a *App) SaveProfile(name string, p profiles.Profile) string {
	if name == "" {
		return "profile name is empty"
	}
	a.mu.Lock()
	a.doc.Profiles[name] = p
	a.doc.Active = name
	err := profiles.Save(a.doc)
	a.mu.Unlock()
	if err != nil {
		return err.Error()
	}
	_ = cfgpkg.Save(cfgpkg.Config{URL: p.URL, Token: p.Token, Username: p.Username, Password: p.Password})
	return ""
}

// DeleteProfile removes a profile (active moves to another one, if any).
func (a *App) DeleteProfile(name string) string {
	a.mu.Lock()
	delete(a.doc.Profiles, name)
	if a.doc.Active == name {
		a.doc.Active = ""
		for n := range a.doc.Profiles {
			a.doc.Active = n
			break
		}
	}
	err := profiles.Save(a.doc)
	a.mu.Unlock()
	if err != nil {
		return err.Error()
	}
	return ""
}

// SetActiveProfile switches the active profile.
func (a *App) SetActiveProfile(name string) string {
	a.mu.Lock()
	if _, ok := a.doc.Profiles[name]; !ok {
		a.mu.Unlock()
		return "no such profile"
	}
	a.doc.Active = name
	err := profiles.Save(a.doc)
	a.mu.Unlock()
	if err != nil {
		return err.Error()
	}
	return ""
}

// GetOptions returns the last-used mount options.
func (a *App) GetOptions() MountOptions {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opts
}

// SaveOptions persists mount options.
func (a *App) SaveOptions(opts MountOptions) string {
	a.mu.Lock()
	a.opts = opts
	a.doc.Options = toStoreOptions(opts)
	err := profiles.Save(a.doc)
	a.mu.Unlock()
	if err != nil {
		return err.Error()
	}
	return ""
}

// SetAutoMount toggles mounting the active profile at app start.
func (a *App) SetAutoMount(enable bool) string {
	a.mu.Lock()
	a.doc.AutoMount = enable
	err := profiles.Save(a.doc)
	a.mu.Unlock()
	if err != nil {
		return err.Error()
	}
	return ""
}

// GetAutoMount reports the automount flag.
func (a *App) GetAutoMount() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.doc.AutoMount
}

// ---- autostart ----

// GetAutostart reports whether boot auto-start is on.
func (a *App) GetAutostart() bool { return autostart.Enabled() }

// SetAutostart enables/disables boot auto-start.
func (a *App) SetAutostart(enable bool) string {
	if err := autostart.Set(enable); err != nil {
		return err.Error()
	}
	return ""
}

// ---- mount ----

// Mount brings up the drive with the active profile (or the legacy env
// configuration when no profiles exist). Returns an error message or "".
func (a *App) Mount(opts MountOptions) string {
	a.mu.Lock()
	if a.session != nil {
		a.mu.Unlock()
		return "already mounted"
	}
	profile, hasProfile := a.doc.ActiveProfile()
	a.opts = opts
	a.mu.Unlock()

	var url, token, username, password string
	if hasProfile {
		url, token = profile.URL, profile.Token
		username, password = profile.Username, profile.Password
	} else {
		c, err := cfgpkg.Load()
		if err != nil {
			a.setErr(err.Error())
			return err.Error()
		}
		url, token = c.URL, c.Token
		username, password = c.Username, c.Password
	}

	s, err := mounter.Start(a.ctx, mounter.Config{
		URL:         url,
		Token:       token,
		Username:    username,
		Password:    password,
		Mountpoint:  opts.Drive,
		Network:     opts.Network,
		Window:      int64(opts.ReadAheadMiB) << 20,
		ChunkSize:   int64(opts.ChunkKiB) << 10,
		CacheSize:   int64(opts.CacheMiB) << 20,
		Concurrency: opts.Workers,
		MetaTTL:     time.Duration(opts.TTLMinutes) * time.Minute,
		Writable:    opts.Writable,
		Proxy:       opts.Proxy,
		Backend:     opts.Backend,
	})
	if err != nil {
		a.setErr(err.Error())
		return err.Error()
	}
	a.mu.Lock()
	a.session = s
	a.err = ""
	profileName := a.doc.Active
	a.mu.Unlock()

	go a.saveOptionsQuietly(opts)
	go a.syncActiveToEnv(profileName)
	return ""
}

func toStoreOptions(o MountOptions) profiles.Options {
	return profiles.Options{
		Drive: o.Drive, Network: o.Network,
		ReadAheadMiB: o.ReadAheadMiB, ChunkKiB: o.ChunkKiB,
		CacheMiB: o.CacheMiB, Workers: o.Workers, TTLMinutes: o.TTLMinutes,
		Proxy: o.Proxy, Backend: o.Backend,
	}
}

func fromStoreOptions(o profiles.Options) MountOptions {
	return MountOptions{
		Drive: o.Drive, Network: o.Network,
		ReadAheadMiB: o.ReadAheadMiB, ChunkKiB: o.ChunkKiB,
		CacheMiB: o.CacheMiB, Workers: o.Workers, TTLMinutes: o.TTLMinutes,
		Proxy: o.Proxy, Backend: o.Backend,
	}
}

func (a *App) saveOptionsQuietly(opts MountOptions) {
	a.mu.Lock()
	a.doc.Options = toStoreOptions(opts)
	err := profiles.Save(a.doc)
	a.mu.Unlock()
	_ = err
}

func (a *App) syncActiveToEnv(name string) {
	a.mu.Lock()
	p, ok := a.doc.Profiles[name]
	a.mu.Unlock()
	if !ok {
		return
	}
	_ = cfgpkg.Save(cfgpkg.Config{URL: p.URL, Token: p.Token, Username: p.Username, Password: p.Password})
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
	profile := a.doc.Active
	a.mu.Unlock()
	v := StatusView{Error: lastErr, Profile: profile}
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

func (a *App) setErr(msg string) {
	a.mu.Lock()
	a.err = msg
	a.mu.Unlock()
}

// beforeClose hides the window instead of quitting (tray keeps running).
// A real quit comes from the tray menu, which unmounts first.
func (a *App) beforeClose(ctx context.Context) bool {
	runtime.WindowHide(ctx)
	return true
}

// ---- tray ----

func (a *App) onTrayReady() {
	if icon, err := assets.ReadFile("assets/icon.ico"); err == nil {
		systray.SetIcon(icon)
	}
	systray.SetTooltip("OpenListFS (read-only PoC)")

	mShow := systray.AddMenuItem("显示窗口", "Show the OpenListFS window")
	mToggle := systray.AddMenuItem("挂载", "Mount / Unmount the drive")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出 (先卸载)", "Unmount and quit")

	mShow.Click(func() { runtime.WindowShow(a.ctx) })
	mToggle.Click(func() {
		a.mu.Lock()
		mounted := a.session != nil
		a.mu.Unlock()
		if mounted {
			a.Unmount()
		} else if msg := a.Mount(a.currentOptions()); msg != "" {
			runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
				Type:    runtime.ErrorDialog,
				Title:   "Mount failed",
				Message: msg,
				Buttons: []string{"OK"},
			})
		}
	})
	mQuit.Click(func() {
		a.Unmount()
		systray.Quit()
	})

	// Keep the toggle label in sync with the session.
	go func() {
		for {
			time.Sleep(time.Second)
			a.mu.Lock()
			mounted := a.session != nil
			a.mu.Unlock()
			if mounted {
				mToggle.SetTitle("卸载")
			} else {
				mToggle.SetTitle("挂载")
			}
		}
	}()
}

func (a *App) currentOptions() MountOptions {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opts
}
