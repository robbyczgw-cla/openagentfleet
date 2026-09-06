package computer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/compute"
	"github.com/robbyczgw-cla/openagentfleet/internal/testexe"
)

func TestNativeStartStopHealthRequireAllowExecution(t *testing.T) {
	disabled := NewNativeBackend("native-off", false)
	if err := disabled.Start(context.Background()); !errors.Is(err, compute.ErrExecutionDisabled) {
		t.Fatalf("disabled start: %v", err)
	}
	if err := disabled.Stop(context.Background()); !errors.Is(err, compute.ErrExecutionDisabled) {
		t.Fatalf("disabled stop: %v", err)
	}
	health, err := disabled.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.Ready || health.State != compute.ComputerStateUnavailable {
		t.Fatalf("disabled health = %+v", health)
	}
	if _, err := disabled.Exec(context.Background(), ExecRequest{Argv: []string{"true"}}); !errors.Is(err, compute.ErrExecutionDisabled) {
		t.Fatalf("disabled exec: %v", err)
	}

	enabled := NewNativeBackend("native-on", true)
	if err := enabled.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	health, err = enabled.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !health.Ready || health.State != nativeStateReady {
		t.Fatalf("enabled health = %+v", health)
	}
	if err := enabled.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	health, err = enabled.Health(context.Background())
	if err != nil || health.Ready {
		t.Fatalf("stopped health = %+v err = %v", health, err)
	}
	if enabled.Kind() != KindNative || enabled.ID() != "native-on" {
		t.Fatalf("id=%q kind=%q", enabled.ID(), enabled.Kind())
	}
	caps := enabled.Capabilities()
	if !caps.Exec || !caps.Files || caps.Browser || caps.Screen || caps.Remote {
		t.Fatalf("native capabilities = %+v", caps)
	}
}

func TestNativeExecRejectsEmptyArgv(t *testing.T) {
	backend := NewNativeBackend("native-on", true)
	_, err := backend.Exec(context.Background(), ExecRequest{})
	if !errors.Is(err, ErrEmptyArgv) {
		t.Fatalf("empty argv: %v", err)
	}
}

func TestNativeExecRunsArgvWithoutShell(t *testing.T) {
	dir := t.TempDir()
	helper := testexe.Path(dir, "printarg")
	testexe.Write(t, helper, "#!/bin/sh\nprintf '%s\\n' \"$1\"\n", "@echo off\r\necho(%~1\r\n")
	backend := NewNativeBackend("native-on", true)
	result, err := backend.Exec(context.Background(), ExecRequest{Argv: []string{helper, "hello; echo injected"}})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(result.Output)
	if got != "hello; echo injected" {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestNativeExecUsesWorkdir(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "work")
	if err := os.Mkdir(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := testexe.Path(root, "touchcwd")
	testexe.Write(t, helper, "#!/bin/sh\nprintf ok > marker\n", "@echo off\r\necho ok> marker\r\n")
	backend := NewNativeBackend("native-on", true)
	if _, err := backend.Exec(context.Background(), ExecRequest{Argv: []string{helper}, Workdir: workdir}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(workdir, "marker"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(body)) != "ok" {
		t.Fatalf("marker = %q", body)
	}
}
