package compute

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestClassifyDockerContextIdentifiesSupportedMacRuntimes(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     string
	}{
		{name: "desktop-linux", endpoint: "unix:///Users/test/.docker/run/docker.sock", want: RuntimeDockerDesktop},
		{name: "openagentfleet", endpoint: "unix:///Users/test/.colima/openagentfleet/docker.sock", want: RuntimeColima},
		{name: "orbstack", endpoint: "unix:///Users/test/.orbstack/run/docker.sock", want: RuntimeOrbStack},
		{name: "remote-linux", endpoint: "ssh://builder@example.com", want: RuntimeDocker},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyDockerContext(dockerContext{Name: test.name, Endpoint: test.endpoint}); got != test.want {
				t.Fatalf("classifyDockerContext = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDockerCommandArgsKeepGlobalContextUntouched(t *testing.T) {
	docker := NewDocker(t.TempDir(), "", true)
	if got := docker.commandArgs("info", "--format", "json"); !reflect.DeepEqual(got, []string{"info", "--format", "json"}) {
		t.Fatalf("default command args = %#v", got)
	}
	docker.Context = "colima-openagentfleet"
	if got := docker.commandArgs("info"); !reflect.DeepEqual(got, []string{"--context", "colima-openagentfleet", "info"}) {
		t.Fatalf("explicit context command args = %#v", got)
	}
}

func TestStoppedColimaSelectionUsesDedicatedProfile(t *testing.T) {
	selection := stoppedColimaSelection(DefaultColimaProfile)
	if selection.ID != RuntimeColima || selection.Context != "colima-openagentfleet" {
		t.Fatalf("stopped Colima selection = %#v", selection)
	}
	if selection.Endpoint == "" || selection.Detail == "" {
		t.Fatalf("stopped Colima selection lacks endpoint/detail: %#v", selection)
	}
}

func TestDockerStartsStoppedColimaWithoutChangingGlobalContext(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("COLIMA_HOME", filepath.Join(tempDir, "colima-home"))
	marker := filepath.Join(tempDir, "started")
	t.Setenv("OPENAGENTFLEET_TEST_COLIMA_MARKER", marker)
	dockerBinary := filepath.Join(tempDir, "docker")
	colimaBinary := filepath.Join(tempDir, "colima")
	if err := os.WriteFile(dockerBinary, []byte("#!/bin/sh\n[ -f \"$OPENAGENTFLEET_TEST_COLIMA_MARKER\" ]\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(colimaBinary, []byte("#!/bin/sh\ntouch \"$OPENAGENTFLEET_TEST_COLIMA_MARKER\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	docker := NewDocker(t.TempDir(), "", true)
	docker.Binary = dockerBinary
	docker.ConfigureRuntime(stoppedColimaSelection(DefaultColimaProfile))
	docker.ColimaBinary = colimaBinary
	if err := docker.ensureRuntimeReady(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("Colima starter was not called: %v", err)
	}
	if docker.Context != "colima-openagentfleet" {
		t.Fatalf("runtime context = %q", docker.Context)
	}
}

func TestAppleContainerIsNotClaimedAsAgentComputerReady(t *testing.T) {
	info := discoverAppleContainer(t.Context(), false)
	if info.SupportsAgentComputer {
		t.Fatal("Apple Container must not claim Agent Computer support before adapter validation")
	}
	if !info.Experimental || !info.OpenSource {
		t.Fatalf("Apple Container metadata = %#v", info)
	}
}

func TestInferStateDistinguishesComputerLifecycle(t *testing.T) {
	tests := []struct {
		name      string
		status    Status
		wantState string
		wantRetry bool
	}{
		{name: "unavailable", status: Status{}, wantState: ComputerStateUnavailable},
		{name: "stopped", status: Status{Available: true}, wantState: ComputerStateStopped, wantRetry: true},
		{name: "starting", status: Status{Available: true, Running: true}, wantState: ComputerStateStarting, wantRetry: true},
		{name: "browser only", status: Status{Available: true, Running: true, BrowserReady: true}, wantState: ComputerStateStarting, wantRetry: true},
		{name: "ready", status: Status{Available: true, Running: true, BrowserReady: true, DesktopReady: true}, wantState: ComputerStateReady},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, retry := inferState(test.status)
			if state != test.wantState || retry != test.wantRetry {
				t.Fatalf("inferState = (%q, %t), want (%q, %t)", state, retry, test.wantState, test.wantRetry)
			}
		})
	}
}
