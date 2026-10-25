//go:build windows

package winfspcheck

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInspectWindowsDLLRejectsDamagedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not a DLL.dll")
	if _, err := inspectWindowsDLL(path, "amd64"); err == nil {
		t.Fatal("missing DLL passed inspection")
	}
	if err := os.WriteFile(path, []byte("damaged DLL"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectWindowsDLL(path, "amd64"); err == nil {
		t.Fatal("damaged DLL passed inspection")
	}
	if _, err := windowsFileVersion(path); err == nil {
		t.Fatal("file without a version resource passed inspection")
	}
}
