//go:build windows

package main

import (
	"errors"
	"strings"
	"testing"

	"openlistfs/internal/mounter"
	"openlistfs/internal/profiles"
)

type deletionSession struct{ unmount func() }

func (s deletionSession) Unmount()              { s.unmount() }
func (s deletionSession) Stats() mounter.Status { return mounter.Status{} }

func TestDeleteProfileUnmountsAllOwnDrivesBeforeSaving(t *testing.T) {
	a := NewApp()
	a.doc.Profiles = map[string]profiles.Profile{"delete": {}, "keep": {Options: &profiles.Options{Drive: "Q:"}}}
	a.doc.Active = "delete"
	count := 0
	for _, key := range []string{"O:", "P:"} {
		a.sessions[key] = &mountEntry{profile: "delete", session: deletionSession{unmount: func() {
			count++
			if _, ok := a.GetProfiles()["active"]; !ok {
				t.Fatal("cannot poll during teardown")
			}
			if _, ok := a.doc.Profiles["delete"]; !ok {
				t.Fatal("profile removed before unmount")
			}
			if msg := a.Mount(MountOptions{Drive: "R:"}); !strings.Contains(msg, "暂时不能挂载") {
				t.Fatal(msg)
			}
		}}}
	}
	a.sessions["Q:"] = &mountEntry{profile: "keep", session: deletionSession{unmount: func() { t.Fatal("unmounted other profile") }}}
	msg := a.deleteProfile("delete", func(next profiles.Document) error {
		if count != 2 {
			t.Fatalf("saved before both drives unmounted: %d", count)
		}
		if _, ok := next.Profiles["delete"]; ok {
			t.Fatal("deleted profile persisted")
		}
		return nil
	})
	if msg != "" || len(a.sessions) != 1 || a.doc.Active != "keep" || a.opts.Drive != "Q:" || a.maintenanceActive {
		t.Fatalf("bad state after deletion: %q %+v", msg, a)
	}
}

func TestDeleteProfileSaveFailureRetainsConfigAfterUnmount(t *testing.T) {
	a := NewApp()
	a.doc.Profiles = map[string]profiles.Profile{"delete": {}}
	a.doc.Active = "delete"
	unmounted := false
	a.sessions["O:"] = &mountEntry{profile: "delete", session: deletionSession{unmount: func() { unmounted = true }}}
	msg := a.deleteProfile("delete", func(profiles.Document) error { return errors.New("disk full") })
	if msg != "disk full" || !unmounted || len(a.sessions) != 0 || len(a.doc.Profiles) != 1 || a.doc.Active != "delete" || a.maintenanceActive {
		t.Fatal("failed save lost config or left stale mount")
	}
	a.mountsInProgress = 1
	if msg = a.deleteProfile("delete", func(profiles.Document) error { t.Fatal("saved during pending mount"); return nil }); msg == "" {
		t.Fatal("pending mount did not block deletion")
	}
}

func TestProfileRoutingSurvivesGUIAndCLIConversion(t *testing.T) {
	options := MountOptions{Drive: "O:", Proxy: "socks5://127.0.0.1:1080", IPFamily: "ipv6", Backend: "api"}
	if got := fromStoreOptions(toStoreOptions(options)); got != options {
		t.Fatalf("options changed: %#v", got)
	}
	stored := toStoreOptions(options)
	cfg := profileEnvConfig(profiles.Profile{URL: "https://example.test", Options: &stored})
	if cfg.IPFamily != "ipv6" || cfg.Proxy != options.Proxy {
		t.Fatal("CLI environment lost profile routing")
	}
	app := NewApp()
	stored.IPFamily = "invalid"
	if app.SaveProfile("invalid", profiles.Profile{Options: &stored}) == "" {
		t.Fatal("invalid family was saved")
	}
	if len(app.doc.Profiles) != 0 {
		t.Fatal("failed validation mutated configuration")
	}
}

func TestInstallWinFspRefusesMountedDrives(t *testing.T) {
	app := NewApp()
	// A session-map entry is sufficient: rejecting maintenance must happen
	// before the installer helper is called or a live session is accessed.
	app.sessions["O:"] = &mountEntry{}
	if msg := app.InstallWinFsp(); !strings.Contains(msg, "卸载所有盘符") {
		t.Fatalf("InstallWinFsp() = %q, want unmount guidance", msg)
	}
}

func TestWinFspMaintenanceExcludesMounts(t *testing.T) {
	app := NewApp()
	app.mountsInProgress = 1
	if msg := app.InstallWinFsp(); !strings.Contains(msg, "卸载所有盘符") {
		t.Fatalf("pending mount did not block installation: %q", msg)
	}
	app.mountsInProgress = 0
	app.maintenanceActive = true
	if msg := app.InstallWinFsp(); !strings.Contains(msg, "等待") {
		t.Fatalf("duplicate installation not blocked: %q", msg)
	}
	if msg := app.Mount(MountOptions{Drive: "O:"}); !strings.Contains(msg, "暂时不能挂载") {
		t.Fatalf("maintenance did not block mount before loading credentials: %q", msg)
	}
	if app.mountsInProgress != 0 {
		t.Fatal("blocked mount left a pending operation")
	}
	app.maintenanceActive = false
	app.quitting = true
	if app.beforeClose(nil) {
		t.Fatal("real quit was intercepted")
	}
}
