//go:build windows

package compute

import (
	"context"
	"os/exec"
)

func newCommandContext(ctx context.Context, program string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, program, args...)
}
