//go:build !windows

package compute

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

// newCommandContext puts the CLI and any helpers it starts into one process
// group. exec.CommandContext's default cancellation kills only the direct
// process; Docker CLI plugins can otherwise survive as orphaned children.
func newCommandContext(ctx context.Context, program string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, program, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
		return nil
	}
	command.WaitDelay = 500 * time.Millisecond
	return command
}
