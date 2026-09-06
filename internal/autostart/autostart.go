// Package autostart manages the per-user boot auto-start entry for the
// OpenListFS GUI (HKCU\...\Run). Enabled entry launches the executable
// with --minimized --automount so it starts in the tray and mounts the
// active profile.
package autostart

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const valueName = "OpenListFS"

func command() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return `"` + exe + `" --minimized --automount`, nil
}

// Enabled reports whether the auto-start entry exists and matches the
// current executable path.
func Enabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	want, err := command()
	if err != nil {
		return false
	}
	got, _, err := k.GetStringValue(valueName)
	return err == nil && got == want
}

// Set enables or disables the auto-start entry.
func Set(enable bool) error {
	if !enable {
		k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
		if err != nil {
			return err
		}
		defer k.Close()
		return k.DeleteValue(valueName)
	}
	cmd, err := command()
	if err != nil {
		return err
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(valueName, cmd)
}
