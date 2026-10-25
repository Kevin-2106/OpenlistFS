//go:build !windows

package winfspcheck

import "runtime"

// Check reports the unsupported platform without touching system configuration.
func Check() Status {
	return Status{
		Architecture: runtime.GOARCH,
		Code:         "unsupported_platform",
		Message:      "WinFsp mounts require Windows. Run the Windows build of OpenListFS.",
	}
}
