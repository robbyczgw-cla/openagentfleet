package websearchplus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/ospath"
)

func TestValidateHoundEndpoint(t *testing.T) {
	t.Parallel()

	valid := []string{
		DefaultHoundMCPEndpoint,
		"http://127.0.0.1:49152/mcp",
		"http://[::1]:8765/mcp",
	}
	for _, endpoint := range valid {
		endpoint := endpoint
		t.Run("valid_"+strings.NewReplacer(":", "_", "/", "_").Replace(endpoint), func(t *testing.T) {
			t.Parallel()
			if err := ValidateHoundEndpoint(endpoint); err != nil {
				t.Fatalf("ValidateHoundEndpoint(%q): %v", endpoint, err)
			}
		})
	}

	invalid := []string{
		"",
		" " + DefaultHoundMCPEndpoint,
		"https://127.0.0.1:8765/mcp",
		"http://localhost:8765/mcp",
		"http://127.0.0.2:8765/mcp",
		"http://0.0.0.0:8765/mcp",
		"http://user@127.0.0.1:8765/mcp",
		"http://user:pass@127.0.0.1:8765/mcp",
		"http://127.0.0.1:8765/mcp?token=secret",
		"http://127.0.0.1:8765/mcp#fragment",
		"http://127.0.0.1:8765/mcp/",
		"http://127.0.0.1:8765/%6dcp",
		"http://127.0.0.1/mcp",
		"http://127.0.0.1:0/mcp",
		"http://127.0.0.1:70000/mcp",
	}
	for _, endpoint := range invalid {
		endpoint := endpoint
		t.Run("invalid_"+strings.NewReplacer(":", "_", "/", "_", "?", "_", "#", "_").Replace(endpoint), func(t *testing.T) {
			t.Parallel()
			if err := ValidateHoundEndpoint(endpoint); err == nil {
				t.Fatalf("ValidateHoundEndpoint(%q) succeeded", endpoint)
			}
		})
	}
}

func TestNewRejectsImplicitBridgeAndInvalidTimeouts(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	for name, config := range map[string]Config{
		"relative state": {StateDir: "relative"},
		"bridge without wsp": {
			StateDir:                stateDir,
			EnableTestedHoundBridge: true,
		},
		"managed without bridge": {
			StateDir:                 stateDir,
			ManageHoundBridgeSidecar: true,
		},
		"short probe": {
			StateDir:     stateDir,
			ProbeTimeout: time.Millisecond,
		},
		"short startup": {
			StateDir:       stateDir,
			StartupTimeout: time.Millisecond,
		},
	} {
		config := config
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(config); err == nil {
				t.Fatal("New succeeded")
			}
		})
	}
}

func TestIndependentSpecsUseExactPinsAndDefaultConfigDoesNotForceHound(t *testing.T) {
	t.Parallel()

	manager := newReadyTestManager(t, Config{
		StateDir:            t.TempDir(),
		EnableWebSearchPlus: true,
		EnableHound:         true,
	}, unreachableBridgeProbe())

	specs, err := manager.MCPServerSpecs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []MCPServerSpec{
		{
			Name:    "web-search-plus",
			Command: "/test/uvx",
			Args:    []string{"--from", "web-search-plus-mcp==3.6.0", "web-search-plus-mcp", "serve"},
			Env:     map[string]string{"WEB_SEARCH_PLUS_CONFIG": manager.ConfigPath()},
		},
		{
			Name:    "hound",
			Command: "/test/uvx",
			Args:    []string{"--from", "hound-mcp==13.1.2", "hound"},
			Env:     map[string]string{},
		},
	}
	if !reflect.DeepEqual(specs, want) {
		t.Fatalf("specs = %#v, want %#v", specs, want)
	}

	payload, err := os.ReadFile(manager.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(payload)), "hound") {
		t.Fatalf("default config silently forced Hound: %s", payload)
	}
	var config map[string]any
	if err := json.Unmarshal(payload, &config); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(config, map[string]any{"version": float64(1)}) {
		t.Fatalf("default config = %#v", config)
	}
	info, err := os.Stat(manager.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if ospath.POSIXModeEnforced() {
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("config mode = %#o, want 0600", got)
		}
	}

	// Returned specs are defensive copies.
	specs[0].Args[0] = "changed"
	specs[0].Env["SECRET"] = "bad"
	again, err := manager.MCPServerSpecs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Args[0] != "--from" || again[0].Env["SECRET"] != "" {
		t.Fatalf("spec mutation escaped: %#v", again[0])
	}
}

func TestDonsetchSpecUsesExactNpxPinAndStaysIndependent(t *testing.T) {
	t.Parallel()

	manager := newReadyTestManager(t, Config{
		StateDir:       t.TempDir(),
		EnableDonsetch: true,
	}, unreachableBridgeProbe())
	specs, err := manager.MCPServerSpecs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []MCPServerSpec{
		{
			Name:    "donsetch",
			Command: "/test/npx",
			Args:    []string{"--yes", "donsetch@2.1.0", "mcp"},
			Env:     map[string]string{},
		},
	}
	if !reflect.DeepEqual(specs, want) {
		t.Fatalf("specs = %#v, want %#v", specs, want)
	}
	if _, err := os.Stat(manager.ConfigPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("donsetch prepared WSP config: %v", err)
	}
}

func TestBridgeSpecRequiresExactCompatibilityAndWritesFixedConfig(t *testing.T) {
	t.Parallel()

	unready := newReadyTestManager(t, Config{
		StateDir:                t.TempDir(),
		EnableWebSearchPlus:     true,
		EnableHound:             true,
		EnableTestedHoundBridge: true,
	}, bridgeProbe{reachable: true, serverName: "Hound", serverVersion: "13.1.1", detail: "wrong version"})
	unreadySpecs, err := unready.MCPServerSpecs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(unreadySpecs) != 1 || unreadableName(unreadySpecs[0]) != "hound" {
		t.Fatalf("unready bridge specs = %#v", unreadySpecs)
	}
	if _, err := os.Stat(unready.ConfigPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bridge config was produced without compatibility: %v", err)
	}

	readyProbe := compatibleBridgeProbe()
	manager := newReadyTestManager(t, Config{
		StateDir:                t.TempDir(),
		EnableWebSearchPlus:     true,
		EnableHound:             true,
		EnableTestedHoundBridge: true,
	}, readyProbe)
	specs, err := manager.MCPServerSpecs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("spec count = %d: %#v", len(specs), specs)
	}
	if got := specs[0].Env; !reflect.DeepEqual(got, map[string]string{
		"WEB_SEARCH_PLUS_CONFIG": manager.ConfigPath(),
		"HOUND_MCP_URL":          DefaultHoundMCPEndpoint,
	}) {
		t.Fatalf("WSP bridge env = %#v", got)
	}
	if len(specs[1].Env) != 0 {
		t.Fatalf("direct Hound spec inherited bridge env: %#v", specs[1].Env)
	}

	data, err := os.ReadFile(manager.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Version         int    `json:"version"`
		DefaultProvider string `json:"default_provider"`
		Defaults        struct {
			Provider string `json:"provider"`
		} `json:"defaults"`
		AutoRouting struct {
			Enabled                 bool            `json:"enabled"`
			FallbackProvider        string          `json:"fallback_provider"`
			ProviderPriority        []string        `json:"provider_priority"`
			ExtractProviderPriority []string        `json:"extract_provider_priority"`
			DisabledProviders       []string        `json:"disabled_providers"`
			AutoAllow               map[string]bool `json:"auto_allow"`
		} `json:"auto_routing"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Version != 1 || config.DefaultProvider != "hound" || config.Defaults.Provider != "hound" {
		t.Fatalf("fixed provider config = %#v", config)
	}
	if config.AutoRouting.Enabled || config.AutoRouting.FallbackProvider != "hound" ||
		!reflect.DeepEqual(config.AutoRouting.ProviderPriority, []string{"hound"}) ||
		!reflect.DeepEqual(config.AutoRouting.ExtractProviderPriority, []string{"hound"}) ||
		len(config.AutoRouting.DisabledProviders) != 0 || !config.AutoRouting.AutoAllow["hound"] {
		t.Fatalf("fixed routing config = %#v", config.AutoRouting)
	}
}

func TestAtomicConfigPreparationIsRaceSafe(t *testing.T) {
	t.Parallel()

	manager := newReadyTestManager(t, Config{
		StateDir:                t.TempDir(),
		EnableWebSearchPlus:     true,
		EnableTestedHoundBridge: true,
	}, compatibleBridgeProbe())

	const workers = 32
	errorsSeen := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := manager.MCPServerSpecs(context.Background())
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(manager.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("final config is not atomic JSON: %v\n%s", err, data)
	}
	if config["default_provider"] != "hound" {
		t.Fatalf("final config = %#v", config)
	}
	if matches, err := filepathGlobForTest(manager.config.StateDir, ".web-search-plus-"); err != nil || len(matches) != 0 {
		t.Fatalf("temporary config files = %v, err = %v", matches, err)
	}
}

func TestConfigPreparationRejectsSymlinkStateDirectory(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	realDirectory := t.TempDir()
	linkedDirectory := parent + "/linked-state"
	if err := os.Symlink(realDirectory, linkedDirectory); err != nil {
		t.Fatal(err)
	}
	manager := newReadyTestManager(t, Config{
		StateDir:            linkedDirectory,
		EnableWebSearchPlus: true,
	}, unreachableBridgeProbe())
	if _, err := manager.MCPServerSpecs(context.Background()); err == nil {
		t.Fatal("configuration followed a symlink state directory")
	}
}

func filepathGlobForTest(directory, prefix string) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	var matches []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			matches = append(matches, entry.Name())
		}
	}
	return matches, nil
}

func TestStatusKeepsConnectorsIndependentAndRedactsProbeFailures(t *testing.T) {
	t.Parallel()

	deps := readyTestDeps(compatibleBridgeProbe())
	deps.runVersion = func(_ context.Context, path string, _ ...string) (string, error) {
		if strings.Contains(path, "uvx") {
			return "API_KEY=super-secret", errors.New("TOKEN=do-not-leak")
		}
		return "unexpected", nil
	}
	deps.localHoundVersion = func(context.Context, string) (string, error) { return "13.1.1", nil }
	manager, err := newManager(Config{
		StateDir:                t.TempDir(),
		EnableWebSearchPlus:     true,
		EnableHound:             true,
		EnableTestedHoundBridge: true,
	}, deps)
	if err != nil {
		t.Fatal(err)
	}
	status := manager.Status(context.Background())
	if status.UVX.Available != true || status.UVX.Version != "" || status.WebSearchPlus.Ready || status.Hound.Ready {
		t.Fatalf("status readiness = %#v", status)
	}
	if status.LocalHoundCLI.Version != "13.1.1" || status.LocalHoundCLI.Exact {
		t.Fatalf("local Hound status = %#v", status.LocalHoundCLI)
	}
	encoded, _ := json.Marshal(status)
	if strings.Contains(string(encoded), "super-secret") || strings.Contains(string(encoded), "do-not-leak") {
		t.Fatalf("status leaked probe output: %s", encoded)
	}
	if !status.Bridge.Compatible || status.Bridge.ServerVersion != HoundMCPVersion {
		t.Fatalf("bridge status = %#v", status.Bridge)
	}
}

func TestRealBridgeProbeChecksVersionAndRequiredTools(t *testing.T) {
	t.Parallel()

	for name, scenario := range map[string]struct {
		version    string
		tools      []string
		compatible bool
	}{
		"compatible": {
			version: HoundMCPVersion, tools: []string{"mcp_smart_fetch", "mcp_smart_search", "version"}, compatible: true,
		},
		"wrong version": {
			version: "13.1.1", tools: []string{"mcp_smart_fetch", "mcp_smart_search"}, compatible: false,
		},
		"missing fetch": {
			version: HoundMCPVersion, tools: []string{"mcp_smart_search"}, compatible: false,
		},
	} {
		scenario := scenario
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := newFakeHoundHTTPServer(t, scenario.version, scenario.tools)
			defer server.Close()
			probe := probeBridgeCompatibility(context.Background(), server.URL+"/mcp", time.Second)
			if !probe.reachable || probe.compatible != scenario.compatible {
				t.Fatalf("probe = %#v", probe)
			}
			if probe.serverVersion != scenario.version {
				t.Fatalf("server version = %q", probe.serverVersion)
			}
		})
	}
}

func TestBridgeProbeDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		targetCalls.Add(1)
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Redirect(writer, &http.Request{}, target.URL+"/mcp", http.StatusFound)
	}))
	defer redirect.Close()

	probe := probeBridgeCompatibility(context.Background(), redirect.URL+"/mcp", time.Second)
	if !probe.reachable || probe.compatible {
		t.Fatalf("redirect probe = %#v", probe)
	}
	if targetCalls.Load() != 0 {
		t.Fatalf("redirect target called %d times", targetCalls.Load())
	}
}

func TestManagedChildExactArgsOwnershipAndStop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed child process groups are unix-only")
	}
	if testing.Short() {
		t.Skip("spawns the test helper process")
	}

	var commandMu sync.Mutex
	var commandPath string
	var commandArgs []string
	deps := readyTestDeps(compatibleBridgeProbe())
	deps.tcpReachable = func(context.Context, string, time.Duration) (bool, error) { return false, nil }
	deps.command = func(ctx context.Context, path string, args ...string) *exec.Cmd {
		commandMu.Lock()
		commandPath = path
		commandArgs = append([]string(nil), args...)
		commandMu.Unlock()
		return exec.CommandContext(ctx, os.Args[0], "-test.run=TestOwnedChildHelper", "--", "owned-child")
	}
	manager, err := newManager(Config{
		StateDir:                 t.TempDir(),
		EnableWebSearchPlus:      true,
		EnableTestedHoundBridge:  true,
		ManageHoundBridgeSidecar: true,
		StartupTimeout:           2 * time.Second,
	}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartHoundBridge(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.StartHoundBridge(context.Background()); err == nil {
		t.Fatal("second start succeeded")
	}
	commandMu.Lock()
	gotPath := commandPath
	gotArgs := append([]string(nil), commandArgs...)
	commandMu.Unlock()
	if gotPath != "/test/hound" {
		t.Fatalf("command path = %q", gotPath)
	}
	wantArgs := []string{
		"--http", "--host", "127.0.0.1", "--port", "8765",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("child args = %#v, want %#v", gotArgs, wantArgs)
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("idempotent Stop: %v", err)
	}
}

func TestManagedChildRequiresInstalledExactHoundWithoutLaunchingUvx(t *testing.T) {
	t.Parallel()

	var commandCalls atomic.Int32
	deps := readyTestDeps(compatibleBridgeProbe())
	deps.tcpReachable = func(context.Context, string, time.Duration) (bool, error) { return false, nil }
	deps.localHoundVersion = func(context.Context, string) (string, error) { return "13.1.1", nil }
	deps.command = func(ctx context.Context, path string, args ...string) *exec.Cmd {
		commandCalls.Add(1)
		return exec.CommandContext(ctx, path, args...)
	}
	manager, err := newManager(Config{
		StateDir:                 t.TempDir(),
		EnableWebSearchPlus:      true,
		EnableTestedHoundBridge:  true,
		ManageHoundBridgeSidecar: true,
	}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartHoundBridge(context.Background()); err == nil || !strings.Contains(err.Error(), "exact managed-bridge pin") {
		t.Fatalf("StartHoundBridge error = %v", err)
	}
	if commandCalls.Load() != 0 {
		t.Fatalf("version mismatch launched %d processes", commandCalls.Load())
	}
}

func TestOwnedChildHelper(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "owned-child" {
		return
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestManagerRefusesToAdoptExistingSidecar(t *testing.T) {
	t.Parallel()

	var commandCalls atomic.Int32
	deps := readyTestDeps(compatibleBridgeProbe())
	deps.tcpReachable = func(context.Context, string, time.Duration) (bool, error) { return true, nil }
	deps.command = func(ctx context.Context, path string, args ...string) *exec.Cmd {
		commandCalls.Add(1)
		return exec.CommandContext(ctx, path, args...)
	}
	manager, err := newManager(Config{
		StateDir:                 t.TempDir(),
		EnableWebSearchPlus:      true,
		EnableTestedHoundBridge:  true,
		ManageHoundBridgeSidecar: true,
	}, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.StartHoundBridge(context.Background()); err == nil || !strings.Contains(err.Error(), "refusing to adopt") {
		t.Fatalf("StartHoundBridge error = %v", err)
	}
	if commandCalls.Load() != 0 {
		t.Fatalf("external endpoint triggered %d child starts", commandCalls.Load())
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestVersionProbeUsesBoundedSanitizedEnvironment(t *testing.T) {
	t.Setenv("WEBSEARCHPLUS_TEST_SECRET", "must-not-reach-child")
	output, err := runBoundedVersion(context.Background(), os.Args[0], "-test.run=TestVersionProbeHelper", "--", "version-helper")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "13.1.2") {
		t.Fatalf("version output = %q", output)
	}

	buffer := &boundedBuffer{limit: 8}
	payload := []byte("1234567890-secret")
	if written, err := buffer.Write(payload); err != nil || written != len(payload) {
		t.Fatalf("bounded write = %d, %v", written, err)
	}
	if got := buffer.String(); got != "12345678 [truncated]" {
		t.Fatalf("bounded output = %q", got)
	}
}

func TestLocalHoundVersionProbeRejectsExecutableShebangArguments(t *testing.T) {
	t.Parallel()

	launcher := t.TempDir() + "/hound"
	if err := os.WriteFile(launcher, []byte("#!/usr/bin/python3 -c malicious_code\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readLocalHoundDistributionVersion(context.Background(), launcher); err == nil {
		t.Fatal("unsafe Hound launcher shebang was executed")
	}
}

func TestVersionProbeHelper(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "version-helper" {
		return
	}
	if os.Getenv("WEBSEARCHPLUS_TEST_SECRET") != "" {
		os.Exit(9)
	}
	fmt.Println("Hound 13.1.2")
}

func TestAttributionMetadataIsPinned(t *testing.T) {
	t.Parallel()

	if WebSearchPlusMCPVersion != "3.6.0" || WebSearchPlusReleaseTag != "v3.6.0" ||
		WebSearchPlusReleaseCommit != "13e589ac38ef73da3292b1286191bf922a514d31" ||
		WebSearchPlusUpstreamURL != "https://github.com/robbyczgw-cla/web-search-plus-mcp" || WebSearchPlusLicense != "MIT" {
		t.Fatal("Web Search Plus attribution metadata drifted")
	}
	if HoundMCPVersion != "13.1.2" || HoundReleaseTag != "v13.1.2" ||
		HoundReleaseCommit != "6c7299974870752a1d25aaf4b5727cc7d91bbaa7" ||
		HoundUpstreamURL != "https://github.com/dondai44423/master-fetch" || HoundLicense != "MIT" {
		t.Fatal("Hound attribution metadata drifted")
	}
	if DonsetchMCPVersion != "2.1.0" || DonsetchReleaseTag != "v2.1.0" ||
		DonsetchReleaseCommit != "2753878cc1f46558f9b9bd50c87cc9efc9bdafba" ||
		DonsetchUpstreamURL != "https://github.com/dondai44423/donsetch" || DonsetchLicense != "AGPL-3.0-only" {
		t.Fatal("Donsetch attribution metadata drifted")
	}
}

func newReadyTestManager(t *testing.T, config Config, probe bridgeProbe) *Manager {
	t.Helper()
	manager, err := newManager(config, readyTestDeps(probe))
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func readyTestDeps(probe bridgeProbe) managerDeps {
	return managerDeps{
		lookPath: func(name string) (string, error) {
			switch name {
			case "uvx":
				return "/test/uvx", nil
			case "npx":
				return "/test/npx", nil
			case "hound":
				return "/test/hound", nil
			case "donsetch":
				return "/test/donsetch", nil
			default:
				return "", exec.ErrNotFound
			}
		},
		runVersion: func(_ context.Context, path string, _ ...string) (string, error) {
			if strings.Contains(path, "uvx") {
				return "uvx 0.9.0", nil
			}
			if strings.Contains(path, "npx") {
				return "10.9.2", nil
			}
			return "Hound " + HoundMCPVersion, nil
		},
		localHoundVersion: func(context.Context, string) (string, error) {
			return HoundMCPVersion, nil
		},
		command: exec.CommandContext,
		tcpReachable: func(context.Context, string, time.Duration) (bool, error) {
			return probe.reachable, nil
		},
		probeBridge: func(context.Context, string, time.Duration) bridgeProbe {
			cloned := probe
			cloned.tools = append([]string(nil), probe.tools...)
			return cloned
		},
	}
}

func compatibleBridgeProbe() bridgeProbe {
	return bridgeProbe{
		reachable:       true,
		compatible:      true,
		serverName:      "Hound",
		serverVersion:   HoundMCPVersion,
		protocolVersion: "2026-07-28",
		tools:           []string{"mcp_smart_fetch", "mcp_smart_search"},
		detail:          "exact Hound version and WSP bridge tools verified",
	}
}

func unreachableBridgeProbe() bridgeProbe {
	return bridgeProbe{detail: "not reachable"}
}

func unreadableName(spec MCPServerSpec) string {
	return spec.Name
}

func newFakeHoundHTTPServer(t *testing.T, version string, tools []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/mcp" {
			http.NotFound(writer, request)
			return
		}
		if request.Method == http.MethodDelete {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		var message struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(request.Body).Decode(&message); err != nil {
			http.Error(writer, "bad JSON", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		switch message.Method {
		case "initialize":
			writer.Header().Set("Mcp-Session-Id", "test-session")
			_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2026-07-28","serverInfo":{"name":"Hound","version":%q},"capabilities":{"tools":{}}}}`, version)
		case "notifications/initialized":
			if request.Header.Get("Mcp-Session-Id") != "test-session" {
				http.Error(writer, "missing session", http.StatusBadRequest)
				return
			}
			writer.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if request.Header.Get("Mcp-Session-Id") != "test-session" {
				http.Error(writer, "missing session", http.StatusBadRequest)
				return
			}
			items := make([]map[string]string, 0, len(tools))
			for _, tool := range tools {
				items = append(items, map[string]string{"name": tool})
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      2,
				"result":  map[string]any{"tools": items},
			})
		default:
			http.Error(writer, "unknown method", http.StatusBadRequest)
		}
	}))
}
