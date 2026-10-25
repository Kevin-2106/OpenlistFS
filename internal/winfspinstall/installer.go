// Package winfspinstall verifies and opens the unmodified official WinFsp MSI.
// Installation is always initiated by a user action, never by a mount/check.
package winfspinstall

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"openlistfs/internal/winfspcheck"
)

//go:embed winfsp.json
var manifestJSON []byte

type ManifestInfo struct {
	Version        string `json:"version"`
	FileVersion    string `json:"fileVersion"`
	MinimumVersion string `json:"minimumVersion"`
	Filename       string `json:"filename"`
	URL            string `json:"url"`
	SHA256         string `json:"sha256"`
	LicenseURL     string `json:"licenseUrl"`
}

// Manifest returns compiled-in metadata. An external JSON cannot replace the
// trusted digest used to verify the bundled MSI.
func Manifest() ManifestInfo {
	var m ManifestInfo
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		panic(err)
	}
	return m
}

type InstallerStatus struct {
	Available bool   `json:"available"`
	Message   string `json:"message"`
}

type InstallPlan struct {
	Action     string             `json:"action"` // reuse, install, upgrade, repair, or manual
	Message    string             `json:"message"`
	Dependency winfspcheck.Status `json:"dependency"`
}

// Plan protects newer and unknown installations from an automatic downgrade.
// A compatible installation is reused even if newer than the bundled MSI.
func Plan() InstallPlan {
	status := winfspcheck.Check()
	if status.Ready {
		return decidePlan(status, nil, nil)
	}
	versions, err := installedProductVersions()
	return decidePlan(status, versions, err)
}

func decidePlan(status winfspcheck.Status, productVersions []string, inspectErr error) InstallPlan {
	p := InstallPlan{Dependency: status}
	if status.Ready {
		p.Action, p.Message = "reuse", "Compatible WinFsp is already installed; it will be reused."
		return p
	}
	if status.Code == "unsupported_architecture" {
		p.Action, p.Message = "manual", status.Message
		return p
	}
	if inspectErr != nil {
		p.Action, p.Message = "manual", fmt.Sprintf("Cannot safely determine the installed WinFsp version (%v). Repair the existing version from Windows Installed apps; the bundled MSI will not be used.", inspectErr)
		return p
	}
	versions := append([]string(nil), productVersions...)
	if status.Version != "" {
		versions = append(versions, status.Version)
	}
	bundled, _ := numericVersion(Manifest().FileVersion)
	older := false
	for _, value := range versions {
		v, ok := numericVersion(value)
		if !ok {
			p.Action, p.Message = "manual", "The installed WinFsp version is unknown. Repair it from Windows Installed apps; automatic replacement is disabled."
			return p
		}
		if compare(v, bundled) > 0 {
			p.Action, p.Message = "manual", fmt.Sprintf("WinFsp %s is newer than bundled %s. Repair that version from Windows Installed apps or its official installer; OpenListFS will not downgrade it.", value, Manifest().Version)
			return p
		}
		older = older || compare(v, bundled) < 0
	}
	if status.Installed && len(versions) == 0 {
		p.Action, p.Message = "manual", "A damaged WinFsp registration has an unknown version. Repair it from Windows Installed apps before mounting."
		return p
	}
	switch {
	case !status.Installed && len(versions) == 0:
		p.Action, p.Message = "install", "Install the bundled official WinFsp MSI. Windows will request administrator permission."
	case status.Code == "incompatible_version" || older:
		p.Action, p.Message = "upgrade", "Update WinFsp with the bundled official MSI. Existing shared installations are never forcibly uninstalled."
	default:
		p.Action, p.Message = "repair", "Open the bundled official MSI to repair WinFsp. Windows will request administrator permission."
	}
	return p
}

func numericVersion(value string) ([4]uint32, bool) {
	var v [4]uint32
	parts := strings.Split(value, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return v, false
	}
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return v, false
		}
		v[i] = uint32(n)
	}
	return v, true
}

func compare(a, b [4]uint32) int {
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

func bundledPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), "dependencies", Manifest().Filename), nil
}

func verifyMSI(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("Bundled WinFsp installer is unavailable. Use the OpenListFS setup or download it from https://winfsp.dev/rel/: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != strings.ToLower(expected) {
		return fmt.Errorf("Bundled WinFsp MSI failed SHA-256 verification. Run the OpenListFS setup again; this file will not be opened.")
	}
	return nil
}

func Check() InstallerStatus {
	path, err := bundledPath()
	if err == nil {
		err = verifyMSI(path, Manifest().SHA256)
	}
	if err != nil {
		return InstallerStatus{Message: err.Error()}
	}
	p := maintenancePlan()
	if p.Action == "manual" {
		return InstallerStatus{Message: p.Message}
	}
	return InstallerStatus{Available: true, Message: p.Message}
}

// maintenancePlan also guards the GUI's repair button: a ready newer version
// must not be replaced merely because the user requested maintenance.
func maintenancePlan() InstallPlan {
	status := winfspcheck.Check()
	versions, err := installedProductVersions()
	status.Ready = false
	return decidePlan(status, versions, err)
}

func prepare(maintenance bool) (string, InstallPlan, error) {
	path, err := bundledPath()
	if err != nil {
		return "", InstallPlan{}, err
	}
	if err := verifyMSI(path, Manifest().SHA256); err != nil {
		return "", InstallPlan{}, err
	}
	p := Plan()
	if maintenance {
		p = maintenancePlan()
	}
	if p.Action == "manual" {
		return "", p, fmt.Errorf("%s", p.Message)
	}
	return path, p, nil
}

// Launch opens the verified MSI for the application's maintenance button and
// waits so that mounts can stay disabled until maintenance has completed.
func Launch() error {
	path, plan, err := prepare(true)
	if err != nil {
		return err
	}
	code, err := launchMSI(path, plan, true)
	if err != nil {
		return err
	}
	switch code {
	case 0:
		return nil
	case 3010:
		return fmt.Errorf("WinFsp installation completed. Restart Windows before mounting a drive.")
	case 1602:
		return fmt.Errorf("WinFsp installation was cancelled.")
	default:
		return fmt.Errorf("WinFsp installation failed (code %d). See the Windows Installer log in your temporary directory.", code)
	}
}

// Run installs a required dependency for the setup program and returns the MSI
// exit code (including 3010/reboot required). It never replaces a ready version.
func Run() (uint32, error) {
	path, plan, err := prepare(false)
	if err != nil {
		return 1, err
	}
	if plan.Action == "reuse" {
		return 0, nil
	}
	return launchMSI(path, plan, true)
}
