package computer

import (
	"context"
	"errors"
	"os/exec"
	"sync"

	"github.com/robbyczgw-cla/openagentfleet/internal/compute"
)

const nativeStateReady = compute.ComputerStateReady

// ErrEmptyArgv is returned when Exec is called without a program.
var ErrEmptyArgv = errors.New("computer exec argv is empty")

// NativeBackend executes argv on the controller host. It is the first
// functional backend, for tests and future host exec, not the shipped
// Agent Computer. Start and Stop do not touch the OS; they only gate Health.
type NativeBackend struct {
	id             string
	allowExecution bool

	mu      sync.Mutex
	started bool
}

func NewNativeBackend(id string, allowExecution bool) *NativeBackend {
	return &NativeBackend{id: id, allowExecution: allowExecution}
}

func (b *NativeBackend) ID() string { return b.id }

func (b *NativeBackend) Kind() Kind { return KindNative }

func (b *NativeBackend) Start(context.Context) error {
	if !b.allowExecution {
		return compute.ErrExecutionDisabled
	}
	b.mu.Lock()
	b.started = true
	b.mu.Unlock()
	return nil
}

func (b *NativeBackend) Stop(context.Context) error {
	if !b.allowExecution {
		return compute.ErrExecutionDisabled
	}
	b.mu.Lock()
	b.started = false
	b.mu.Unlock()
	return nil
}

func (b *NativeBackend) Health(context.Context) (Health, error) {
	if !b.allowExecution {
		return Health{State: compute.ComputerStateUnavailable, Detail: compute.ErrExecutionDisabled.Error()}, nil
	}
	b.mu.Lock()
	started := b.started
	b.mu.Unlock()
	if !started {
		return Health{State: compute.ComputerStateStopped}, nil
	}
	return Health{State: nativeStateReady, Ready: true}, nil
}

func (b *NativeBackend) Capabilities() Capabilities {
	return Capabilities{Exec: true, Files: true}
}

func (b *NativeBackend) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	if !b.allowExecution {
		return ExecResult{}, compute.ErrExecutionDisabled
	}
	if len(req.Argv) == 0 {
		return ExecResult{}, ErrEmptyArgv
	}
	cmd := exec.CommandContext(ctx, req.Argv[0], req.Argv[1:]...)
	if req.Workdir != "" {
		cmd.Dir = req.Workdir
	}
	output, err := cmd.CombinedOutput()
	return ExecResult{Output: string(output)}, err
}
