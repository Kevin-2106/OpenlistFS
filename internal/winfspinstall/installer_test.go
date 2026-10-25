package winfspinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"openlistfs/internal/winfspcheck"
)

func TestInstallPlansProtectExistingVersions(t *testing.T) {
	tests := []struct {
		name     string
		status   winfspcheck.Status
		products []string
		err      error
		action   string
	}{
		{"missing", winfspcheck.Status{Code: "missing_installation"}, nil, nil, "install"},
		{"compatible", winfspcheck.Status{Ready: true, Installed: true, Version: "2.0.23075.0"}, nil, nil, "reuse"},
		{"compatible newer", winfspcheck.Status{Ready: true, Installed: true, Version: "2.2.26000.0"}, nil, nil, "reuse"},
		{"old", winfspcheck.Status{Installed: true, Code: "incompatible_version", Version: "1.12.22339.0"}, []string{"1.12.22339"}, nil, "upgrade"},
		{"damaged registered package", winfspcheck.Status{Installed: true, Code: "broken_installation"}, []string{"2.1.25156"}, nil, "repair"},
		{"damaged older package", winfspcheck.Status{Installed: true, Code: "broken_installation"}, []string{"2.0.23075"}, nil, "upgrade"},
		{"missing key registered package", winfspcheck.Status{Code: "missing_installation"}, []string{"2.1.25156"}, nil, "repair"},
		{"damaged newer", winfspcheck.Status{Installed: true, Code: "broken_installation"}, []string{"2.2.26000"}, nil, "manual"},
		{"newer GUI maintenance", winfspcheck.Status{Installed: true, Version: "2.2.26000.0"}, nil, nil, "manual"},
		{"unknown damaged registration", winfspcheck.Status{Installed: true, Code: "broken_installation"}, nil, nil, "manual"},
		{"unparseable version", winfspcheck.Status{Installed: true}, []string{"unknown"}, nil, "manual"},
		{"registry denied", winfspcheck.Status{}, nil, errors.New("access denied"), "manual"},
		{"unsupported", winfspcheck.Status{Code: "unsupported_architecture"}, nil, nil, "manual"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := decidePlan(tt.status, tt.products, tt.err)
			if p.Action != tt.action {
				t.Fatalf("action %q, want %q: %s", p.Action, tt.action, p.Message)
			}
		})
	}
}

func TestVerifyBundledMSIRejectsChangedBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory with spaces", "winfsp.msi")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("test installer bytes")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	expected := hex.EncodeToString(digest[:])
	if err := verifyMSI(path, expected); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyMSI(path, expected); err == nil {
		t.Fatal("changed MSI was accepted")
	}
	if err := verifyMSI(filepath.Join(t.TempDir(), "missing.msi"), expected); err == nil {
		t.Fatal("missing MSI was accepted")
	}
}

func TestPinnedManifestAgreesWithRuntime(t *testing.T) {
	m := Manifest()
	if m.MinimumVersion != winfspcheck.MinimumVersion || !winfspcheck.CompatibleVersion(m.FileVersion) {
		t.Fatalf("manifest/checker disagree: %+v", m)
	}
	digest, err := hex.DecodeString(m.SHA256)
	if err != nil || len(digest) != sha256.Size {
		t.Fatal("invalid pinned digest")
	}
	if m.Filename != "winfsp.msi" {
		t.Fatal("unexpected bundle name")
	}
}
