// Package websearchplus manages the optional, first-party Web Search Plus and
// Hound MCP launch contracts. It never installs either dependency and never
// adopts or terminates a process it did not start.
package websearchplus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// WebSearchPlusMCPVersion is the exact first-party MCP package version.
	WebSearchPlusMCPVersion = "3.6.0"
	// WebSearchPlusUpstreamURL is the canonical first-party source repository.
	WebSearchPlusUpstreamURL = "https://github.com/robbyczgw-cla/web-search-plus-mcp"
	// WebSearchPlusReleaseTag is the audited upstream release tag.
	WebSearchPlusReleaseTag = "v3.6.0"
	// WebSearchPlusReleaseCommit is the commit referenced by the annotated tag.
	WebSearchPlusReleaseCommit = "13e589ac38ef73da3292b1286191bf922a514d31"
	// WebSearchPlusLicense is the upstream software license identifier.
	WebSearchPlusLicense = "MIT"

	// HoundMCPVersion is the exact Hound compatibility and launch pin. Version
	// 13.1.2 includes the upstream redirect and DNS SSRF fixes.
	HoundMCPVersion = "13.1.2"
	// HoundUpstreamURL is the canonical Hound source repository.
	HoundUpstreamURL = "https://github.com/dondai44423/master-fetch"
	// HoundReleaseTag is the audited upstream release tag.
	HoundReleaseTag = "v13.1.2"
	// HoundReleaseCommit is the commit referenced by the annotated tag.
	HoundReleaseCommit = "6c7299974870752a1d25aaf4b5727cc7d91bbaa7"
	// HoundLicense is the upstream software license identifier.
	HoundLicense = "MIT"

	// DefaultHoundMCPEndpoint is the only endpoint started by Manager. A caller
	// may probe another explicitly configured literal loopback port.
	DefaultHoundMCPEndpoint = "http://127.0.0.1:8765/mcp"

	webSearchPlusServerName = "web-search-plus"
	houndServerName         = "hound"
	configFilename          = "web-search-plus.json"
	maxProbeOutputBytes     = 8 << 10
	maxHTTPProbeBytes       = 128 << 10
	maxChildOutputBytes     = 64 << 10
	defaultProbeTimeout     = 3 * time.Second
	defaultStartupTimeout   = 30 * time.Second
)

var (
	semverPattern      = regexp.MustCompile(`\b\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?\b`)
	credentialPattern  = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|authorization|bearer|client[_-]?secret|password|passwd|private[_-]?key|secret|token)\s*[:=]\s*[^\s,;]+`)
	commonTokenPattern = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{8,}|xai-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9_]{8,}|xox[baprs]-[A-Za-z0-9-]{8,})\b`)
)

// MCPServerSpec is a credential-free stdio MCP launch contract. Env contains
// only connector routing values owned by this package, never provider secrets.
type MCPServerSpec struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

// ExecutableStatus reports a bounded local executable probe. Path and raw
// command output are intentionally not exposed.
type ExecutableStatus struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Exact     bool   `json:"exact,omitempty"`
	Detail    string `json:"detail,omitempty"`
	path      string
}

// ConnectorStatus is the independently enabled/readied state of one MCP.
type ConnectorStatus struct {
	Enabled bool   `json:"enabled"`
	Ready   bool   `json:"ready"`
	Version string `json:"version"`
	Detail  string `json:"detail,omitempty"`
}

// BridgeStatus describes the explicit, optional WSP-to-Hound HTTP bridge.
// Compatible is true only after an MCP initialize/tools handshake against the
// exact audited Hound version and the tools consumed by WSP.
type BridgeStatus struct {
	Selected        bool     `json:"selected"`
	Managed         bool     `json:"managed"`
	OwnedChild      bool     `json:"owned_child"`
	Endpoint        string   `json:"endpoint,omitempty"`
	Reachable       bool     `json:"reachable"`
	Compatible      bool     `json:"compatible"`
	ServerName      string   `json:"server_name,omitempty"`
	ServerVersion   string   `json:"server_version,omitempty"`
	ProtocolVersion string   `json:"protocol_version,omitempty"`
	Tools           []string `json:"tools,omitempty"`
	Detail          string   `json:"detail,omitempty"`
}

// Status is a race-safe snapshot. Web Search Plus and direct Hound are
// independent connectors; selecting the bridge never enables direct Hound.
type Status struct {
	UVX           ExecutableStatus `json:"uvx"`
	LocalHoundCLI ExecutableStatus `json:"local_hound_cli"`
	WebSearchPlus ConnectorStatus  `json:"web_search_plus"`
	Hound         ConnectorStatus  `json:"hound"`
	Bridge        BridgeStatus     `json:"bridge"`
	ConfigPath    string           `json:"config_path"`
}

// Config controls only explicit optional runtime behavior. Both MCP servers
// default to disabled. EnableTestedHoundBridge fixes WSP routing to Hound only
// after a successful compatibility probe; it is never inferred from Hound's
// direct-MCP toggle.
type Config struct {
	StateDir                 string
	EnableWebSearchPlus      bool
	EnableHound              bool
	EnableTestedHoundBridge  bool
	ManageHoundBridgeSidecar bool
	HoundEndpoint            string
	ProbeTimeout             time.Duration
	StartupTimeout           time.Duration
}

type bridgeProbe struct {
	reachable       bool
	compatible      bool
	serverName      string
	serverVersion   string
	protocolVersion string
	tools           []string
	detail          string
}

type managerDeps struct {
	lookPath          func(string) (string, error)
	runVersion        func(context.Context, string, ...string) (string, error)
	localHoundVersion func(context.Context, string) (string, error)
	command           func(context.Context, string, ...string) *exec.Cmd
	tcpReachable      func(context.Context, string, time.Duration) (bool, error)
	probeBridge       func(context.Context, string, time.Duration) bridgeProbe
}

type ownedChild struct {
	command *exec.Cmd
	cancel  context.CancelFunc
	done    chan error
	stderr  *boundedBuffer
}

// Manager validates dependencies, prepares WSP configuration and optionally
// owns one explicitly started Hound HTTP bridge child.
type Manager struct {
	config Config
	deps   managerDeps

	configMu sync.Mutex
	childMu  sync.Mutex
	child    *ownedChild
}

// New validates configuration without creating files, downloading packages,
// starting processes, or contacting the sidecar.
func New(config Config) (*Manager, error) {
	return newManager(config, managerDeps{
		lookPath:          exec.LookPath,
		runVersion:        runBoundedVersion,
		localHoundVersion: readLocalHoundDistributionVersion,
		command:           exec.CommandContext,
		tcpReachable:      probeTCP,
		probeBridge:       probeBridgeCompatibility,
	})
}

func newManager(config Config, deps managerDeps) (*Manager, error) {
	if !filepath.IsAbs(config.StateDir) {
		return nil, errors.New("websearchplus: state directory must be absolute")
	}
	if config.HoundEndpoint == "" {
		config.HoundEndpoint = DefaultHoundMCPEndpoint
	}
	if err := ValidateHoundEndpoint(config.HoundEndpoint); err != nil {
		return nil, err
	}
	if config.EnableTestedHoundBridge && !config.EnableWebSearchPlus {
		return nil, errors.New("websearchplus: Hound bridge requires Web Search Plus")
	}
	if config.ManageHoundBridgeSidecar && !config.EnableTestedHoundBridge {
		return nil, errors.New("websearchplus: managed sidecar requires the explicit tested bridge")
	}
	if config.ProbeTimeout == 0 {
		config.ProbeTimeout = defaultProbeTimeout
	}
	if config.ProbeTimeout < 100*time.Millisecond || config.ProbeTimeout > 30*time.Second {
		return nil, errors.New("websearchplus: probe timeout must be between 100ms and 30s")
	}
	if config.StartupTimeout == 0 {
		config.StartupTimeout = defaultStartupTimeout
	}
	if config.StartupTimeout < time.Second || config.StartupTimeout > 2*time.Minute {
		return nil, errors.New("websearchplus: startup timeout must be between 1s and 2m")
	}
	if deps.lookPath == nil || deps.runVersion == nil || deps.localHoundVersion == nil || deps.command == nil || deps.tcpReachable == nil || deps.probeBridge == nil {
		return nil, errors.New("websearchplus: incomplete runtime dependencies")
	}
	return &Manager{config: config, deps: deps}, nil
}

// ConfigPath returns the app-owned canonical WSP configuration path.
func (m *Manager) ConfigPath() string {
	if m == nil {
		return ""
	}
	return filepath.Join(m.config.StateDir, configFilename)
}

// ValidateHoundEndpoint accepts only uncredentialed HTTP endpoints at /mcp on
// a literal IPv4/IPv6 loopback address with an explicit port.
func ValidateHoundEndpoint(endpoint string) error {
	if endpoint == "" || endpoint != strings.TrimSpace(endpoint) {
		return errors.New("websearchplus: invalid Hound endpoint")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return errors.New("websearchplus: Hound endpoint must be uncredentialed loopback HTTP")
	}
	if parsed.Path != "/mcp" || parsed.RawPath != "" {
		return errors.New("websearchplus: Hound endpoint path must be exactly /mcp")
	}
	host := parsed.Hostname()
	if host != "127.0.0.1" && host != "::1" {
		return errors.New("websearchplus: Hound endpoint host must be a literal loopback address")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("websearchplus: Hound endpoint requires an explicit valid port")
	}
	return nil
}

// Status probes local executables concurrently and, only when explicitly
// selected, probes the Hound HTTP bridge. It never launches a pinned package.
func (m *Manager) Status(ctx context.Context) Status {
	if m == nil {
		return Status{}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var uvxStatus, houndCLIStatus ExecutableStatus
	var bridge BridgeStatus
	var probes sync.WaitGroup
	probes.Add(2)
	go func() {
		defer probes.Done()
		uvxStatus = m.probeExecutable(ctx, "uvx", "--version", "")
	}()
	go func() {
		defer probes.Done()
		houndCLIStatus = m.probeLocalHound(ctx)
	}()

	bridge = BridgeStatus{
		Selected: m.config.EnableTestedHoundBridge,
		Managed:  m.config.ManageHoundBridgeSidecar,
		Endpoint: endpointWhenSelected(m.config),
	}
	m.childMu.Lock()
	bridge.OwnedChild = m.child != nil
	m.childMu.Unlock()

	if bridge.Selected {
		probes.Add(1)
		go func() {
			defer probes.Done()
			reachable, err := m.deps.tcpReachable(ctx, m.config.HoundEndpoint, m.config.ProbeTimeout)
			if err != nil {
				bridge.Detail = safeDetail(err.Error())
				return
			}
			bridge.Reachable = reachable
			if !reachable {
				bridge.Detail = "Hound bridge is not reachable"
				return
			}
			probe := m.deps.probeBridge(ctx, m.config.HoundEndpoint, m.config.ProbeTimeout)
			bridge.Reachable = probe.reachable
			bridge.Compatible = probe.compatible
			bridge.ServerName = probe.serverName
			bridge.ServerVersion = probe.serverVersion
			bridge.ProtocolVersion = probe.protocolVersion
			bridge.Tools = append([]string(nil), probe.tools...)
			bridge.Detail = safeDetail(probe.detail)
		}()
	}
	probes.Wait()

	status := Status{
		UVX:           uvxStatus,
		LocalHoundCLI: houndCLIStatus,
		WebSearchPlus: ConnectorStatus{Enabled: m.config.EnableWebSearchPlus, Version: WebSearchPlusMCPVersion},
		Hound:         ConnectorStatus{Enabled: m.config.EnableHound, Version: HoundMCPVersion},
		Bridge:        bridge,
		ConfigPath:    m.ConfigPath(),
	}
	status.WebSearchPlus.Ready = status.WebSearchPlus.Enabled && uvxStatus.Available && uvxStatus.Version != ""
	if status.WebSearchPlus.Ready && bridge.Selected {
		status.WebSearchPlus.Ready = bridge.Compatible
	}
	status.Hound.Ready = status.Hound.Enabled && uvxStatus.Available && uvxStatus.Version != ""
	status.WebSearchPlus.Detail = connectorDetail(status.WebSearchPlus, bridge.Selected, bridge.Compatible)
	status.Hound.Detail = connectorDetail(status.Hound, false, false)
	return status
}

func endpointWhenSelected(config Config) string {
	if config.EnableTestedHoundBridge {
		return config.HoundEndpoint
	}
	return ""
}

func connectorDetail(status ConnectorStatus, bridgeSelected, bridgeCompatible bool) string {
	if !status.Enabled {
		return "disabled"
	}
	if status.Ready {
		return "ready"
	}
	if bridgeSelected && !bridgeCompatible {
		return "waiting for an exact compatible Hound bridge"
	}
	return "uvx is unavailable or its version could not be verified"
}

func (m *Manager) probeExecutable(ctx context.Context, name, versionArg, exactVersion string) ExecutableStatus {
	path, err := m.deps.lookPath(name)
	if err != nil {
		return ExecutableStatus{Detail: name + " not found"}
	}
	probeContext, cancel := context.WithTimeout(ctx, m.config.ProbeTimeout)
	defer cancel()
	output, err := m.deps.runVersion(probeContext, path, versionArg)
	if err != nil {
		return ExecutableStatus{Available: true, Detail: name + " version probe failed"}
	}
	version := semverPattern.FindString(output)
	if version == "" {
		return ExecutableStatus{Available: true, Detail: name + " returned no bounded semantic version"}
	}
	result := ExecutableStatus{Available: true, Version: version, path: path}
	if exactVersion != "" {
		result.Exact = version == exactVersion
		if !result.Exact {
			result.Detail = "local executable differs from the audited pin; launch specs still use the exact uvx pin"
		}
	}
	return result
}

func (m *Manager) probeLocalHound(ctx context.Context) ExecutableStatus {
	path, err := m.deps.lookPath("hound")
	if err != nil {
		return ExecutableStatus{Detail: "hound not found"}
	}
	probeContext, cancel := context.WithTimeout(ctx, m.config.ProbeTimeout)
	defer cancel()
	output, err := m.deps.localHoundVersion(probeContext, path)
	if err != nil {
		return ExecutableStatus{
			Available: true,
			Detail:    "hound found; package version was not executed because the launcher may self-repair",
		}
	}
	version := semverPattern.FindString(output)
	if version == "" {
		return ExecutableStatus{Available: true, Detail: "hound package metadata returned no bounded semantic version"}
	}
	result := ExecutableStatus{Available: true, Version: version, Exact: version == HoundMCPVersion}
	if !result.Exact {
		result.Detail = "local executable differs from the audited pin; launch specs still use the exact uvx pin"
	}
	return result
}

// MCPServerSpecs returns only enabled and currently ready specs. WSP and Hound
// are evaluated independently. Preparing WSP writes its canonical config
// atomically; direct Hound never depends on the HTTP bridge.
func (m *Manager) MCPServerSpecs(ctx context.Context) ([]MCPServerSpec, error) {
	if m == nil {
		return nil, errors.New("websearchplus: nil manager")
	}
	status := m.Status(ctx)
	result := make([]MCPServerSpec, 0, 2)
	if status.WebSearchPlus.Ready {
		configPath, err := m.prepareWebSearchPlusConfig(m.config.EnableTestedHoundBridge)
		if err != nil {
			return nil, err
		}
		env := map[string]string{"WEB_SEARCH_PLUS_CONFIG": configPath}
		if m.config.EnableTestedHoundBridge {
			env["HOUND_MCP_URL"] = m.config.HoundEndpoint
		}
		result = append(result, MCPServerSpec{
			Name:    webSearchPlusServerName,
			Command: status.UVX.path,
			Args:    []string{"--from", "web-search-plus-mcp==" + WebSearchPlusMCPVersion, "web-search-plus-mcp", "serve"},
			Env:     env,
		})
	}
	if status.Hound.Ready {
		result = append(result, MCPServerSpec{
			Name:    houndServerName,
			Command: status.UVX.path,
			Args:    []string{"--from", "hound-mcp==" + HoundMCPVersion, "hound"},
			Env:     map[string]string{},
		})
	}
	return cloneSpecs(result), nil
}

type webSearchPlusConfig struct {
	Version         int                   `json:"version"`
	DefaultProvider string                `json:"default_provider,omitempty"`
	Defaults        *webSearchDefaults    `json:"defaults,omitempty"`
	AutoRouting     *webSearchAutoRouting `json:"auto_routing,omitempty"`
}

type webSearchDefaults struct {
	Provider string `json:"provider"`
}

type webSearchAutoRouting struct {
	Enabled                 bool            `json:"enabled"`
	FallbackProvider        string          `json:"fallback_provider"`
	ProviderPriority        []string        `json:"provider_priority"`
	ExtractProviderPriority []string        `json:"extract_provider_priority"`
	DisabledProviders       []string        `json:"disabled_providers"`
	AutoAllow               map[string]bool `json:"auto_allow"`
}

func (m *Manager) prepareWebSearchPlusConfig(houndBridge bool) (string, error) {
	m.configMu.Lock()
	defer m.configMu.Unlock()

	config := webSearchPlusConfig{Version: 1}
	if houndBridge {
		config.DefaultProvider = "hound"
		config.Defaults = &webSearchDefaults{Provider: "hound"}
		config.AutoRouting = &webSearchAutoRouting{
			Enabled:                 false,
			FallbackProvider:        "hound",
			ProviderPriority:        []string{"hound"},
			ExtractProviderPriority: []string{"hound"},
			DisabledProviders:       []string{},
			AutoAllow:               map[string]bool{"hound": true},
		}
	}
	payload, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", fmt.Errorf("websearchplus: encode config: %w", err)
	}
	payload = append(payload, '\n')
	path := m.ConfigPath()
	if err := atomicWritePrivate(path, payload); err != nil {
		return "", fmt.Errorf("websearchplus: write config: %w", err)
	}
	return path, nil
}

func atomicWritePrivate(path string, payload []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("state directory is not a regular directory")
	}
	temporary, err := os.CreateTemp(directory, ".web-search-plus-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	if directoryHandle, err := os.Open(directory); err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}

// StartHoundBridge starts an already-installed, exact-version Hound HTTP child
// only when bridge management was explicitly enabled. It never invokes uvx or
// an installer and refuses to adopt an existing listener.
func (m *Manager) StartHoundBridge(ctx context.Context) error {
	if m == nil {
		return errors.New("websearchplus: nil manager")
	}
	if !m.config.ManageHoundBridgeSidecar {
		return errors.New("websearchplus: managed Hound bridge is not enabled")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.childMu.Lock()
	if m.child != nil {
		m.childMu.Unlock()
		return errors.New("websearchplus: Hound bridge child is already running")
	}
	m.childMu.Unlock()

	reachable, err := m.deps.tcpReachable(ctx, m.config.HoundEndpoint, m.config.ProbeTimeout)
	if err != nil {
		return fmt.Errorf("websearchplus: preflight Hound endpoint: %w", err)
	}
	if reachable {
		return errors.New("websearchplus: Hound endpoint is already in use; refusing to adopt it")
	}
	houndPath, err := m.deps.lookPath("hound")
	if err != nil {
		return errors.New("websearchplus: an installed Hound launcher is required for the managed bridge")
	}
	versionContext, versionCancel := context.WithTimeout(ctx, m.config.ProbeTimeout)
	versionOutput, versionErr := m.deps.localHoundVersion(versionContext, houndPath)
	versionCancel()
	if versionErr != nil || semverPattern.FindString(versionOutput) != HoundMCPVersion {
		return errors.New("websearchplus: installed Hound does not match the exact managed-bridge pin")
	}

	parsed, _ := url.Parse(m.config.HoundEndpoint)
	childContext, childCancel := context.WithCancel(context.Background())
	args := []string{
		"--http", "--host", parsed.Hostname(), "--port", parsed.Port(),
	}
	command := m.deps.command(childContext, houndPath, args...)
	stdout := &boundedBuffer{limit: maxChildOutputBytes}
	stderr := &boundedBuffer{limit: maxChildOutputBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = safeRuntimeEnvironment()
	if err := command.Start(); err != nil {
		childCancel()
		return fmt.Errorf("websearchplus: start Hound bridge: %w", err)
	}
	child := &ownedChild{command: command, cancel: childCancel, done: make(chan error, 1), stderr: stderr}
	m.childMu.Lock()
	if m.child != nil {
		m.childMu.Unlock()
		_ = command.Process.Kill()
		_ = command.Wait()
		childCancel()
		return errors.New("websearchplus: concurrent Hound bridge start")
	}
	m.child = child
	m.childMu.Unlock()
	go m.waitChild(child)

	startupContext, startupCancel := context.WithTimeout(ctx, m.config.StartupTimeout)
	defer startupCancel()
	ticker := time.NewTicker(75 * time.Millisecond)
	defer ticker.Stop()
	for {
		probe := m.deps.probeBridge(startupContext, m.config.HoundEndpoint, m.config.ProbeTimeout)
		if probe.compatible {
			return nil
		}
		if probe.reachable && !probe.compatible {
			m.stopChildAfterFailedStart(child)
			return fmt.Errorf("websearchplus: started Hound bridge is incompatible: %s", safeDetail(probe.detail))
		}
		select {
		case err := <-child.done:
			childCancel()
			detail := safeDetail(stderr.String())
			if detail == "" {
				detail = "child exited before readiness"
			}
			if err != nil {
				return fmt.Errorf("websearchplus: Hound bridge exited: %s", detail)
			}
			return fmt.Errorf("websearchplus: Hound bridge exited: %s", detail)
		case <-startupContext.Done():
			m.stopChildAfterFailedStart(child)
			return fmt.Errorf("websearchplus: Hound bridge readiness: %w", startupContext.Err())
		case <-ticker.C:
		}
	}
}

func (m *Manager) waitChild(child *ownedChild) {
	err := child.command.Wait()
	child.done <- err
	close(child.done)
	child.cancel()
	m.childMu.Lock()
	if m.child == child {
		m.child = nil
	}
	m.childMu.Unlock()
}

func (m *Manager) stopChildAfterFailedStart(child *ownedChild) {
	stopContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = stopOwnedChild(stopContext, child)
}

// Stop stops only the child currently owned by this Manager. It is idempotent
// and never discovers or signals processes by port, name, PID file, or version.
func (m *Manager) Stop(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.childMu.Lock()
	child := m.child
	m.childMu.Unlock()
	if child == nil {
		return nil
	}
	return stopOwnedChild(ctx, child)
}

func stopOwnedChild(ctx context.Context, child *ownedChild) error {
	if child == nil || child.command == nil || child.command.Process == nil {
		return nil
	}
	signalErr := child.command.Process.Signal(os.Interrupt)
	if signalErr != nil && !errors.Is(signalErr, os.ErrProcessDone) {
		return signalErr
	}
	select {
	case <-child.done:
		child.cancel()
		return nil
	case <-ctx.Done():
		killErr := child.command.Process.Kill()
		<-child.done
		child.cancel()
		if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return errors.Join(ctx.Err(), killErr)
		}
		return ctx.Err()
	}
}

func runBoundedVersion(ctx context.Context, path string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, path, args...)
	stdout := &boundedBuffer{limit: maxProbeOutputBytes}
	stderr := &boundedBuffer{limit: maxProbeOutputBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = safeRuntimeEnvironment()
	err := command.Run()
	combined := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
	if err != nil {
		return "", err
	}
	return combined, nil
}

// readLocalHoundDistributionVersion never executes the Hound launcher. Hound's
// CLI contains self-repair behavior, so its version flags are not a read-only
// probe. Instead, this reads the launcher's Python shebang and asks that exact
// interpreter for installed distribution metadata using a fixed script.
func readLocalHoundDistributionVersion(ctx context.Context, launcherPath string) (string, error) {
	launcher, err := os.Open(launcherPath)
	if err != nil {
		return "", err
	}
	defer launcher.Close()
	data, err := io.ReadAll(io.LimitReader(launcher, 4096))
	if err != nil {
		return "", err
	}
	line := string(data)
	if index := strings.IndexByte(line, '\n'); index >= 0 {
		line = line[:index]
	}
	if !strings.HasPrefix(line, "#!") {
		return "", errors.New("hound launcher has no Python shebang")
	}
	parts := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "#!")))
	if len(parts) == 0 {
		return "", errors.New("hound launcher has an empty shebang")
	}
	program := parts[0]
	args := append([]string(nil), parts[1:]...)
	programName := strings.ToLower(filepath.Base(program))
	if programName == "env" {
		if len(args) != 1 || !strings.HasPrefix(strings.ToLower(filepath.Base(args[0])), "python") {
			return "", errors.New("hound launcher does not use Python")
		}
	} else {
		if !filepath.IsAbs(program) || !strings.HasPrefix(programName, "python") || len(args) != 0 {
			return "", errors.New("hound launcher does not use a safe Python shebang")
		}
	}
	args = append(args, "-c", "import importlib.metadata as m; print(m.version('hound-mcp'))")
	return runBoundedVersion(ctx, program, args...)
}

func safeRuntimeEnvironment() []string {
	allowed := []string{
		"HOME", "PATH", "TMPDIR", "TEMP", "TMP", "LANG", "LC_ALL",
		"SYSTEMROOT", "WINDIR", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "UV_CACHE_DIR", "UV_PYTHON",
	}
	result := make([]string, 0, len(allowed)+2)
	for _, name := range allowed {
		if value, ok := os.LookupEnv(name); ok {
			result = append(result, name+"="+value)
		}
	}
	result = append(result, "NO_COLOR=1", "PYTHONUNBUFFERED=1")
	return result
}

func probeTCP(ctx context.Context, endpoint string, timeout time.Duration) (bool, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return false, err
	}
	probeContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	connection, err := (&net.Dialer{Timeout: timeout}).DialContext(probeContext, "tcp", parsed.Host)
	if err != nil {
		if probeContext.Err() != nil && ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	_ = connection.Close()
	return true, nil
}

func probeBridgeCompatibility(ctx context.Context, endpoint string, timeout time.Duration) bridgeProbe {
	result := bridgeProbe{}
	if err := ValidateHoundEndpoint(endpoint); err != nil {
		result.detail = err.Error()
		return result
	}
	parsed, _ := url.Parse(endpoint)
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(dialContext context.Context, network, address string) (net.Conn, error) {
			if address != parsed.Host {
				return nil, errors.New("websearchplus: bridge attempted a non-canonical address")
			}
			return dialer.DialContext(dialContext, "tcp", parsed.Host)
		},
		DisableCompression: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	initialize := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2026-07-28",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]string{
				"name":    "openagentfleet-websearchplus-probe",
				"version": "1",
			},
		},
	}
	initEnvelope, sessionID, statusCode, err := postRPC(ctx, client, endpoint, "", initialize)
	if err != nil {
		result.detail = "MCP initialize failed"
		return result
	}
	result.reachable = true
	if statusCode < 200 || statusCode >= 300 {
		result.detail = fmt.Sprintf("MCP initialize returned HTTP %d", statusCode)
		return result
	}
	var initResult struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := decodeRPCResult(initEnvelope, &initResult); err != nil {
		result.detail = "MCP initialize returned an invalid result"
		return result
	}
	result.serverName = initResult.ServerInfo.Name
	result.serverVersion = initResult.ServerInfo.Version
	result.protocolVersion = initResult.ProtocolVersion
	if !strings.Contains(strings.ToLower(result.serverName), "hound") {
		result.detail = "MCP server is not Hound"
		return result
	}
	if result.serverVersion != HoundMCPVersion {
		result.detail = "Hound server version does not match the audited compatibility pin"
		return result
	}

	initialized := map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}
	_, _, initializedStatus, err := postRPC(ctx, client, endpoint, sessionID, initialized)
	if err != nil || initializedStatus < 200 || initializedStatus >= 300 {
		result.detail = "MCP initialized notification failed"
		return result
	}
	toolsRequest := map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}}
	toolsEnvelope, _, toolsStatus, err := postRPC(ctx, client, endpoint, sessionID, toolsRequest)
	if err != nil || toolsStatus < 200 || toolsStatus >= 300 {
		result.detail = "MCP tools/list failed"
		return result
	}
	var toolsResult struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := decodeRPCResult(toolsEnvelope, &toolsResult); err != nil {
		result.detail = "MCP tools/list returned an invalid result"
		return result
	}
	hasSearch := false
	hasFetch := false
	for _, tool := range toolsResult.Tools {
		if tool.Name == "" {
			continue
		}
		result.tools = append(result.tools, tool.Name)
		hasSearch = hasSearch || tool.Name == "mcp_smart_search"
		hasFetch = hasFetch || tool.Name == "mcp_smart_fetch"
	}
	if !hasSearch || !hasFetch {
		result.detail = "Hound lacks the tools required by the tested WSP bridge"
		return result
	}
	result.compatible = true
	result.detail = "exact Hound version and WSP bridge tools verified"
	deleteSession(ctx, client, endpoint, sessionID)
	return result
}

type rpcEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func postRPC(ctx context.Context, client *http.Client, endpoint, sessionID string, payload any) (rpcEnvelope, string, int, error) {
	var envelope rpcEnvelope
	encoded, err := json.Marshal(payload)
	if err != nil {
		return envelope, "", 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return envelope, "", 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		request.Header.Set("Mcp-Session-Id", sessionID)
	}
	response, err := client.Do(request)
	if err != nil {
		return envelope, "", 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxHTTPProbeBytes+1))
	if err != nil {
		return envelope, "", response.StatusCode, err
	}
	if len(body) > maxHTTPProbeBytes {
		return envelope, "", response.StatusCode, errors.New("websearchplus: MCP probe response exceeded limit")
	}
	if len(bytes.TrimSpace(body)) != 0 {
		envelope, err = decodeRPCEnvelope(body)
		if err != nil {
			return envelope, "", response.StatusCode, err
		}
	}
	returnedSession := response.Header.Get("Mcp-Session-Id")
	if returnedSession == "" {
		returnedSession = sessionID
	}
	return envelope, returnedSession, response.StatusCode, nil
}

func decodeRPCEnvelope(body []byte) (rpcEnvelope, error) {
	var envelope rpcEnvelope
	trimmed := bytes.TrimSpace(body)
	if bytes.HasPrefix(trimmed, []byte("data:")) || bytes.Contains(trimmed, []byte("\ndata:")) {
		for _, line := range bytes.Split(trimmed, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			candidate := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if json.Unmarshal(candidate, &envelope) == nil {
				return envelope, nil
			}
		}
		return envelope, errors.New("websearchplus: invalid MCP event stream")
	}
	if err := json.Unmarshal(trimmed, &envelope); err != nil {
		return envelope, err
	}
	return envelope, nil
}

func decodeRPCResult(envelope rpcEnvelope, target any) error {
	if envelope.Error != nil {
		return errors.New("MCP returned an error")
	}
	if len(envelope.Result) == 0 {
		return errors.New("MCP returned no result")
	}
	return json.Unmarshal(envelope.Result, target)
}

func deleteSession(ctx context.Context, client *http.Client, endpoint, sessionID string) {
	if sessionID == "" {
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return
	}
	request.Header.Set("Mcp-Session-Id", sessionID)
	response, err := client.Do(request)
	if err == nil {
		_ = response.Body.Close()
	}
}

func cloneSpecs(specs []MCPServerSpec) []MCPServerSpec {
	result := make([]MCPServerSpec, 0, len(specs))
	for _, spec := range specs {
		cloned := MCPServerSpec{Name: spec.Name, Command: spec.Command, Args: append([]string(nil), spec.Args...)}
		if spec.Env != nil {
			cloned.Env = make(map[string]string, len(spec.Env))
			for name, value := range spec.Env {
				cloned.Env[name] = value
			}
		}
		result = append(result, cloned)
	}
	return result
}

func safeDetail(detail string) string {
	detail = credentialPattern.ReplaceAllString(detail, "$1=[redacted]")
	detail = commonTokenPattern.ReplaceAllString(detail, "[redacted]")
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) > 512 {
		detail = detail[:512]
	}
	return detail
}

type boundedBuffer struct {
	mu        sync.Mutex
	limit     int
	data      []byte
	truncated bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		amount := len(value)
		if amount > remaining {
			amount = remaining
		}
		b.data = append(b.data, value[:amount]...)
	}
	if len(value) > remaining {
		b.truncated = true
	}
	return len(value), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := string(b.data)
	if b.truncated {
		result += " [truncated]"
	}
	return result
}
