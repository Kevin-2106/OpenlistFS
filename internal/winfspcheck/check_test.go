package winfspcheck

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectDependency(t *testing.T) {
	installDir := filepath.Join(t.TempDir(), "Program Files (x86)", "WinFsp")
	cases := []struct {
		name      string
		arch      string
		dir       string
		dirErr    error
		version   string
		dllErr    error
		wantCode  string
		ready     bool
		installed bool
		wantDLL   string
	}{
		{name: "missing", arch: "amd64", dirErr: os.ErrNotExist, wantCode: "missing_installation"},
		{name: "unreadable registration", arch: "amd64", dirErr: os.ErrPermission, wantCode: "broken_installation"},
		{name: "empty directory", arch: "amd64", wantCode: "broken_installation", installed: true},
		{name: "relative directory", arch: "amd64", dir: "WinFsp", wantCode: "broken_installation", installed: true},
		{name: "missing DLL", arch: "amd64", dir: installDir, dllErr: os.ErrNotExist, wantCode: "broken_installation", installed: true, wantDLL: "winfsp-x64.dll"},
		{name: "broken DLL", arch: "amd64", dir: installDir, dllErr: errors.New("invalid PE file"), wantCode: "broken_installation", installed: true, wantDLL: "winfsp-x64.dll"},
		{name: "old version", arch: "amd64", dir: installDir, version: "1.12.23075.0", wantCode: "incompatible_version", installed: true, wantDLL: "winfsp-x64.dll"},
		{name: "unknown version", arch: "amd64", dir: installDir, version: "2.1.25156.ddca7bd", wantCode: "incompatible_version", installed: true, wantDLL: "winfsp-x64.dll"},
		{name: "minimum with spaces", arch: "amd64", dir: installDir, version: MinimumVersion, wantCode: "ready", ready: true, installed: true, wantDLL: "winfsp-x64.dll"},
		{name: "newer version", arch: "amd64", dir: installDir, version: "2.1.25156.0", wantCode: "ready", ready: true, installed: true, wantDLL: "winfsp-x64.dll"},
		{name: "ARM64", arch: "arm64", dir: installDir, version: "2.1.25156.0", wantCode: "ready", ready: true, installed: true, wantDLL: "winfsp-a64.dll"},
		{name: "x86", arch: "386", dir: installDir, version: "2.1.25156.0", wantCode: "ready", ready: true, installed: true, wantDLL: "winfsp-x86.dll"},
		{name: "unsupported", arch: "riscv64", wantCode: "unsupported_architecture"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inspected := false
			status := inspect(environment{
				architecture: tc.arch,
				installDir: func() (string, error) {
					if tc.wantCode == "unsupported_architecture" {
						t.Fatal("unsupported architecture should not inspect registry")
					}
					return tc.dir, tc.dirErr
				},
				inspectDLL: func(path, arch string) (string, error) {
					inspected = true
					if path != filepath.Join(installDir, "bin", tc.wantDLL) || arch != tc.arch {
						t.Fatalf("incorrect DLL inspection: %q, %q", path, arch)
					}
					return tc.version, tc.dllErr
				},
			})
			if status.Code != tc.wantCode || status.Ready != tc.ready || status.Installed != tc.installed {
				t.Fatalf("unexpected status: %+v", status)
			}
			if inspected != (tc.wantDLL != "") {
				t.Fatalf("DLL inspected=%v, want %v", inspected, tc.wantDLL != "")
			}
			if status.Message == "" || (!status.Ready && !strings.Contains(status.Message, "OpenListFS")) {
				t.Fatalf("dependency status lacks actionable guidance: %+v", status)
			}
			if err := require(status); (err == nil) != tc.ready {
				t.Fatalf("require = %v, ready = %v", err, tc.ready)
			}
		})
	}
}

func TestCompatibleVersion(t *testing.T) {
	cases := map[string]bool{
		"1.65535.65535.65535": false,
		"2.0.0.0":             true,
		"2.0.0.1":             true,
		"2.1.25156.0":         true,
		"3.0.0.0":             true,
		"":                    false,
		"2.0":                 false,
		"2.0.0.0.1":           false,
		"2.0.0.65536":         false,
		"2.1.25156.ddca7bd":   false,
		"2.-1.0.0":            false,
	}
	for version, want := range cases {
		if got := CompatibleVersion(version); got != want {
			t.Errorf("CompatibleVersion(%q) = %v, want %v", version, got, want)
		}
	}
}
