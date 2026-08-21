// Package ospath treats Linux container paths and Windows host paths as
// absolute. botd on Windows still launches Linux Agent Computer paths like
// /workspace while harnesses use host paths such as C:\Users\...
package ospath

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

func IsAbs(p string) bool {
	if p == "" {
		return false
	}
	if path.IsAbs(p) {
		return true
	}
	return filepath.IsAbs(p)
}

func IsCleanAbs(p string) bool {
	if p == "" || strings.TrimSpace(p) != p || strings.ContainsRune(p, 0) {
		return false
	}
	if path.IsAbs(p) && !strings.Contains(p, `\`) {
		return path.Clean(p) == p
	}
	return filepath.IsAbs(p) && filepath.Clean(p) == p
}

func POSIXModeEnforced() bool {
	return runtime.GOOS != "windows"
}

func CleanPOSIX(p string) string {
	return path.Clean(p)
}

func WithinPOSIX(p, root string) bool {
	p = path.Clean(p)
	root = path.Clean(root)
	if p == root {
		return true
	}
	if root == "/" {
		return strings.HasPrefix(p, "/")
	}
	return strings.HasPrefix(p, root+"/")
}

func IsFilesystemRoot(p string) bool {
	if p == "" {
		return false
	}
	if path.IsAbs(p) && !strings.Contains(p, `\`) && path.Clean(p) == "/" {
		return true
	}
	clean := filepath.Clean(p)
	if clean == "/" || clean == `\` {
		return true
	}
	volume := filepath.VolumeName(clean)
	return volume != "" && (clean == volume+`\` || clean == volume+"/")
}

func OwnerOnlyFile(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if !POSIXModeEnforced() {
		return true
	}
	return info.Mode().Perm() == 0o600
}

func OwnerOnlyDir(info os.FileInfo) bool {
	if info == nil || !info.IsDir() {
		return false
	}
	if !POSIXModeEnforced() {
		return true
	}
	return info.Mode().Perm()&0o077 == 0
}
