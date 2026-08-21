//go:build darwin || linux

package compute

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func platformDiskFreeBytes(path string) (uint64, string, error) {
	resolved, err := existingPath(path)
	if err != nil {
		return 0, path, err
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(resolved, &stat); err != nil {
		return 0, resolved, fmt.Errorf("stat filesystem: %w", err)
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), resolved, nil
}
