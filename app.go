package main

import (
	"context"
	"maps"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"time"

	systray "github.com/energye/systray"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"openlistfs/internal/autostart"
	cfgpkg "openlistfs/internal/config"
	"openlistfs/internal/mounter"
	"openlistfs/internal/netpolicy"
	"openlistfs/internal/profiles"
	"openlistfs/internal/winfspcheck"
	"openlistfs/internal/winfspinstall"
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
	Proxy        string `json:"proxy"` // "none", "system" or a proxy URL
	IPFamily     string `json:"ipFamily,omitempty"`
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

	mu                sync.Mutex
	teardownMu        sync.Mutex             // serialize unmount/delete without holding mu across driver calls
	sessions          map[string]*mountEntry // one per drive letter (key: driveKey)
	err               string
	doc               profiles.Document
	opts              MountOptions
	maintenanceActive bool
	mountsInProgress  int
	quitting          bool
}

// mountEntry is one live mount plus the profile it came from.
type mountEntry struct {
	session interface {
		Unmount()
		Stats() mounter.Status
	}
	profile string
}

func NewApp() *App {
	return &App{sessions: make(map[string]*mountEntry)}
}

// driveKey normalizes a drive letter into the session map key ("o: " → "O:").
func driveKey(drive string) string {
	return strings.ToUpper(strings.TrimSpace(drive))
}

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

	if automountRequested || a.doc.AutoMount {
		go func() {
			// Give the tray a moment to appear first.
			time.Sleep(500 * time.Millisecond)
			if msg := a.Mount(a.currentOptions()); msg != "" {
				println("automount:", msg)
			}
		}()
	}

	go func() {
		// Keep HWND creation and its message pump on the same Windows thread.
		goruntime.LockOSThread()
		defer goruntime.UnlockOSThread()
		systray.Run(a.onTrayReady, nil)
	}()
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
	sort.Strings(names)
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
		return profileEnvConfig(p)
	}
	cfg, _ := cfgpkg.Load()
	return cfg
}

// SaveProfile creates or updates a profile and makes it active. It also
// refreshes .env so the CLI stays in sync with the active profile.
func (a *App) SaveProfile(name string, p profiles.Profile) string {
	if p.Options != nil {
		if err := netpolicy.Validate(p.Options.IPFamily); err != nil {
			return err.Error()
		}
	}
	if name == "" {
		return "profile name is empty"
	}
	a.mu.Lock()
	if a.doc.Profiles == nil {
		a.doc.Profiles = make(map[string]profiles.Profile)
	}
	a.doc.Profiles[name] = p
	a.doc.Active = name
	if p.Options != nil {
		a.doc.Options = *p.Options
		a.opts = fromStoreOptions(*p.Options)
	}
	err := profiles.Save(a.doc)
	a.mu.Unlock()
	if err != nil {
		return err.Error()
	}
	_ = cfgpkg.Save(profileEnvConfig(p))
	return ""
}

// DeleteProfile unmounts every drive belonging to the profile before deleting it.
func (a *App) DeleteProfile(name string) string {
	return a.deleteProfile(name, profiles.Save)
}

func (a *App) deleteProfile(name string, save func(profiles.Document) error) string {
	a.teardownMu.Lock()
	defer a.teardownMu.Unlock()
	a.mu.Lock()
	if a.maintenanceActive || a.quitting || a.mountsInProgress != 0 {
		a.mu.Unlock()
		return "正在挂载、维护或退出，请等待当前操作完成后再删除连接。"
	}
	if _, ok := a.doc.Profiles[name]; !ok {
		a.mu.Unlock()
		return "连接不存在，请刷新后重试。"
	}
	a.maintenanceActive = true
	entries := make(map[string]*mountEntry)
	for key, me := range a.sessions {
		if me.profile == name {
			entries[key] = me
		}
	}
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.maintenanceActive = false; a.mu.Unlock() }()
	// Keep the profile and session records until synchronous teardown completes.
	for _, me := range entries {
		me.session.Unmount()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, me := range entries {
		if a.sessions[key] == me {
			delete(a.sessions, key)
		}
	}
	next := a.doc
	next.Profiles = maps.Clone(a.doc.Profiles)
	delete(next.Profiles, name)
	if next.Active == name {
		next.Active = ""
		names := make([]string, 0, len(next.Profiles))
		for n := range next.Profiles {
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) > 0 {
			next.Active = names[0]
			if p := next.Profiles[next.Active]; p.Options != nil {
				next.Options = *p.Options
			}
		}
	}
	if err := save(next); err != nil {
		return err.Error()
	}
	a.doc = next
	a.opts = fromStoreOptions(next.Options)
	return ""
}

// SetActiveProfile switches the active profile.
func (a *App) SetActiveProfile(name string) string {
	a.mu.Lock()
	p, ok := a.doc.Profiles[name]
	if !ok {
		a.mu.Unlock()
		return "no such profile"
	}
	a.doc.Active = name
	if p.Options != nil {
		a.doc.Options = *p.Options
		a.opts = fromStoreOptions(*p.Options)
	}
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
	if err := netpolicy.Validate(opts.IPFamily); err != nil {
		return err.Error()
	}
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

// GetWinFspStatus checks the shared Windows driver before mounting.
func (a *App) GetWinFspStatus() winfspcheck.Status { return winfspcheck.Check() }

// GetWinFspInstallerStatus reports whether the verified bundled installer is available.
func (a *App) GetWinFspInstallerStatus() winfspinstall.InstallerStatus {
	return winfspinstall.Check()
}

// InstallWinFsp opens the official installer only after an explicit UI action.
// Windows Installer handles elevation and its own install/repair confirmation.
func (a *App) InstallWinFsp() string {
	a.mu.Lock()
	if len(a.sessions) != 0 || a.mountsInProgress != 0 {
		a.mu.Unlock()
		return "请先卸载所有盘符，再安装或修复 WinFsp。"
	}
	if a.maintenanceActive || a.quitting {
		a.mu.Unlock()
		return "正在安装或退出，请等待当前操作完成。"
	}
	a.maintenanceActive = true
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.maintenanceActive = false; a.mu.Unlock() }()
	if err := winfspinstall.Launch(); err != nil {
		return err.Error()
	}
	return ""
}

func (a *App) OpenWinFspDownload() {
	runtime.BrowserOpenURL(a.ctx, "https://winfsp.dev/rel/")
}

func (a *App) OpenWinFspProject() {
	runtime.BrowserOpenURL(a.ctx, "https://github.com/winfsp/winfsp")
}

// ---- mount ----

// Mount brings up one more drive with the active profile (or the legacy
// env configuration when no profiles exist). Sessions are per drive
// letter: mounting at a fresh letter adds another drive alongside the
// running ones. Returns an error message or "".
func (a *App) Mount(opts MountOptions) string {
	key := driveKey(opts.Drive)
	if key == "" {
		return "drive letter is empty"
	}
	a.mu.Lock()
	if a.maintenanceActive || a.quitting {
		a.mu.Unlock()
		return "正在安装或退出，暂时不能挂载。"
	}
	a.mountsInProgress++
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.mountsInProgress--; a.mu.Unlock() }()
	if err := winfspcheck.Require(); err != nil {
		a.setErr(err.Error())
		return err.Error()
	}
	a.mu.Lock()
	if _, busy := a.sessions[key]; busy {
		a.mu.Unlock()
		return key + " is already mounted"
	}
	profile, hasProfile := a.doc.ActiveProfile()
	profileName := a.doc.Active
	a.opts = opts
	a.mu.Unlock()

	var url, token, username, password string
	if hasProfile {
		url, token = profile.URL, profile.Token
		username, password = profile.Username, profile.Password
	} else {
		profileName = ""
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
		IPFamily:    opts.IPFamily,
	})
	if err != nil {
		a.setErr(err.Error())
		return err.Error()
	}
	a.mu.Lock()
	if a.quitting {
		a.mu.Unlock()
		s.Unmount()
		return "应用正在退出。"
	}
	if _, busy := a.sessions[key]; busy { // lost a race with another Mount
		a.mu.Unlock()
		s.Unmount()
		return key + " is already mounted"
	}
	a.sessions[key] = &mountEntry{session: s, profile: profileName}
	a.err = ""
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
		Proxy: o.Proxy, Backend: o.Backend, Writable: o.Writable, IPFamily: o.IPFamily,
	}
}

func fromStoreOptions(o profiles.Options) MountOptions {
	return MountOptions{
		Drive: o.Drive, Network: o.Network,
		ReadAheadMiB: o.ReadAheadMiB, ChunkKiB: o.ChunkKiB,
		CacheMiB: o.CacheMiB, Workers: o.Workers, TTLMinutes: o.TTLMinutes,
		Proxy: o.Proxy, Backend: o.Backend, Writable: o.Writable, IPFamily: o.IPFamily,
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
	_ = cfgpkg.Save(profileEnvConfig(p))
}

// Unmount tears down the drive mounted at mountpoint. Returns an error
// message or "".
func (a *App) Unmount(mountpoint string) string {
	a.teardownMu.Lock()
	defer a.teardownMu.Unlock()
	key := driveKey(mountpoint)
	a.mu.Lock()
	me := a.sessions[key]
	delete(a.sessions, key)
	a.mu.Unlock()
	if me != nil {
		me.session.Unmount()
	}
	return ""
}

// UnmountAll tears down every mounted drive (tray quit, beforeClose is
// hide-only so it is not used there).
func (a *App) UnmountAll() {
	a.teardownMu.Lock()
	defer a.teardownMu.Unlock()
	a.mu.Lock()
	entries := make([]*mountEntry, 0, len(a.sessions))
	for _, me := range a.sessions {
		entries = append(entries, me)
	}
	a.sessions = make(map[string]*mountEntry)
	a.mu.Unlock()
	for _, me := range entries {
		me.session.Unmount()
	}
}

// StatusesView is what the UI polls once a second: the last mount error
// plus one StatusView per mounted drive.
type StatusesView struct {
	Error    string       `json:"error"`
	Sessions []StatusView `json:"sessions"`
}

// GetStatuses polls every live session and their counters.
func (a *App) GetStatuses() StatusesView {
	a.mu.Lock()
	lastErr := a.err
	entries := make([]*mountEntry, 0, len(a.sessions))
	for _, me := range a.sessions {
		entries = append(entries, me)
	}
	a.mu.Unlock()
	out := StatusesView{Error: lastErr, Sessions: []StatusView{}}
	for _, me := range entries {
		st := me.session.Stats()
		v := StatusView{
			Mounted:    st.Mounted,
			Mountpoint: st.Mountpoint,
			Server:     st.Server,
			Profile:    me.profile,
			UptimeSec:  st.UptimeSec,
		}
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
		out.Sessions = append(out.Sessions, v)
	}
	sort.Slice(out.Sessions, func(i, j int) bool {
		return out.Sessions[i].Mountpoint < out.Sessions[j].Mountpoint
	})
	return out
}

func (a *App) setErr(msg string) {
	a.mu.Lock()
	a.err = msg
	a.mu.Unlock()
}

// beforeClose hides the window instead of quitting (tray keeps running).
// A real quit comes from the tray menu, which unmounts first.
func (a *App) beforeClose(ctx context.Context) bool {
	a.mu.Lock()
	quitting := a.quitting
	a.mu.Unlock()
	if quitting {
		return false
	}
	runtime.WindowHide(ctx)
	return true
}

func (a *App) quit() {
	a.mu.Lock()
	a.quitting = true
	a.mu.Unlock()
	a.UnmountAll()
	runtime.Quit(a.ctx)
}

func (a *App) shutdown(ctx context.Context) {
	a.mu.Lock()
	a.quitting = true
	a.mu.Unlock()
	a.UnmountAll()
	systray.Quit()
}

// ---- tray ----

func (a *App) onTrayReady() {
	systray.SetIcon(trayIcon)
	systray.SetOnDClick(func(_ systray.IMenu) { go a.showWindow() })
	systray.SetTooltip("OpenListFS")

	mShow := systray.AddMenuItem("显示窗口", "Show the OpenListFS window")
	mToggle := systray.AddMenuItem("挂载", "Mount / unmount the configured drive")
	mAll := systray.AddMenuItem("卸载全部", "Unmount every drive")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出 (先卸载)", "Unmount all and quit")

	mShow.Click(func() { go a.showWindow() })
	mToggle.Click(func() { go a.trayToggle() })
	mAll.Click(func() { go a.UnmountAll() })
	mQuit.Click(func() { go a.quit() })

	// Keep the toggle label in sync with the configured drive's session.
	go func() {
		lastTitle := "挂载"
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-ticker.C:
			}
			a.mu.Lock()
			key := driveKey(a.opts.Drive)
			_, mounted := a.sessions[key]
			count := len(a.sessions)
			a.mu.Unlock()
			title := ""
			switch {
			case count == 0:
				title = "挂载"
			case mounted:
				title = "卸载 " + key
			default:
				title = "挂载 " + key
			}
			if title != lastTitle {
				mToggle.SetTitle(title)
				lastTitle = title
			}
		}
	}()
}

func (a *App) showWindow() {
	runtime.WindowShow(a.ctx)
	// Raise the existing window even when Windows restricts focus transfer
	// from a background launcher. Do not leave a permanent topmost window.
	runtime.WindowSetAlwaysOnTop(a.ctx, true)
	runtime.WindowSetAlwaysOnTop(a.ctx, false)
}

// trayToggle mounts or unmounts the drive the current options point at.
func (a *App) trayToggle() {
	a.mu.Lock()
	key := driveKey(a.opts.Drive)
	_, mounted := a.sessions[key]
	opts := a.opts
	a.mu.Unlock()
	if key == "" {
		runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
			Type:    runtime.ErrorDialog,
			Title:   "Mount failed",
			Message: "no drive letter configured",
			Buttons: []string{"OK"},
		})
		return
	}
	if mounted {
		a.Unmount(key)
		return
	}
	if msg := a.Mount(opts); msg != "" {
		runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
			Type:    runtime.ErrorDialog,
			Title:   "Mount failed",
			Message: msg,
			Buttons: []string{"OK"},
		})
	}
}

func (a *App) currentOptions() MountOptions {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opts
}

// profileEnvConfig preserves routing when a saved GUI profile is used by the CLI.
func profileEnvConfig(p profiles.Profile) cfgpkg.Config {
	c := cfgpkg.Config{URL: p.URL, Token: p.Token, Username: p.Username, Password: p.Password}
	if p.Options != nil {
		c.Proxy = p.Options.Proxy
		c.IPFamily = p.Options.IPFamily
	}
	return c
}
