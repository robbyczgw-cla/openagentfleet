//go:build windows

package compute

import (
	"context"
	"os/exec"
)

// newCommandContext is a plain CommandContext. Windows has no process group
// here and no Job Object, so Docker CLI plugins can outlive cancellation.
func newCommandContext(ctx context.Context, program string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, program, args...)
}
