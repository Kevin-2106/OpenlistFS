//go:build windows

package winfspcheck

import (
	"debug/pe"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Check reads the same registry view and DLL filename as go-winfsp. It never
// initializes go-winfsp or executes code from the installed WinFsp DLL.
func Check() Status {
	return inspect(environment{
		architecture: runtime.GOARCH,
		installDir:   installedDirectory,
		inspectDLL:   inspectWindowsDLL,
	})
}

func installedDirectory() (string, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `Software\WinFsp`, registry.QUERY_VALUE|registry.WOW64_32KEY)
	if err != nil {
		return "", err
	}
	defer key.Close()
	dir, valueType, err := key.GetStringValue("InstallDir")
	if err != nil {
		// A present key with a missing value is damaged, not an absent install.
		return "", fmt.Errorf("read InstallDir: %v", err)
	}
	if valueType != registry.SZ {
		return "", fmt.Errorf("InstallDir must be a REG_SZ value")
	}
	return dir, nil
}

func inspectWindowsDLL(path, architecture string) (string, error) {
	image, err := pe.Open(path)
	if err != nil {
		return "", err
	}
	defer image.Close()
	machines := map[string]uint16{
		"amd64": pe.IMAGE_FILE_MACHINE_AMD64,
		"386":   pe.IMAGE_FILE_MACHINE_I386,
		"arm64": pe.IMAGE_FILE_MACHINE_ARM64,
	}
	const imageFileDLL = 0x2000
	if image.Machine != machines[architecture] || image.Characteristics&imageFileDLL == 0 || image.OptionalHeader == nil {
		return "", fmt.Errorf("DLL is not a valid %s Windows image", architecture)
	}
	return windowsFileVersion(path)
}

func windowsFileVersion(path string) (string, error) {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil {
		return "", fmt.Errorf("read numeric file version: %w", err)
	}
	// Normal version resources are small. Reject malformed/unbounded resources.
	if size == 0 || size > 8<<20 {
		return "", fmt.Errorf("invalid version resource size %d", size)
	}
	buffer := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buffer[0])); err != nil {
		return "", fmt.Errorf("read numeric file version: %w", err)
	}
	var info *windows.VS_FIXEDFILEINFO
	var infoSize uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buffer[0]), `\`, unsafe.Pointer(&info), &infoSize); err != nil {
		return "", fmt.Errorf("read numeric file version: %w", err)
	}
	if info == nil || infoSize < uint32(unsafe.Sizeof(*info)) || info.Signature != 0xFEEF04BD {
		return "", fmt.Errorf("invalid numeric file version resource")
	}
	return fmt.Sprintf("%d.%d.%d.%d", info.FileVersionMS>>16, info.FileVersionMS&0xffff, info.FileVersionLS>>16, info.FileVersionLS&0xffff), nil
}
