// Package winfspcheck inspects the installed WinFsp dependency without loading
// its DLL. A failed check therefore cannot poison go-winfsp's cached DLL setup.
package winfspcheck

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MinimumVersion is the oldest supported numeric Windows DLL file version.
const MinimumVersion = "2.0.0.0"

// Status describes the files required by the current application's architecture.
// Ready is a dependency preflight; the mount still reports driver/dispatcher
// errors that can only be discovered when WinFsp starts a filesystem.
type Status struct {
	Ready        bool   `json:"ready"`
	Installed    bool   `json:"installed"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	InstallDir   string `json:"installDir"`
	DLLPath      string `json:"dllPath"`
	Code         string `json:"code"`
	Message      string `json:"message"`
}

// Require returns an actionable dependency error before any mount side effects.
func Require() error {
	return require(Check())
}

func require(status Status) error {
	if status.Ready {
		return nil
	}
	return fmt.Errorf("WinFsp [%s]: %s", status.Code, status.Message)
}

// CompatibleVersion compares numeric VS_FIXEDFILEINFO versions rather than the
// display string, which WinFsp may suffix with a source-control revision.
func CompatibleVersion(version string) bool {
	got, ok := parseVersion(version)
	if !ok {
		return false
	}
	minimum, _ := parseVersion(MinimumVersion)
	for i := range got {
		if got[i] != minimum[i] {
			return got[i] > minimum[i]
		}
	}
	return true
}

func parseVersion(value string) ([4]uint16, bool) {
	var version [4]uint16
	parts := strings.Split(value, ".")
	if len(parts) != len(version) {
		return version, false
	}
	for i, part := range parts {
		if part == "" {
			return version, false
		}
		n, err := strconv.ParseUint(part, 10, 16)
		if err != nil {
			return version, false
		}
		version[i] = uint16(n)
	}
	return version, true
}

type environment struct {
	architecture string
	installDir   func() (string, error)
	inspectDLL   func(path, architecture string) (string, error)
}

func inspect(env environment) Status {
	status := Status{Architecture: env.architecture}
	dllName := architectureDLL(env.architecture)
	if dllName == "" {
		status.Code = "unsupported_architecture"
		status.Message = fmt.Sprintf("WinFsp does not support this application's architecture (%s). Use an x64, x86, or ARM64 OpenListFS build.", env.architecture)
		return status
	}
	dir, err := env.installDir()
	if err != nil {
		status.Code = "broken_installation"
		status.Message = fmt.Sprintf("Cannot inspect the WinFsp installation (%v). Run the OpenListFS installer to install or repair WinFsp %s or newer, then check again.", err, MinimumVersion)
		if errors.Is(err, os.ErrNotExist) {
			status.Code = "missing_installation"
			status.Message = fmt.Sprintf("WinFsp is not installed. Run the OpenListFS installer to install WinFsp %s or newer, then check again. Portable users can install it from https://winfsp.dev/rel/.", MinimumVersion)
		}
		return status
	}
	status.Installed = true
	status.InstallDir = dir
	if strings.TrimSpace(dir) == "" || !filepath.IsAbs(dir) {
		status.Code = "broken_installation"
		status.Message = "WinFsp's InstallDir registration is invalid. Run the OpenListFS installer to repair WinFsp, then check again."
		return status
	}
	status.DLLPath = filepath.Join(dir, "bin", dllName)
	version, err := env.inspectDLL(status.DLLPath, env.architecture)
	if err != nil {
		status.Code = "broken_installation"
		status.Message = fmt.Sprintf("WinFsp's %s DLL is missing or damaged (%v). Run the OpenListFS installer to repair WinFsp, then check again.", env.architecture, err)
		return status
	}
	status.Version = version
	if !CompatibleVersion(version) {
		status.Code = "incompatible_version"
		status.Message = fmt.Sprintf("WinFsp %s is incompatible; OpenListFS requires %s or newer. Run the OpenListFS installer to update WinFsp, then check again.", version, MinimumVersion)
		return status
	}
	status.Ready = true
	status.Code = "ready"
	status.Message = fmt.Sprintf("WinFsp %s is ready for %s mounts.", version, env.architecture)
	return status
}

func architectureDLL(architecture string) string {
	switch architecture {
	case "amd64":
		return "winfsp-x64.dll"
	case "386":
		return "winfsp-x86.dll"
	case "arm64":
		return "winfsp-a64.dll"
	default:
		return ""
	}
}
