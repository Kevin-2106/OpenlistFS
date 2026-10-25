//go:build windows

package winfspinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

func TestShellExecuteInfoNativeLayout(t *testing.T) {
	expected := uintptr(112)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		expected = 60
	}
	if got := unsafe.Sizeof(shellExecuteInfo{}); got != expected {
		t.Fatalf("native struct size %d, want %d", got, expected)
	}
}

func TestVerifiedMSIStaysLockedUntilCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "winfsp.msi")
	data := []byte("test installer")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	f, err := openLockedMSI(path, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.WriteFile(path, []byte("replacement"), 0600); err == nil {
		t.Fatal("verified MSI could be modified while held")
	}
	if err := os.Rename(path, path+".moved"); err == nil {
		t.Fatal("verified MSI could be replaced while held")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("after close"), 0600); err != nil {
		t.Fatal(err)
	}
}
