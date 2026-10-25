//go:build windows

package main

import (
	"errors"
	"testing"

	"openlistfs/internal/profilecode"
	"openlistfs/internal/profiles"
)

func TestProfileImportFailedSaveLeavesRunningStateUntouched(t *testing.T) {
	a := NewApp()
	a.doc = profiles.Document{Store: profiles.Store{Active: "old", Profiles: map[string]profiles.Profile{"old": {URL: "https://old.example.test"}}}}
	c := profilecode.Configuration{Version: 1, Name: "new", Profile: profiles.Profile{URL: "https://new.example.test"}}
	if _, err := a.importProfile(c, func(profiles.Document) error { return errors.New("disk full") }); err == nil {
		t.Fatal("save failure was ignored")
	}
	if a.doc.Active != "old" || len(a.doc.Profiles) != 1 {
		t.Fatal("failed import changed running configuration")
	}
}

func TestProfileImportAndExportSavedConfiguration(t *testing.T) {
	a := NewApp()
	a.opts = MountOptions{Drive: "O:", Network: true, ReadAheadMiB: 64, ChunkKiB: 512, CacheMiB: 256, Workers: 3, TTLMinutes: 2, Proxy: "none", Backend: "webdav"}
	c := profilecode.Configuration{Version: 1, Name: "test", Profile: profiles.Profile{URL: "https://new.example.test/dav", Username: "fake-user", Password: "fake-password"}}
	var saved profiles.Document
	name, err := a.importProfile(c, func(d profiles.Document) error { saved = d; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if saved.Active != name || len(saved.Profiles) != 1 {
		t.Fatal("import was not saved")
	}
	r := a.ExportProfileCode(name)
	if r.Error != "" {
		t.Fatal(r.Error)
	}
	decoded, err := profilecode.Decode(r.Code)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Profile.Password != "fake-password" || decoded.Profile.Options.Backend != "webdav" {
		t.Fatal("export lost server credentials or mount settings")
	}
	if len(a.sessions) != 0 {
		t.Fatal("configuration import/export mounted a drive")
	}
}
