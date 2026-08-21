package compute

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
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
		{name: "desktop-windows", endpoint: `npipe:////./pipe/docker_engine`, want: RuntimeDockerDesktop},
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

func TestReconcilePreferredRuntimeUsesDockerWhenColimaIsMissingOnLinux(t *testing.T) {
	got := ReconcilePreferredRuntime("colima")
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("colima"); err != nil && got != RuntimeDocker {
			t.Fatalf("linux without colima = %q, want docker", got)
		}
	} else if runtime.GOOS == "windows" {
		if got != RuntimeDockerDesktop {
			t.Fatalf("windows colima leftover = %q, want docker_desktop", got)
		}
	} else if got != RuntimeColima {
		t.Fatalf("non-linux colima = %q", got)
	}
	if ReconcilePreferredRuntime("docker") != RuntimeDocker {
		t.Fatalf("reconcile docker = %q", ReconcilePreferredRuntime("docker"))
	}
	if ReconcilePreferredRuntime("docker_desktop") != RuntimeDockerDesktop {
		t.Fatalf("reconcile docker_desktop = %q", ReconcilePreferredRuntime("docker_desktop"))
	}
}

func TestLinuxDockerInstallCommandMatchesCommonDistros(t *testing.T) {
	if LinuxDockerInstallCommand() == "" {
		t.Fatal("linux docker install command is empty")
	}
	if !strings.Contains(LinuxDockerInstallDebian, "docker.io") {
		t.Fatalf("debian install command = %q", LinuxDockerInstallDebian)
	}
	if !strings.Contains(LinuxDockerInstallFedora, "dnf") {
		t.Fatalf("fedora install command = %q", LinuxDockerInstallFedora)
	}
}

func TestDockerDaemonUnavailableDetailExplainsLinuxPermissions(t *testing.T) {
	detail := dockerDaemonUnavailableDetail(errors.New("permission denied while trying to connect to the docker API"))
	if !strings.Contains(detail, "docker group") && !strings.Contains(detail, "permission denied") {
		t.Fatalf("permission detail = %q", detail)
	}
}

func TestDiscoverRuntimesAlwaysIncludesDockerEngine(t *testing.T) {
	inventory := DiscoverRuntimes(t.Context(), RuntimeAuto)
	found := false
	for _, item := range inventory {
		if item.ID == RuntimeDocker {
			found = true
			if item.Name != "Docker Engine" || !item.SupportsAgentComputer {
				t.Fatalf("docker engine inventory = %#v", item)
			}
		}
	}
	if !found {
		t.Fatalf("docker engine missing from inventory: %#v", inventory)
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
	if runtime.GOOS == "windows" {
		t.Skip("Colima is not a Windows runtime")
	}
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

func TestDockerStartsColimaWithSelectedResourceFlags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Colima is not a Windows runtime")
	}
	tempDir := t.TempDir()
	t.Setenv("COLIMA_HOME", filepath.Join(tempDir, "colima-home"))
	marker := filepath.Join(tempDir, "started")
	argsFile := filepath.Join(tempDir, "colima-args")
	t.Setenv("OPENAGENTFLEET_TEST_COLIMA_MARKER", marker)
	t.Setenv("OPENAGENTFLEET_TEST_COLIMA_ARGS", argsFile)
	dockerBinary := filepath.Join(tempDir, "docker")
	colimaBinary := filepath.Join(tempDir, "colima")
	if err := os.WriteFile(dockerBinary, []byte("#!/bin/sh\n[ -f \"$OPENAGENTFLEET_TEST_COLIMA_MARKER\" ]\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	colimaScript := `#!/bin/sh
if [ "$1" = "ssh" ]; then
  exit 0
fi
printf '%s\n' "$@" > "$OPENAGENTFLEET_TEST_COLIMA_ARGS"
touch "$OPENAGENTFLEET_TEST_COLIMA_MARKER"
`
	if err := os.WriteFile(colimaBinary, []byte(colimaScript), 0o700); err != nil {
		t.Fatal(err)
	}

	docker := NewDocker(filepath.Join(tempDir, "workspace"), "", true)
	docker.Binary = dockerBinary
	docker.ConfigureRuntime(stoppedColimaSelection(DefaultColimaProfile))
	docker.ConfigureResources(ResourceConfig{CPUs: 6, MemoryGiB: 8, DiskGiB: 50, SwapGiB: 2, OSImage: "ubuntu-24.04"})
	docker.ColimaBinary = colimaBinary
	if err := docker.ensureRuntimeReady(t.Context()); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"start\n", "--profile\nopenagentfleet\n", "--cpus\n6\n", "--memory\n8\n", "--disk\n50\n", "--activate=false\n"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("Colima args %q do not contain %q", args, want)
		}
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
