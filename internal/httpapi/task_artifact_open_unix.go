//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package httpapi

import (
	"os"
	"syscall"
)

func openTaskArtifactFile(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
