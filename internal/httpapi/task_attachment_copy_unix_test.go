//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package httpapi

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestTaskAttachmentCopyRejectsFIFOAndSymlink(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- copyTaskAttachmentFile(fifo, filepath.Join(dir, "copy")) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("copied a named pipe")
		}
	case <-time.After(time.Second):
		t.Fatal("attachment copy blocked on a named pipe")
	}
	other := filepath.Join(dir, "other-upload")
	if err := os.WriteFile(other, []byte("unselected upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "selected")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if err := copyTaskAttachmentFile(link, filepath.Join(dir, "copy")); err == nil {
		t.Fatal("copied another upload through a symlink")
	}
}
