package computer

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/compute"
)

func TestDockerBackendWrapsComputeDocker(t *testing.T) {
	docker := compute.NewDocker(t.TempDir(), "", false)
	backend := NewDockerBackend("agent-computer", docker)
	if backend.ID() != "agent-computer" || backend.Kind() != KindDocker {
		t.Fatalf("id=%q kind=%q", backend.ID(), backend.Kind())
	}
	if err := backend.Start(context.Background()); !errors.Is(err, compute.ErrExecutionDisabled) {
		t.Fatalf("disabled start: %v", err)
	}
	if err := backend.Stop(context.Background()); !errors.Is(err, compute.ErrExecutionDisabled) {
		t.Fatalf("disabled stop: %v", err)
	}
	if _, err := backend.Exec(context.Background(), ExecRequest{Argv: []string{"id"}}); !errors.Is(err, compute.ErrExecutionDisabled) {
		t.Fatalf("disabled exec: %v", err)
	}
	if _, err := backend.Exec(context.Background(), ExecRequest{}); !errors.Is(err, ErrEmptyArgv) {
		t.Fatalf("empty argv: %v", err)
	}

	caps := backend.Capabilities()
	if !caps.Exec || !caps.Files || !caps.Browser || !caps.Screen || caps.Remote {
		t.Fatalf("local docker capabilities = %+v", caps)
	}
	docker.RemoteBaseURL = "https://worker.example"
	if !backend.Capabilities().Remote || !RemoteConfigured(docker) {
		t.Fatal("remote capability should follow RemoteBaseURL")
	}
	if backend.Kind() != KindDocker {
		t.Fatalf("remote transport must not change kind: %q", backend.Kind())
	}
}

func TestDockerHealthMapsStatus(t *testing.T) {
	docker := compute.NewDocker(t.TempDir(), "", true)
	docker.Binary = filepath.Join(t.TempDir(), "missing-docker")
	backend := NewDockerBackend("agent-computer", docker)
	health, err := backend.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.Ready {
		t.Fatalf("missing docker reported ready: %+v", health)
	}
	if health.State == "" {
		t.Fatal("health state is empty")
	}
}

func TestDockerBackendRequiresComputeDocker(t *testing.T) {
	backend := NewDockerBackend("missing", nil)
	if err := backend.Start(context.Background()); !errors.Is(err, errDockerRequired) {
		t.Fatalf("nil start: %v", err)
	}
	if _, err := backend.Health(context.Background()); !errors.Is(err, errDockerRequired) {
		t.Fatalf("nil health: %v", err)
	}
}
