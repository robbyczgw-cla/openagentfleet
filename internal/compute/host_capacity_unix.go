//go:build darwin || linux

package compute

import (
	"fmt"
	"os"
	"path/filepath"

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

func existingPath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		if _, err := os.Stat(current); err == nil {
			return current, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing parent for %s", path)
		}
	}
}
