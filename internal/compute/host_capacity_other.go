//go:build !darwin && !linux && !windows

package compute

import "errors"

func platformDiskFreeBytes(path string) (uint64, string, error) {
	return 0, path, errors.New("host free-space checks are not implemented on this platform")
}
