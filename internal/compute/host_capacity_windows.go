//go:build windows

package compute

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func platformDiskFreeBytes(path string) (uint64, string, error) {
	resolved, err := existingPath(path)
	if err != nil {
		return 0, path, err
	}
	ptr, err := windows.UTF16PtrFromString(resolved)
	if err != nil {
		return 0, resolved, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &free, &total, &totalFree); err != nil {
		return 0, resolved, fmt.Errorf("get disk free space: %w", err)
	}
	return free, resolved, nil
}
