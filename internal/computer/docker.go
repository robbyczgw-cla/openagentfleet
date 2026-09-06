package computer

import (
	"context"
	"errors"
	"strings"

	"github.com/robbyczgw-cla/openagentfleet/internal/compute"
)

var errDockerRequired = errors.New("docker backend requires compute.Docker")

// DockerBackend wraps the existing local Agent Computer. It does not add a
// second Docker or remote stack.
type DockerBackend struct {
	id     string
	docker *compute.Docker
}

func NewDockerBackend(id string, docker *compute.Docker) *DockerBackend {
	return &DockerBackend{id: id, docker: docker}
}

// RemoteConfigured reports whether the wrapped controller is pointed at a
// remote Agent Computer worker. It does not probe health.
func RemoteConfigured(d *compute.Docker) bool {
	return d != nil && strings.TrimSpace(d.RemoteBaseURL) != ""
}

func (b *DockerBackend) ID() string { return b.id }

func (b *DockerBackend) Kind() Kind { return KindDocker }

func (b *DockerBackend) Start(ctx context.Context) error {
	if b.docker == nil {
		return errDockerRequired
	}
	_, err := b.docker.Ensure(ctx)
	return err
}

func (b *DockerBackend) Stop(ctx context.Context) error {
	if b.docker == nil {
		return errDockerRequired
	}
	return b.docker.Stop(ctx)
}

func (b *DockerBackend) Health(ctx context.Context) (Health, error) {
	if b.docker == nil {
		return Health{}, errDockerRequired
	}
	status := b.docker.Status(ctx)
	return Health{
		State:  status.State,
		Ready:  status.State == compute.ComputerStateReady,
		Detail: status.Detail,
	}, nil
}

func (b *DockerBackend) Capabilities() Capabilities {
	return Capabilities{
		Exec:    true,
		Files:   true,
		Browser: true,
		Screen:  true,
		Remote:  RemoteConfigured(b.docker),
	}
}

func (b *DockerBackend) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	if b.docker == nil {
		return ExecResult{}, errDockerRequired
	}
	if len(req.Argv) == 0 {
		return ExecResult{}, ErrEmptyArgv
	}
	output, err := b.docker.Exec(ctx, req.Argv...)
	return ExecResult{Output: output}, err
}
