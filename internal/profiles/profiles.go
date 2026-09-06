// Package profiles manages multiple named server configurations
// (profiles.json in the working directory). Passwords are stored in
// plain text — profiles.json must never be committed.
package profiles

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Profile is one named server configuration.
type Profile struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// Store is the on-disk document.
type Store struct {
	Active    string             `json:"active"`
	AutoMount bool               `json:"autoMount"`
	Profiles  map[string]Profile `json:"profiles"`
}

// Options are the last-used mount options (not per profile).
type Options struct {
	Drive        string `json:"drive"`
	Network      bool   `json:"network"`
	ReadAheadMiB int    `json:"readAheadMiB"`
	ChunkKiB     int    `json:"chunkKiB"`
	CacheMiB     int    `json:"cacheMiB"`
	Workers      int    `json:"workers"`
	TTLMinutes   int    `json:"ttlMinutes"`
}

// Document wraps the store with its mount options.
type Document struct {
	Store
	Options Options `json:"options"`
}

// path resolves next to the executable (portable: double-click, tray
// autostart and dev runs all find the same file regardless of CWD).
func path() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "profiles.json")
	}
	return "profiles.json"
}

// Load reads profiles.json; a missing file yields an empty store.
func Load() (Document, error) {
	var d Document
	raw, err := os.ReadFile(path())
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, err
	}
	if d.Profiles == nil {
		d.Profiles = make(map[string]Profile)
	}
	return d, nil
}

// Save writes profiles.json.
func Save(d Document) error {
	if d.Profiles == nil {
		d.Profiles = make(map[string]Profile)
	}
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path(), raw, 0600)
}

// ActiveProfile returns the active profile ("" if none or missing).
func (d *Document) ActiveProfile() (Profile, bool) {
	p, ok := d.Profiles[d.Active]
	return p, ok
}
