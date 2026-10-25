//go:build windows

package winfspinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func installedProductVersions() ([]string, error) {
	var versions []string
	for _, view := range []uint32{registry.WOW64_32KEY, registry.WOW64_64KEY} {
		root, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, registry.ENUMERATE_SUB_KEYS|view)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			continue
		}
		if err != nil {
			return nil, err
		}
		names, err := root.ReadSubKeyNames(-1)
		if err != nil {
			root.Close()
			return nil, err
		}
		for _, name := range names {
			key, err := registry.OpenKey(root, name, registry.QUERY_VALUE|view)
			if err != nil {
				continue
			}
			displayName, _, _ := key.GetStringValue("DisplayName")
			if displayName == "WinFsp" || strings.HasPrefix(displayName, "WinFsp ") {
				version, _, err := key.GetStringValue("DisplayVersion")
				key.Close()
				if err != nil {
					root.Close()
					return nil, fmt.Errorf("read %s version: %w", displayName, err)
				}
				versions = append(versions, version)
			} else {
				key.Close()
			}
		}
		root.Close()
	}
	return versions, nil
}

// Native alignment is supplied by Go for both 32-bit and 64-bit Windows.
type shellExecuteInfo struct {
	Size       uint32
	Mask       uint32
	Window     windows.Handle
	Verb       *uint16
	File       *uint16
	Parameters *uint16
	Directory  *uint16
	Show       int32
	Instance   windows.Handle
	IDList     uintptr
	Class      *uint16
	ClassKey   windows.Handle
	HotKey     uint32
	Icon       windows.Handle
	Process    windows.Handle
}

var shellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

func launchMSI(path string, plan InstallPlan, wait bool) (uint32, error) {
	// Deny writes/deletion from verification through MSI completion, including
	// the time spent waiting for UAC. The elevated process opens these bytes.
	locked, err := openLockedMSI(path, Manifest().SHA256)
	if err != nil {
		return 1, err
	}
	defer locked.Close()
	systemDir, err := windows.GetSystemDirectory()
	if err != nil {
		return 1, err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(filepath.Join(systemDir, "msiexec.exe"))
	if err != nil {
		return 1, err
	}
	// Quotes cannot occur in a Windows filename. The verified absolute path is
	// passed directly to msiexec, with no shell command parsing.
	parameters := `/i "` + path + `" /norestart`
	if wait {
		logPath := filepath.Join(os.TempDir(), "OpenListFS-WinFsp-install.log")
		parameters += ` /passive /L*v "` + logPath + `"`
		if plan.Action == "repair" {
			parameters += ` REINSTALL=ALL REINSTALLMODE=vomus`
		}
	}
	args, err := windows.UTF16PtrFromString(parameters)
	if err != nil {
		return 1, err
	}
	info := shellExecuteInfo{Mask: 0x00000040 | 0x00000400, Verb: verb, File: file, Parameters: args, Show: windows.SW_SHOWNORMAL}
	info.Size = uint32(unsafe.Sizeof(info))
	ok, _, callErr := shellExecuteEx.Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return 1602, fmt.Errorf("WinFsp installation was cancelled at the administrator permission prompt")
		}
		return 1, fmt.Errorf("Cannot open the official WinFsp installer: %w", callErr)
	}
	if info.Process == 0 {
		return 1, fmt.Errorf("Windows Installer did not return a process handle")
	}
	defer windows.CloseHandle(info.Process)
	if !wait {
		return 0, nil
	}
	if _, err := windows.WaitForSingleObject(info.Process, windows.INFINITE); err != nil {
		return 1, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.Process, &code); err != nil {
		return 1, err
	}
	return code, nil
}

func openLockedMSI(path, expected string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, fmt.Errorf("Cannot lock the bundled WinFsp MSI for verification: %w", err)
	}
	f := os.NewFile(uintptr(handle), path)
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		f.Close()
		return nil, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(expected) {
		f.Close()
		return nil, fmt.Errorf("Bundled WinFsp MSI failed SHA-256 verification; it will not be opened.")
	}
	return f, nil
}
