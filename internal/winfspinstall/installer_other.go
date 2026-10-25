//go:build !windows

package winfspinstall

import "fmt"

func installedProductVersions() ([]string, error) {
	return nil, fmt.Errorf("WinFsp is only supported on Windows")
}
func launchMSI(string, InstallPlan, bool) (uint32, error) {
	return 1, fmt.Errorf("WinFsp is only supported on Windows")
}
