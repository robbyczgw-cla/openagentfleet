package isolation

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDockerPlanIsDeterministicAndHardened(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	readOnly := filepath.Join(root, "reference")
	for _, path := range []string{workspace, readOnly} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	planner := mustPlanner(t, Policy{ApprovedMountRoots: []string{root}, WritableMountRoots: []string{workspace}})
	base := validDockerSpec(workspace)
	base.Mounts = []Mount{
		{HostPath: workspace, GuestPath: "/workspace", Mode: MountReadWrite},
		{HostPath: readOnly, GuestPath: "/reference", Mode: MountReadOnly},
	}
	base.Secrets = []SecretReference{{Environment: "GITHUB_REF", Source: SecretSourceKeychain, Reference: "github-work"}}

	first, err := planner.Plan(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Mounts[0], base.Mounts[1] = base.Mounts[1], base.Mounts[0]
	second, err := planner.Plan(base)
	if err != nil {
		t.Fatal(err)
	}
	if first.SpecHash != second.SpecHash || !reflect.DeepEqual(first.DockerArgs, second.DockerArgs) {
		t.Fatalf("plans are not deterministic\nfirst=%q\nsecond=%q", first.DockerArgs, second.DockerArgs)
	}

	wantPrefix := []string{
		"run", "--rm", "--pull=never", "--name", "openagentfleet-worker-run_01", "--label", workerLabel + "=true", "--label", "io.openagentfleet.session=run_01", "--label", "io.openagentfleet.isolation-version=1", "--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--pids-limit", "128", "--cpus", "1.5", "--memory", "536870912", "--network", "none",
	}
	if !reflect.DeepEqual(first.DockerArgs[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("unexpected hardened Docker prefix\n got: %q\nwant: %q", first.DockerArgs[:len(wantPrefix)], wantPrefix)
	}
	resolvedReadOnly, err := filepath.EvalSymlinks(readOnly)
	if err != nil {
		t.Fatal(err)
	}
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range [][2]string{
		{"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=67108864,mode=1777"},
		{"--mount", "type=bind,source=" + resolvedReadOnly + ",target=/reference,readonly"},
		{"--mount", "type=bind,source=" + resolvedWorkspace + ",target=/workspace"},
		{"--env", "GITHUB_REF=keychain:github-work"},
		{"--workdir", "/workspace"},
	} {
		if !hasArgPair(first.DockerArgs, required[0], required[1]) {
			t.Errorf("Docker args missing %q: %q", required, first.DockerArgs)
		}
	}
	args := strings.Join(first.DockerArgs, " ")
	for _, forbidden := range []string{"--privileged", "--cap-add", "--network host", "--volume", "--publish"} {
		if strings.Contains(args, forbidden) {
			t.Errorf("Docker args must not contain %q: %q", forbidden, args)
		}
	}
	if !reflect.DeepEqual(first.Cleanup.StopArgs, []string{"stop", "--time", "10", "openagentfleet-worker-run_01"}) {
		t.Fatalf("unexpected cleanup stop args: %q", first.Cleanup.StopArgs)
	}
}

func TestForbiddenWindowsSystemRoots(t *testing.T) {
	if !forbiddenHostPath(`C:\Windows\System32`) {
		t.Fatal("C:\\Windows\\System32 must be forbidden")
	}
	if !forbiddenHostPath(`C:\Program Files\Docker\Docker`) {
		t.Fatal("C:\\Program Files must be forbidden")
	}
	if !forbiddenHostPath(`npipe:////./pipe/docker_engine`) {
		t.Fatal("docker engine named pipe must be forbidden")
	}
	if forbiddenWindowsHostPath("/var/lib/docker") {
		t.Fatal("linux docker root must not match windows system rules")
	}
	if forbiddenWindowsHostPath(`C:\WINDOWS\SystemTemp`) {
		t.Fatal("Windows temp must not be treated as a system root")
	}
	if forbiddenWindowsHostPath(`C:\Users\dev\AppData\Local\Temp`) {
		t.Fatal("approved workspaces under a user profile must be allowed")
	}
	if !forbiddenWindowsHostPath(`C:\Users`) {
		t.Fatal("C:\\Users must be forbidden as a wholesale mount")
	}
	if !forbiddenHostPath("/") {
		t.Fatal("filesystem root must be forbidden")
	}
}

func TestMountsFailClosed(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	planner := mustPlanner(t, Policy{ApprovedMountRoots: []string{root}, WritableMountRoots: []string{workspace}})

	tests := []struct {
		name  string
		mount Mount
		err   error
	}{
		{"outside approved root", Mount{HostPath: outside, GuestPath: "/workspace", Mode: MountReadOnly}, ErrForbiddenMount},
		{"read write beyond writable root", Mount{HostPath: root, GuestPath: "/workspace", Mode: MountReadWrite}, ErrForbiddenMount},
		{"relative guest", Mount{HostPath: workspace, GuestPath: "workspace", Mode: MountReadOnly}, ErrInvalidSpec},
		{"forbidden guest", Mount{HostPath: workspace, GuestPath: "/etc/agent", Mode: MountReadOnly}, ErrInvalidSpec},
		{"unknown mode", Mount{HostPath: workspace, GuestPath: "/workspace", Mode: "sometimes"}, ErrInvalidSpec},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := validDockerSpec(workspace)
			spec.Mounts = []Mount{test.mount}
			if _, err := planner.Plan(spec); !errors.Is(err, test.err) {
				t.Fatalf("Plan error = %v, want %v", err, test.err)
			}
		})
	}
}

func TestResolvedSymlinkCannotEscapePolicy(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "workspace")
	outside := t.TempDir()
	link := filepath.Join(inside, "escape")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are unavailable on this platform: %v", err)
	}
	planner := mustPlanner(t, Policy{ApprovedMountRoots: []string{root}, WritableMountRoots: []string{inside}})
	spec := validDockerSpec(inside)
	spec.Mounts = []Mount{{HostPath: link, GuestPath: "/workspace", Mode: MountReadOnly}}
	if _, err := planner.Plan(spec); !errors.Is(err, ErrForbiddenMount) {
		t.Fatalf("Plan error = %v, want forbidden mount", err)
	}
}

func TestNativeDisabledByDefaultAndAppleFailsClosed(t *testing.T) {
	workspace := t.TempDir()
	native := validDockerSpec(workspace)
	native.Profile = ProfileNativeHost
	native.Image = ""
	native.User = ""
	native.Tmpfs = nil
	native.Workdir = workspace
	planner := mustPlanner(t, DefaultPolicy())
	if _, err := planner.Plan(native); !errors.Is(err, ErrNativeDisabled) {
		t.Fatalf("native default error = %v, want ErrNativeDisabled", err)
	}

	nativePlanner := mustPlanner(t, Policy{AllowNativeHost: true, ApprovedMountRoots: []string{workspace}})
	native.NativeReason = "Docker unavailable for a user-approved local diagnostic"
	plan, err := nativePlanner.Plan(native)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Native == nil || len(plan.DockerArgs) != 0 {
		t.Fatalf("native plan must not produce Docker args: %#v", plan)
	}
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Native.Workdir != resolvedWorkspace {
		t.Fatalf("native plan workdir = %q, want %q", plan.Native.Workdir, resolvedWorkspace)
	}

	apple := validDockerSpec(workspace)
	apple.Profile = ProfileAppleContainer
	apple.User = ""
	if _, err := nativePlanner.Plan(apple); !errors.Is(err, ErrProfileUnsupported) {
		t.Fatalf("apple plan error = %v, want ErrProfileUnsupported", err)
	}
}

func TestNetworkAllowlistRequiresExternalEnforcer(t *testing.T) {
	workspace := t.TempDir()
	planner := mustPlanner(t, DefaultPolicy())
	spec := validDockerSpec(workspace)
	spec.Network = NetworkPolicy{Mode: NetworkAllowlist, Allow: []EgressRule{{CIDR: "203.0.113.0/24", Protocol: "tcp", Port: 443}}}
	if _, err := planner.Plan(spec); !errors.Is(err, ErrEgressEnforcerRequired) {
		t.Fatalf("Plan error = %v, want ErrEgressEnforcerRequired", err)
	}
	spec.Network.Allow[0].CIDR = "api.example.com"
	if _, err := planner.Plan(spec); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("domain allowlist error = %v, want ErrInvalidSpec", err)
	}
}

func TestSecretReferencesAreOpaqueAndValidated(t *testing.T) {
	workspace := t.TempDir()
	planner := mustPlanner(t, DefaultPolicy())
	spec := validDockerSpec(workspace)
	spec.Secrets = []SecretReference{{Environment: "TOKEN", Source: SecretSourceHandoff, Reference: "request-1"}}
	if _, err := planner.Plan(spec); err != nil {
		t.Fatal(err)
	}
	spec.Secrets[0].Reference = "actual-secret-value=should-not-fit"
	if _, err := planner.Plan(spec); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("non-opaque reference error = %v, want ErrInvalidSpec", err)
	}
}

func TestDuplicateGuestTargetsAndUnsafeControllerRootsFail(t *testing.T) {
	workspace := t.TempDir()
	planner := mustPlanner(t, Policy{ApprovedMountRoots: []string{workspace}, WritableMountRoots: []string{workspace}})
	spec := validDockerSpec(workspace)
	spec.Mounts = []Mount{
		{HostPath: workspace, GuestPath: "/workspace", Mode: MountReadOnly},
		{HostPath: workspace, GuestPath: "/workspace", Mode: MountReadOnly},
	}
	if _, err := planner.Plan(spec); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("duplicate target error = %v, want ErrInvalidSpec", err)
	}
	if _, err := NewPlanner(Policy{ApprovedMountRoots: []string{"/"}}); !errors.Is(err, ErrForbiddenMount) {
		t.Fatalf("unsafe policy root error = %v, want ErrForbiddenMount", err)
	}
}

func TestNestedGuestTargetsAndSensitivePolicyRootsFail(t *testing.T) {
	workspace := t.TempDir()
	planner := mustPlanner(t, Policy{ApprovedMountRoots: []string{workspace}, WritableMountRoots: []string{workspace}})
	spec := validDockerSpec(workspace)
	spec.Mounts = []Mount{{HostPath: workspace, GuestPath: "/workspace", Mode: MountReadOnly}}
	spec.Tmpfs = []Tmpfs{
		{GuestPath: "/tmp", SizeBytes: 64 << 20, Mode: 0o1777},
		{GuestPath: "/tmp/cache", SizeBytes: 64 << 20, Mode: 0o700},
	}
	if _, err := planner.Plan(spec); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("nested tmpfs error = %v, want ErrInvalidSpec", err)
	}
	if _, err := NewPlanner(Policy{ApprovedMountRoots: []string{"/var/run/docker.sock"}}); !errors.Is(err, ErrForbiddenMount) {
		t.Fatalf("docker socket policy root error = %v, want ErrForbiddenMount", err)
	}
	if home, err := os.UserHomeDir(); err == nil {
		if _, err := NewPlanner(Policy{ApprovedMountRoots: []string{home}}); !errors.Is(err, ErrForbiddenMount) {
			t.Fatalf("home policy root error = %v, want ErrForbiddenMount", err)
		}
	}
}

func TestNativeHostWorkdirMustBeControllerApproved(t *testing.T) {
	approved := t.TempDir()
	outside := t.TempDir()
	planner := mustPlanner(t, Policy{AllowNativeHost: true, ApprovedMountRoots: []string{approved}})
	spec := validDockerSpec(outside)
	spec.Profile = ProfileNativeHost
	spec.Image = ""
	spec.User = ""
	spec.Tmpfs = nil
	spec.Workdir = outside
	spec.NativeReason = "explicit user-approved diagnostic"
	if _, err := planner.Plan(spec); !errors.Is(err, ErrForbiddenMount) {
		t.Fatalf("native outside-root workdir error = %v, want ErrForbiddenMount", err)
	}
}

func validDockerSpec(workspace string) Spec {
	return Spec{
		SessionID: "run_01",
		Profile:   ProfileDocker,
		Image:     "ghcr.io/openagentfleet/worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Command:   []string{"worker", "--safe"},
		Workdir:   "/workspace",
		User:      "1000:1000",
		Network:   NetworkPolicy{Mode: NetworkOff},
		Limits:    ResourceLimits{CPUMilli: 1500, MemoryBytes: 512 << 20, PIDs: 128},
		Tmpfs:     []Tmpfs{{GuestPath: "/tmp", SizeBytes: 64 << 20, Mode: 0o1777}},
	}
}

func mustPlanner(t *testing.T, policy Policy) Planner {
	t.Helper()
	planner, err := NewPlanner(policy)
	if err != nil {
		t.Fatal(err)
	}
	return planner
}

func hasArgPair(args []string, flag, value string) bool {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == flag && args[index+1] == value {
			return true
		}
	}
	return false
}
