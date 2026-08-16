package compute

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var ErrExecutionDisabled = errors.New("computer execution is disabled; set OPENAGENTFLEET_ALLOW_COMPUTER_EXECUTION=1 to enable it")

const (
	ComputerStateUnavailable = "unavailable"
	ComputerStateStopped     = "stopped"
	ComputerStateStarting    = "starting"
	ComputerStateReady       = "ready"
	ComputerStateError       = "error"
)

type Docker struct {
	Binary string
	// Context selects a Docker CLI context without mutating the user's global
	// Docker context. This lets one botd instance target Colima while another
	// process keeps using Docker Desktop.
	Context        string
	RuntimeID      string
	RuntimeName    string
	RuntimeDetail  string
	Resources      ResourceConfig
	ColimaBinary   string
	ColimaProfile  string
	ContainerName  string
	Image          string
	Workspace      string
	BuildContext   string
	AllowExecution bool
	ViewPort       int
	ContainerPort  int
	// BrowserProfilePath is the legacy/controller-side location retained for
	// migration checks and compatibility with older callers. New containers use
	// BrowserProfileVolume because Chromium needs POSIX symlink locks that a
	// macOS virtiofs bind mount cannot reliably provide.
	BrowserProfilePath string
	// BrowserProfileVolume is a durable Docker-managed volume inside the local
	// Colima VM. It keeps Chromium state persistent without exposing its lock
	// files through a macOS host bind mount.
	BrowserProfileVolume string
	// ControlTokenPath is deliberately outside Workspace: Workspace is bind
	// mounted into the Agent Computer and must never expose the controller
	// capability token to the guest.
	ControlTokenPath string
	// RemoteBaseURL switches only the computer view/lifecycle transport. An
	// empty value preserves the local Docker/Colima implementation. The
	// controller never shells into a remote host or exposes its Docker socket.
	RemoteBaseURL string
	RemoteToken   string
	runtimeMu     sync.RWMutex
	controlMu     sync.RWMutex
	controlToken  string
	statusMu      sync.Mutex
	statusRunning bool
	statusDone    chan struct{}
	statusCache   Status
	lifecycleMu   sync.Mutex
}

type Status struct {
	// State is a product-facing lifecycle value. It prevents clients from
	// collapsing a stopped runtime, a warming Chromium session, and a broken
	// provider into the same indefinite "waiting" state.
	State          string         `json:"state"`
	CanRetry       bool           `json:"can_retry"`
	Available      bool           `json:"available"`
	ContainerID    string         `json:"container_id,omitempty"`
	Running        bool           `json:"running"`
	BrowserReady   bool           `json:"browser_ready"`
	DesktopReady   bool           `json:"desktop_ready"`
	Image          string         `json:"image"`
	Resources      ResourceConfig `json:"resources"`
	RuntimeID      string         `json:"runtime_id,omitempty"`
	RuntimeName    string         `json:"runtime_name,omitempty"`
	RuntimeContext string         `json:"runtime_context,omitempty"`
	RuntimeDetail  string         `json:"runtime_detail,omitempty"`
	URL            string         `json:"url,omitempty"`
	Title          string         `json:"title,omitempty"`
	ViewportWidth  int            `json:"viewport_width,omitempty"`
	ViewportHeight int            `json:"viewport_height,omitempty"`
	Takeover       bool           `json:"takeover"`
	AgentControl   bool           `json:"agent_control"`
	Detail         string         `json:"detail,omitempty"`
}

type ViewStatus struct {
	Ready          bool   `json:"ready"`
	URL            string `json:"url,omitempty"`
	Title          string `json:"title,omitempty"`
	ViewportWidth  int    `json:"viewport_width,omitempty"`
	ViewportHeight int    `json:"viewport_height,omitempty"`
	Pages          int    `json:"pages,omitempty"`
}

// TargetBinding is non-secret metadata captured when a human asks the native
// shell to type a secret. The Agent Computer rechecks both values immediately
// before injection so a tab/window switch cannot redirect the secret.
type TargetBinding struct {
	ComputerID string `json:"computer_id"`
	TargetID   string `json:"target_id"`
}

type BrowserAction struct {
	Action    string  `json:"action"`
	URL       string  `json:"url,omitempty"`
	X         float64 `json:"x,omitempty"`
	Y         float64 `json:"y,omitempty"`
	DeltaX    float64 `json:"delta_x,omitempty"`
	DeltaY    float64 `json:"delta_y,omitempty"`
	Text      string  `json:"text,omitempty"`
	Key       string  `json:"key,omitempty"`
	Sensitive bool    `json:"sensitive,omitempty"`
}

func NewDocker(workspace, buildContext string, allowExecution bool) *Docker {
	stateDir := filepath.Dir(workspace)
	resources := DefaultResourceConfig()
	docker := &Docker{
		Binary:               "docker",
		RuntimeID:            RuntimeDocker,
		RuntimeName:          runtimeName(RuntimeDocker),
		ColimaBinary:         "colima",
		ColimaProfile:        DefaultColimaProfile,
		ContainerName:        "openagentfleet-agent-computer",
		Image:                resources.ImageTag(),
		Resources:            resources,
		Workspace:            workspace,
		BuildContext:         buildContext,
		AllowExecution:       allowExecution,
		ViewPort:             9223,
		ContainerPort:        9223,
		BrowserProfilePath:   filepath.Join(stateDir, "agent-computer-browser-profile"),
		BrowserProfileVolume: browserProfileVolumeName(stateDir),
		ControlTokenPath:     filepath.Join(stateDir, "agent-computer-control-token"),
	}
	if binary, err := findExecutable("docker"); err == nil {
		docker.Binary = binary
	}
	return docker
}

func browserProfileVolumeName(stateDir string) string {
	absolute, err := filepath.Abs(stateDir)
	if err != nil {
		absolute = filepath.Clean(stateDir)
	}
	digest := sha256.Sum256([]byte(filepath.Clean(absolute)))
	return fmt.Sprintf("openagentfleet-browser-profile-%x", digest[:8])
}

// ConfigureRuntime changes only this controller's explicit Docker context. It
// never mutates the user's global `docker context` selection.
func (d *Docker) ConfigureRuntime(selection RuntimeSelection) {
	d.runtimeMu.Lock()
	defer d.runtimeMu.Unlock()
	d.Context = selection.Context
	d.RuntimeID = selection.ID
	d.RuntimeName = selection.Name
	d.RuntimeDetail = selection.Detail
}

// ConfigureResources applies the validated Agent Computer contract to this
// controller. It changes the image tag as well, so selecting a different OS
// can never accidentally reuse a previously built distro image.
func (d *Docker) ConfigureResources(resources ResourceConfig) {
	resources = resources.Normalize()
	d.runtimeMu.Lock()
	defer d.runtimeMu.Unlock()
	d.Resources = resources
	d.Image = resources.ImageTag()
}

func (d *Docker) resourceConfig() ResourceConfig {
	d.runtimeMu.RLock()
	resources := d.Resources
	d.runtimeMu.RUnlock()
	if resources.CPUs == 0 {
		resources = DefaultResourceConfig()
	}
	return resources.Normalize()
}

// ConfigureRemote selects an optional authenticated Agent Computer worker.
// Remote lifecycle is owned by the worker; the controller-side Docker CLI is
// never used while this endpoint is configured.
func (d *Docker) ConfigureRemote(baseURL, token string) error {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		d.RemoteBaseURL = ""
		d.RemoteToken = ""
		return nil
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("remote computer URL must be an http(s) URL without credentials, query, or fragment")
	}
	if parsed.Scheme == "http" && !isLoopbackRemoteHost(parsed.Hostname()) {
		return errors.New("remote computer URL must use HTTPS unless it targets localhost")
	}
	if len(strings.TrimSpace(token)) < 32 {
		return errors.New("remote computer token must contain at least 32 characters")
	}
	d.RemoteBaseURL = baseURL
	d.RemoteToken = strings.TrimSpace(token)
	return nil
}

func (d *Docker) remoteEnabled() bool {
	return strings.TrimSpace(d.RemoteBaseURL) != ""
}

func (d *Docker) baseStatus() Status {
	status := Status{State: ComputerStateUnavailable, Image: d.Image, Resources: d.resourceConfig()}
	d.applyRuntimeStatus(&status)
	return status
}

func (d *Docker) applyRuntimeStatus(status *Status) {
	d.runtimeMu.RLock()
	defer d.runtimeMu.RUnlock()
	runtimeID := d.RuntimeID
	if runtimeID == "" {
		runtimeID = RuntimeDocker
	}
	runtimeNameValue := d.RuntimeName
	if runtimeNameValue == "" {
		runtimeNameValue = runtimeName(runtimeID)
	}
	status.RuntimeID = runtimeID
	status.RuntimeName = runtimeNameValue
	status.RuntimeContext = d.Context
	status.RuntimeDetail = d.RuntimeDetail
}

func (d *Docker) commandArgs(args ...string) []string {
	d.runtimeMu.RLock()
	contextName := d.Context
	d.runtimeMu.RUnlock()
	if strings.TrimSpace(contextName) == "" || contextName == "default" {
		return args
	}
	result := make([]string, 0, len(args)+2)
	result = append(result, "--context", contextName)
	return append(result, args...)
}

func (d *Docker) run(ctx context.Context, args ...string) error {
	return run(ctx, d.Binary, d.commandArgs(args...)...)
}

func (d *Docker) runOutput(ctx context.Context, args ...string) (string, error) {
	return runOutput(ctx, d.Binary, d.commandArgs(args...)...)
}

func (d *Docker) runOutputWithTimeout(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	return runOutputWithTimeout(ctx, timeout, d.Binary, d.commandArgs(args...)...)
}

func (d *Docker) runOutputWithTimeoutEnv(ctx context.Context, timeout time.Duration, env []string, args ...string) (string, error) {
	return runOutputWithTimeoutEnv(ctx, timeout, env, d.Binary, d.commandArgs(args...)...)
}

func (d *Docker) Status(ctx context.Context) Status {
	d.statusMu.Lock()
	if d.statusRunning {
		done := d.statusDone
		d.statusMu.Unlock()
		if done == nil {
			return d.baseStatus()
		}
		select {
		case <-done:
			d.statusMu.Lock()
			cached := d.statusCache
			if cached.Image == "" {
				cached.Image = d.Image
			}
			d.applyRuntimeStatus(&cached)
			d.statusMu.Unlock()
			return cached
		case <-ctx.Done():
			cached := d.baseStatus()
			cached.State = ComputerStateStarting
			cached.CanRetry = true
			cached.Detail = "Docker status probe is still running"
			return cached
		}
	}
	d.statusRunning = true
	d.statusDone = make(chan struct{})
	done := d.statusDone
	d.statusMu.Unlock()

	result := d.probeStatus(ctx)
	d.statusMu.Lock()
	d.statusRunning = false
	d.statusCache = result
	d.statusDone = nil
	close(done)
	d.statusMu.Unlock()
	return result
}

func (d *Docker) probeStatus(ctx context.Context) Status {
	if d.remoteEnabled() {
		return d.probeRemoteStatus(ctx)
	}
	result := d.baseStatus()
	// Status is called by bootstrap and by the UI polling loop. Docker Desktop
	// can leave `docker info` waiting indefinitely while its socket/VM is
	// unavailable, so never let a health probe inherit an unbounded HTTP
	// request context. The command runner also kills the whole process group;
	// killing only the docker CLI parent leaves plugin children behind on macOS.
	statusContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := exec.LookPath(d.Binary)
	if err != nil {
		result.Detail = "docker not found in PATH"
		return result
	}
	if err := d.run(statusContext, "info", "--format", "{{.ServerVersion}}"); err != nil {
		if result.RuntimeID == RuntimeColima {
			result.State = ComputerStateStopped
			result.CanRetry = true
			result.Detail = "Colima is stopped; Start Computer launches the dedicated profile on demand"
		} else {
			result.State = ComputerStateError
			result.CanRetry = true
			result.Detail = dockerDaemonUnavailableDetail(err)
		}
		return result
	}
	result.Available = true
	result.State = ComputerStateStopped
	result.CanRetry = true
	output, err := d.runOutput(statusContext, "ps", "--all", "--filter", "name=^/"+d.ContainerName+"$", "--format", "{{.ID}}|{{.Image}}|{{.Status}}")
	if err == nil {
		fields := strings.Split(strings.TrimSpace(output), "|")
		if len(fields) >= 3 {
			result.ContainerID = fields[0]
			result.Image = fields[1]
			result.Running = strings.HasPrefix(fields[2], "Up")
		}
	}
	if result.Running {
		result.State = ComputerStateStarting
		if view, err := d.ViewStatus(statusContext); err == nil {
			result.BrowserReady = view.Ready
			result.URL = view.URL
			result.Title = view.Title
			result.ViewportWidth = view.ViewportWidth
			result.ViewportHeight = view.ViewportHeight
		} else {
			result.Detail = "browser view unavailable: " + compact(err.Error())
		}
		result.DesktopReady = d.DesktopReady(statusContext)
		if result.BrowserReady && result.DesktopReady {
			result.State = ComputerStateReady
			result.CanRetry = false
		}
	}
	return result
}

func (d *Docker) probeRemoteStatus(ctx context.Context) Status {
	result := d.baseStatus()
	response, err := d.remoteRequest(ctx, http.MethodGet, "/status", nil)
	if err != nil {
		result.State = ComputerStateError
		result.CanRetry = true
		result.Detail = "remote Agent Computer unavailable: " + compact(err.Error())
		return result
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		result.State = ComputerStateError
		result.CanRetry = true
		result.Detail = fmt.Sprintf("remote Agent Computer returned HTTP %d", response.StatusCode)
		return result
	}
	var remote Status
	if err := json.NewDecoder(response.Body).Decode(&remote); err != nil {
		result.State = ComputerStateError
		result.CanRetry = true
		result.Detail = "remote Agent Computer returned invalid status"
		return result
	}
	if remote.State == "" {
		remote.State, remote.CanRetry = inferState(remote)
	}
	remote.RuntimeID = "remote"
	remote.RuntimeName = "Remote Agent Computer"
	remote.RuntimeContext = "Tailscale worker"
	remote.RuntimeDetail = d.RemoteBaseURL
	return remote
}

func inferState(status Status) (string, bool) {
	if !status.Available {
		return ComputerStateUnavailable, false
	}
	if !status.Running {
		return ComputerStateStopped, true
	}
	if status.BrowserReady && status.DesktopReady {
		return ComputerStateReady, false
	}
	return ComputerStateStarting, true
}

func (d *Docker) Ensure(ctx context.Context) (Status, error) {
	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	return d.ensure(ctx)
}

func (d *Docker) ensure(ctx context.Context) (Status, error) {
	if !d.AllowExecution {
		return d.baseStatus(), ErrExecutionDisabled
	}
	if d.remoteEnabled() {
		return d.remoteLifecycle(ctx, http.MethodPost, "/ensure")
	}
	resources := d.resourceConfig()
	d.runtimeMu.RLock()
	runtimeID := d.RuntimeID
	d.runtimeMu.RUnlock()
	if runtimeID == RuntimeColima || runtime.GOOS == "linux" {
		if err := d.checkHostStorage(resources); err != nil {
			status := d.baseStatus()
			status.State = ComputerStateError
			status.CanRetry = true
			status.Detail = err.Error()
			return status, err
		}
	}
	if err := os.MkdirAll(d.Workspace, 0o700); err != nil {
		return Status{}, err
	}
	var err error
	profilePath := d.browserProfilePath()
	if strings.TrimSpace(d.BrowserProfileVolume) == "" {
		profilePath, err = d.prepareBrowserProfile()
		if err != nil {
			return Status{}, err
		}
	}
	if err := d.ensureRuntimeReady(ctx); err != nil {
		status := d.baseStatus()
		status.Detail = err.Error()
		return status, err
	}
	status := d.Status(ctx)
	if !status.Available {
		return status, errors.New(status.Detail)
	}
	if status.Running {
		if status.Image == d.Image && status.BrowserReady && status.DesktopReady && d.usesControllerBrowserProfile(ctx, profilePath) {
			return status, nil
		}
		// A running container without the view service is an older Agent
		// Computer instance. Recreate only this named container. A healthy
		// pre-isolation container is also recreated so its old profile mount
		// below Workspace cannot survive this security migration.
		if err := d.stop(ctx); err != nil {
			return status, fmt.Errorf("replace stale agent computer: %w", err)
		}
	} else if status.ContainerID != "" {
		// A failed or interrupted start leaves the named container behind in
		// Docker. Treat that exact, stopped container as stale before issuing
		// the next `docker run`; otherwise Docker rejects the fresh start with
		// a name-conflict error and the UI can never recover from it.
		if err := d.stop(ctx); err != nil {
			return status, fmt.Errorf("remove stale agent computer: %w", err)
		}
	}
	if strings.TrimSpace(d.BrowserProfileVolume) == "" {
		if err := clearStaleBrowserProfileLocks(profilePath); err != nil {
			return status, err
		}
	} else if err := d.ensureBrowserProfileVolume(ctx); err != nil {
		return status, err
	}
	if _, err := d.runOutput(ctx, "image", "inspect", d.Image); err != nil {
		if d.BuildContext == "" {
			return status, fmt.Errorf("agent image %s is missing and no build context is configured", d.Image)
		}
		buildEnv := []string(nil)
		buildxContext, buildxCancel := context.WithTimeout(ctx, 15*time.Second)
		_, buildxErr := d.runOutputWithTimeout(buildxContext, 15*time.Second, "buildx", "version")
		buildxCancel()
		if buildxErr != nil {
			// Some Linux Docker packages ship without the buildx CLI plugin. Keep
			// the local fallback usable there; Docker Desktop and Colima continue
			// to use BuildKit when buildx is available.
			buildEnv = []string{"DOCKER_BUILDKIT=0"}
		}
		if _, err := d.runOutputWithTimeoutEnv(ctx, 15*time.Minute, buildEnv, "build", "--build-arg", "COMPUTER_BASE_IMAGE="+resources.BaseImage(), "--tag", d.Image, d.BuildContext); err != nil {
			return status, fmt.Errorf("build agent image: %w", err)
		}
	}
	controlToken, err := newControlToken()
	if err != nil {
		return status, fmt.Errorf("create agent computer control token: %w", err)
	}
	d.setControlToken(controlToken)
	args := d.containerRunArgs(controlToken)
	if _, err := d.runOutput(ctx, args...); err != nil {
		d.setControlToken("")
		return status, fmt.Errorf("start agent computer: %w", err)
	}
	if err := d.persistControlToken(controlToken); err != nil {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = d.stop(cleanupContext)
		cleanupCancel()
		return status, fmt.Errorf("persist agent computer control token: %w", err)
	}
	// Image creation can be cold on a new Mac and Chromium may need a few
	// seconds to create its persistent profile and expose CDP. Do not return a
	// successful-but-unready status: that leaves the UI stuck on
	// "Waiting for Chromium" with no actionable error.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		status = d.Status(ctx)
		if status.BrowserReady && status.DesktopReady {
			return status, nil
		}
		select {
		case <-ctx.Done():
			cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = d.stop(cleanupContext)
			cleanupCancel()
			return status, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	status = d.Status(ctx)
	if status.Detail == "" {
		status.Detail = "Chromium did not become ready within 90 seconds"
	}
	cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = d.stop(cleanupContext)
	cleanupCancel()
	return status, errors.New(status.Detail)
}

func (d *Docker) ensureBrowserProfileVolume(ctx context.Context) error {
	volume := strings.TrimSpace(d.BrowserProfileVolume)
	if volume == "" {
		return nil
	}
	if _, err := d.runOutput(ctx, "volume", "create", volume); err != nil {
		return fmt.Errorf("create persistent Chromium profile volume: %w", err)
	}
	return nil
}

func clearStaleBrowserProfileLocks(profilePath string) error {
	for _, name := range []string{"SingletonCookie", "SingletonLock", "SingletonSocket"} {
		path := filepath.Join(profilePath, name)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("clear stale Chromium profile lock %s: %w", name, err)
		}
	}
	return nil
}

func (d *Docker) ensureRuntimeReady(parent context.Context) error {
	d.runtimeMu.RLock()
	runtimeID := d.RuntimeID
	colimaBinary := d.ColimaBinary
	profile := d.ColimaProfile
	d.runtimeMu.RUnlock()
	if runtimeID != RuntimeColima {
		return nil
	}
	if strings.TrimSpace(profile) == "" {
		profile = DefaultColimaProfile
	}
	if strings.TrimSpace(colimaBinary) == "" {
		colimaBinary = "colima"
	}
	resources := d.resourceConfig()
	mounts, err := d.ensureColimaHostMounts()
	if err != nil {
		return err
	}
	configExists, mountsChanged, err := ensureColimaMountConfig(profile, mounts)
	if err != nil {
		return err
	}
	resourceConfigExists, resourcesChanged, actualDiskGiB, err := ensureColimaResourceConfig(profile, resources)
	if err != nil {
		return err
	}
	configExists = configExists || resourceConfigExists
	configChanged := mountsChanged || resourcesChanged
	diskDetail := ""
	if actualDiskGiB > resources.DiskGiB {
		diskDetail = fmt.Sprintf("Existing Colima profile keeps its larger %d GiB disk; Colima cannot shrink it to the requested %d GiB", actualDiskGiB, resources.DiskGiB)
	}

	probeContext, probeCancel := context.WithTimeout(parent, 3*time.Second)
	probeErr := d.run(probeContext, "info", "--format", "{{.ServerVersion}}")
	probeCancel()
	if probeErr == nil {
		if configChanged {
			path, err := findExecutable(colimaBinary)
			if err != nil {
				return errors.New("Colima is not installed; use the onboarding installer or run: " + ColimaInstallCommand)
			}
			if err := restartColimaProfile(parent, path, profile); err != nil {
				return err
			}
		}
		path, err := findExecutable(colimaBinary)
		if err != nil {
			return errors.New("Colima is not installed; use the onboarding installer or run: " + ColimaInstallCommand)
		}
		if err := configureColimaSwap(parent, path, profile, resources.SwapGiB); err != nil {
			return err
		}
		verifyContext, verifyCancel := context.WithTimeout(parent, 10*time.Second)
		defer verifyCancel()
		if err := d.run(verifyContext, "info", "--format", "{{.ServerVersion}}"); err != nil {
			return fmt.Errorf("Colima restarted but Docker context %s is unavailable: %w", colimaContextName(profile), err)
		}
		if diskDetail != "" {
			d.setRuntimeDetail(diskDetail)
		}
		return nil
	}
	path, err := findExecutable(colimaBinary)
	if err != nil {
		return errors.New("Colima is not installed; use the onboarding installer or run: " + ColimaInstallCommand)
	}

	startContext, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	args := []string{
		"start", "--profile", profile, "--activate=false",
		"--cpus", strconv.Itoa(resources.CPUs),
		"--memory", strconv.Itoa(resources.MemoryGiB),
		"--disk", strconv.Itoa(resources.DiskGiB),
	}
	if !configExists {
		for _, mount := range mounts {
			args = append(args, "--mount", mount+":w")
		}
	}
	command := newCommandContext(startContext, path, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		detail := compact(string(output))
		if detail != "" {
			return fmt.Errorf("start Colima profile %s: %w: %s", profile, err, detail)
		}
		return fmt.Errorf("start Colima profile %s: %w", profile, err)
	}

	selection := stoppedColimaSelection(profile)
	selection.Detail = "Dedicated Colima profile " + profile + " started on demand"
	if diskDetail != "" {
		selection.Detail += ". " + diskDetail
	}
	d.ConfigureRuntime(selection)
	if err := configureColimaSwap(parent, path, profile, resources.SwapGiB); err != nil {
		return err
	}
	verifyContext, verifyCancel := context.WithTimeout(parent, 10*time.Second)
	defer verifyCancel()
	if err := d.run(verifyContext, "info", "--format", "{{.ServerVersion}}"); err != nil {
		return fmt.Errorf("Colima started but Docker context %s is unavailable: %w", selection.Context, err)
	}
	return nil
}

func (d *Docker) setRuntimeDetail(detail string) {
	d.runtimeMu.Lock()
	d.RuntimeDetail = detail
	d.runtimeMu.Unlock()
}

func restartColimaProfile(parent context.Context, binary, profile string) error {
	restartContext, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	command := newCommandContext(restartContext, binary, "restart", "--profile", profile)
	output, err := command.CombinedOutput()
	if err != nil {
		detail := compact(string(output))
		if detail != "" {
			return fmt.Errorf("restart Colima profile %s after adding Agent Computer mounts: %w: %s", profile, err, detail)
		}
		return fmt.Errorf("restart Colima profile %s after adding Agent Computer mounts: %w", profile, err)
	}
	return nil
}

func (d *Docker) containerRunArgs(controlToken string) []string {
	hostPort := d.ViewPort
	if hostPort <= 0 {
		hostPort = 9223
	}
	containerPort := d.ContainerPort
	if containerPort <= 0 {
		containerPort = 9223
	}
	profilePath := d.browserProfilePath()
	profileMount := "type=bind,source=" + profilePath + ",target=/home/agent/.chromium-profile"
	if volume := strings.TrimSpace(d.BrowserProfileVolume); volume != "" {
		profileMount = "type=volume,source=" + volume + ",target=/home/agent/.chromium-profile"
	}
	resources := d.resourceConfig()
	memorySwapGiB := resources.MemoryGiB + resources.SwapGiB
	if resources.SwapGiB == 0 {
		memorySwapGiB = resources.MemoryGiB
	}
	return []string{
		"run", "--detach", "--init", "--name", d.ContainerName,
		"--label", "com.openagentfleet.role=agent-computer",
		"--label", "com.openagentfleet.computer-view=playwright-cdp-v1",
		"--publish", fmt.Sprintf("127.0.0.1:%d:%d", hostPort, containerPort),
		"--cpus", strconv.Itoa(resources.CPUs),
		"--memory", fmt.Sprintf("%dg", resources.MemoryGiB),
		"--memory-swap", fmt.Sprintf("%dg", memorySwapGiB),
		"--shm-size", "256m",
		"--env", "COMPUTER_CONTROL_TOKEN=" + controlToken,
		"--mount", "type=bind,source=" + d.Workspace + ",target=/workspace",
		"--mount", profileMount,
		"--workdir", "/workspace", d.Image,
	}
}

func (d *Docker) browserProfilePath() string {
	if strings.TrimSpace(d.BrowserProfilePath) != "" {
		return d.BrowserProfilePath
	}
	// Keep manually constructed Docker values safe too. NewDocker always
	// supplies this path, but callers should never fall back into Workspace.
	return filepath.Join(filepath.Dir(d.Workspace), "agent-computer-browser-profile")
}

// prepareBrowserProfile keeps Chromium state out of the writable workspace
// mount. Older OpenAgentFleet versions stored it at Workspace/.browser-profile;
// migrate that directory once so existing browser sessions survive the hardening
// change without leaving their cookies readable to the Agent Computer.
func (d *Docker) prepareBrowserProfile() (string, error) {
	workspace, err := filepath.Abs(d.Workspace)
	if err != nil {
		return "", fmt.Errorf("resolve browser workspace: %w", err)
	}
	profile, err := filepath.Abs(d.browserProfilePath())
	if err != nil {
		return "", fmt.Errorf("resolve browser profile: %w", err)
	}
	relative, err := filepath.Rel(workspace, profile)
	if err != nil {
		return "", fmt.Errorf("compare browser profile path: %w", err)
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return "", errors.New("browser profile must stay outside the bind-mounted workspace")
	}
	if err := os.MkdirAll(filepath.Dir(profile), 0o700); err != nil {
		return "", fmt.Errorf("create browser profile state directory: %w", err)
	}
	legacy := filepath.Join(workspace, ".browser-profile")
	legacyInfo, legacyErr := os.Lstat(legacy)
	profileInfo, profileErr := os.Lstat(profile)
	if profileErr == nil && profileInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("browser profile must not be a symbolic link")
	}
	if profileErr != nil && !errors.Is(profileErr, os.ErrNotExist) {
		return "", fmt.Errorf("inspect browser profile: %w", profileErr)
	}
	if legacyErr == nil {
		if legacyInfo.Mode()&os.ModeSymlink != 0 || !legacyInfo.IsDir() {
			return "", errors.New("legacy browser profile must be a real directory")
		}
		if profileErr == nil {
			return "", errors.New("legacy browser profile remains inside workspace; move it out before starting the Agent Computer")
		}
		if err := os.Rename(legacy, profile); err != nil {
			return "", fmt.Errorf("move legacy browser profile into controller state: %w", err)
		}
		profileErr = nil
	}
	if legacyErr != nil && !errors.Is(legacyErr, os.ErrNotExist) {
		return "", fmt.Errorf("inspect legacy browser profile: %w", legacyErr)
	}
	if profileErr != nil {
		if err := os.MkdirAll(profile, 0o700); err != nil {
			return "", fmt.Errorf("create browser profile: %w", err)
		}
	}
	if err := os.Chmod(profile, 0o700); err != nil {
		return "", fmt.Errorf("restrict browser profile permissions: %w", err)
	}
	if err := ensurePrivateDirectory(profile); err != nil {
		return "", fmt.Errorf("secure browser profile: %w", err)
	}
	return profile, nil
}

func (d *Docker) usesControllerBrowserProfile(ctx context.Context, profilePath string) bool {
	output, err := d.runOutput(ctx, "inspect", "--format", "{{range .Mounts}}{{if eq .Destination \"/home/agent/.chromium-profile\"}}{{.Source}}{{end}}{{end}}", d.ContainerName)
	if err != nil {
		return false
	}
	expected := strings.TrimSpace(d.BrowserProfileVolume)
	if expected == "" {
		var err error
		expected, err = filepath.Abs(profilePath)
		if err != nil {
			return false
		}
	}
	actual := strings.TrimSpace(output)
	if strings.TrimSpace(d.BrowserProfileVolume) != "" {
		return actual == expected
	}
	absolute, err := filepath.Abs(actual)
	return err == nil && absolute == expected
}

// DesktopReady checks the controlled Xfce screenshot surface. Raw VNC/noVNC
// is intentionally not published: manual and agent input both go through the
// server-gated desktop action API.
func (d *Docker) DesktopReady(ctx context.Context) bool {
	response, err := d.viewRequest(ctx, http.MethodGet, "/desktop-frame", nil)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK && strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "image/")
}

func (d *Docker) Stop(ctx context.Context) error {
	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	return d.stop(ctx)
}

func (d *Docker) stop(ctx context.Context) error {
	if !d.AllowExecution {
		return ErrExecutionDisabled
	}
	if d.remoteEnabled() {
		response, err := d.remoteRequest(ctx, http.MethodPost, "/stop", nil)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
			return fmt.Errorf("remote Agent Computer stop returned HTTP %d", response.StatusCode)
		}
		return nil
	}
	_, err := d.runOutput(ctx, "rm", "--force", d.ContainerName)
	if err == nil {
		if clearErr := d.clearControlToken(); clearErr != nil {
			return fmt.Errorf("clear agent computer control token: %w", clearErr)
		}
	}
	return err
}

func (d *Docker) Exec(ctx context.Context, command ...string) (string, error) {
	if !d.AllowExecution {
		return "", ErrExecutionDisabled
	}
	if d.remoteEnabled() {
		return "", errors.New("container exec is unavailable for a remote Agent Computer")
	}
	if len(command) == 0 {
		return "", errors.New("container command is empty")
	}
	args := append([]string{"exec", d.ContainerName}, command...)
	return d.runOutput(ctx, args...)
}

func (d *Docker) ViewStatus(ctx context.Context) (ViewStatus, error) {
	response, err := d.viewRequest(ctx, http.MethodGet, "/health", nil)
	if err != nil {
		return ViewStatus{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ViewStatus{}, fmt.Errorf("computer view returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Ready    bool   `json:"ready"`
		URL      string `json:"url"`
		Title    string `json:"title"`
		Pages    int    `json:"pages"`
		Viewport struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"viewport"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return ViewStatus{}, err
	}
	return ViewStatus{Ready: payload.Ready, URL: payload.URL, Title: payload.Title, Pages: payload.Pages, ViewportWidth: payload.Viewport.Width, ViewportHeight: payload.Viewport.Height}, nil
}

func (d *Docker) Frame(ctx context.Context) ([]byte, error) {
	return d.frame(ctx, "/frame")
}

func (d *Docker) DesktopFrame(ctx context.Context) ([]byte, error) {
	return d.frame(ctx, "/desktop-frame")
}

func (d *Docker) frame(ctx context.Context, path string) ([]byte, error) {
	response, err := d.viewRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("computer frame returned HTTP %d", response.StatusCode)
	}
	frame, err := io.ReadAll(io.LimitReader(response.Body, 12<<20))
	if err != nil {
		return nil, err
	}
	if len(frame) == 0 {
		return nil, errors.New("computer frame is empty")
	}
	if path == "/desktop-frame" {
		if err := validateDesktopFrame(frame); err != nil {
			return nil, err
		}
	}
	return frame, nil
}

// validateDesktopFrame rejects the transient blank root-window capture that
// some X11/desktop combinations can produce while Chromium and XFCE repaint
// after an input event. The client keeps its last good frame when this returns
// an error, so a transient compositor frame never replaces a usable preview.
func validateDesktopFrame(frame []byte) error {
	decoded, err := png.Decode(bytes.NewReader(frame))
	if err != nil {
		return fmt.Errorf("computer desktop frame is not a PNG: %w", err)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() < 2 || bounds.Dy() < 2 {
		return errors.New("computer desktop frame is too small")
	}

	var minLuminance uint32 = ^uint32(0)
	var maxLuminance uint32
	for row := 0; row < 8; row++ {
		y := bounds.Min.Y + row*bounds.Dy()/8
		for column := 0; column < 8; column++ {
			x := bounds.Min.X + column*bounds.Dx()/8
			r, g, b, _ := decoded.At(x, y).RGBA()
			luminance := (299*r + 587*g + 114*b) / 1000
			if luminance < minLuminance {
				minLuminance = luminance
			}
			if luminance > maxLuminance {
				maxLuminance = luminance
			}
		}
	}
	// A real Agent Computer always includes a visible XFCE/Chromium surface.
	// Reject a solid near-black capture, but do not reject a dark web page with
	// normal browser chrome or a dark desktop containing visible applications.
	if maxLuminance < 14000 || maxLuminance-minLuminance < 2000 {
		return errors.New("computer desktop frame is blank while the virtual display repaints")
	}
	return nil
}

func (d *Docker) Action(ctx context.Context, action BrowserAction) (ViewStatus, error) {
	if err := validateBrowserAction(action); err != nil {
		return ViewStatus{}, err
	}
	return d.performAction(ctx, "/action", action)
}

func (d *Docker) DesktopAction(ctx context.Context, action BrowserAction) (ViewStatus, error) {
	if err := validateDesktopAction(action); err != nil {
		return ViewStatus{}, err
	}
	return d.performAction(ctx, "/desktop/action", action)
}

// SensitiveType injects a short UTF-8 value into the currently focused human-
// approved browser or desktop target. The value is deliberately accepted as
// bytes, not a BrowserAction string, so a caller can wipe its buffer directly
// after the local transport call. This is an internal container bridge, not a
// browser-accessible API; callers must enforce their own takeover policy.
func (d *Docker) SensitiveType(ctx context.Context, surface string, binding TargetBinding, value []byte) (ViewStatus, error) {
	if d.remoteEnabled() {
		return ViewStatus{}, errors.New("secure secret handoff is local-only; take over on the worker host")
	}
	if len(value) == 0 || len(value) > 4096 || !utf8.Valid(value) {
		return ViewStatus{}, errors.New("sensitive text is invalid")
	}
	if !validTargetBinding(binding) {
		return ViewStatus{}, errors.New("sensitive target binding is invalid")
	}
	if surface != "browser" {
		return ViewStatus{}, errors.New("unsupported sensitive input surface")
	}
	body := sensitiveTypeBody(value, binding)
	defer wipeBytes(body)
	return d.performActionBody(ctx, "/action", body)
}

// TargetBinding reads the currently selected browser tab or desktop window
// from the controlled Agent Computer. It contains no page title, URL, or
// screen data and must be checked again by SensitiveType at delivery time.
func (d *Docker) TargetBinding(ctx context.Context, surface string) (TargetBinding, error) {
	if surface != "browser" {
		return TargetBinding{}, errors.New("unsupported sensitive input surface")
	}
	response, err := d.viewRequest(ctx, http.MethodGet, "/target?surface="+surface, nil)
	if err != nil {
		return TargetBinding{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return TargetBinding{}, fmt.Errorf("computer target returned HTTP %d", response.StatusCode)
	}
	var binding TargetBinding
	if err := json.NewDecoder(response.Body).Decode(&binding); err != nil {
		return TargetBinding{}, err
	}
	if !validTargetBinding(binding) {
		return TargetBinding{}, errors.New("computer returned an invalid target binding")
	}
	return binding, nil
}

func (d *Docker) performAction(ctx context.Context, path string, action BrowserAction) (ViewStatus, error) {
	body, err := json.Marshal(action)
	if err != nil {
		return ViewStatus{}, err
	}
	return d.performActionBody(ctx, path, body)
}

func (d *Docker) performActionBody(ctx context.Context, path string, body []byte) (ViewStatus, error) {
	response, err := d.viewRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return ViewStatus{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(response.Body).Decode(&failure)
		if failure.Error != "" {
			return ViewStatus{}, errors.New(failure.Error)
		}
		return ViewStatus{}, fmt.Errorf("computer action returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Ready    bool   `json:"ready"`
		URL      string `json:"url"`
		Title    string `json:"title"`
		Pages    int    `json:"pages"`
		Viewport struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"viewport"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return ViewStatus{}, err
	}
	return ViewStatus{Ready: payload.Ready, URL: payload.URL, Title: payload.Title, Pages: payload.Pages, ViewportWidth: payload.Viewport.Width, ViewportHeight: payload.Viewport.Height}, nil
}

func (d *Docker) remoteLifecycle(ctx context.Context, method, path string) (Status, error) {
	response, err := d.remoteRequest(ctx, method, path, nil)
	if err != nil {
		return d.baseStatus(), err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(response.Body).Decode(&failure)
		if failure.Error != "" {
			return d.baseStatus(), errors.New(failure.Error)
		}
		return d.baseStatus(), fmt.Errorf("remote Agent Computer lifecycle returned HTTP %d", response.StatusCode)
	}
	var status Status
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		return d.baseStatus(), err
	}
	status.RuntimeID = "remote"
	status.RuntimeName = "Remote Agent Computer"
	status.RuntimeContext = "Tailscale worker"
	status.RuntimeDetail = d.RemoteBaseURL
	return status, nil
}

func (d *Docker) viewRequest(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	if d.remoteEnabled() {
		return d.remoteRequest(ctx, method, path, body)
	}
	if err := d.restoreControlToken(); err != nil {
		return nil, fmt.Errorf("restore agent computer control token: %w", err)
	}
	if d.ViewPort <= 0 {
		d.ViewPort = 9223
	}
	request, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%d%s", d.ViewPort, path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token := d.controlTokenValue(); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	} else if d.ControlTokenPath != "" {
		return nil, errors.New("agent computer control token is unavailable")
	}
	timeout := 5 * time.Second
	if method == http.MethodGet && path == "/health" {
		timeout = 2 * time.Second
	}
	if method == http.MethodPost && (path == "/action" || path == "/desktop/action") {
		timeout = 40 * time.Second
	}
	return (&http.Client{Timeout: timeout}).Do(request)
}

func (d *Docker) remoteRequest(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	base := strings.TrimRight(strings.TrimSpace(d.RemoteBaseURL), "/")
	if base == "" {
		return nil, errors.New("remote Agent Computer URL is not configured")
	}
	endpoint, err := remoteEndpoint(base, path)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+d.RemoteToken)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	timeout := 5 * time.Second
	if method == http.MethodGet && path == "/health" {
		timeout = 2 * time.Second
	}
	if method == http.MethodPost && (path == "/action" || path == "/desktop/action" || path == "/ensure") {
		timeout = 5 * time.Minute
	}
	response, err := (&http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}).Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest {
		response.Body.Close()
		return nil, errors.New("remote Agent Computer redirect refused")
	}
	return response, nil
}

func remoteEndpoint(base, path string) (string, error) {
	baseURL, err := url.Parse(base)
	if err != nil || baseURL.Host == "" || baseURL.User != nil || (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return "", errors.New("remote computer URL is invalid")
	}
	target, err := url.Parse(path)
	if err != nil || target.IsAbs() || target.Host != "" || target.Fragment != "" || !strings.HasPrefix(target.Path, "/") {
		return "", errors.New("remote computer request path is invalid")
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/") + target.Path
	baseURL.RawPath = ""
	baseURL.RawQuery = target.RawQuery
	return baseURL.String(), nil
}

func isLoopbackRemoteHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}

func (d *Docker) controlTokenValue() string {
	d.controlMu.RLock()
	defer d.controlMu.RUnlock()
	return d.controlToken
}

func (d *Docker) setControlToken(token string) {
	d.controlMu.Lock()
	d.controlToken = token
	d.controlMu.Unlock()
}

func (d *Docker) restoreControlToken() error {
	if d.controlTokenValue() != "" || d.ControlTokenPath == "" {
		return nil
	}
	info, err := os.Lstat(d.ControlTokenPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("control token file is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("control token file is accessible outside its owner")
	}
	if err := ensurePrivateDirectory(filepath.Dir(d.ControlTokenPath)); err != nil {
		return err
	}
	data, err := os.ReadFile(d.ControlTokenPath)
	if err != nil {
		return err
	}
	token := string(data)
	if !validControlToken(token) {
		return errors.New("control token file has invalid contents")
	}
	d.setControlToken(token)
	return nil
}

func (d *Docker) persistControlToken(token string) error {
	if d.ControlTokenPath == "" {
		return nil
	}
	if !validControlToken(token) {
		return errors.New("control token is invalid")
	}
	directory := filepath.Dir(d.ControlTokenPath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := ensurePrivateDirectory(directory); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".agent-computer-control-token-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := io.WriteString(temporary, token); err != nil {
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
	return os.Rename(temporaryPath, d.ControlTokenPath)
}

func (d *Docker) clearControlToken() error {
	d.setControlToken("")
	if d.ControlTokenPath == "" {
		return nil
	}
	if err := os.Remove(d.ControlTokenPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func ensurePrivateDirectory(directory string) error {
	info, err := os.Stat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("control token parent is not a directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("control token parent is accessible outside its owner")
	}
	return nil
}

func newControlToken() (string, error) {
	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(tokenBytes[:]), nil
}

func validControlToken(token string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == token
}

func sensitiveTypeBody(value []byte, binding TargetBinding) []byte {
	body := make([]byte, 0, len(value)+len(binding.ComputerID)+len(binding.TargetID)+96)
	body = append(body, `{"action":"type","text":`...)
	body = appendJSONString(body, value)
	body = append(body, `,"sensitive":true,"native_handoff":true,"computer_id":`...)
	body = appendJSONString(body, []byte(binding.ComputerID))
	body = append(body, `,"target_id":`...)
	body = appendJSONString(body, []byte(binding.TargetID))
	body = append(body, '}')
	return body
}

func validTargetBinding(binding TargetBinding) bool {
	return validBindingIdentifier(binding.ComputerID) && validBindingIdentifier(binding.TargetID)
}

func validBindingIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("._:-", character) {
			continue
		}
		return false
	}
	return true
}

func appendJSONString(dst, value []byte) []byte {
	dst = append(dst, '"')
	for len(value) > 0 {
		runeValue, size := utf8.DecodeRune(value)
		if runeValue == utf8.RuneError && size == 1 {
			// SensitiveType validates UTF-8 before calling this helper. Keep a
			// valid JSON fallback if this helper is reused in the future.
			dst = append(dst, `\ufffd`...)
			value = value[1:]
			continue
		}
		switch runeValue {
		case '"':
			dst = append(dst, `\"`...)
		case '\\':
			dst = append(dst, `\\`...)
		case '\b':
			dst = append(dst, `\b`...)
		case '\f':
			dst = append(dst, `\f`...)
		case '\n':
			dst = append(dst, `\n`...)
		case '\r':
			dst = append(dst, `\r`...)
		case '\t':
			dst = append(dst, `\t`...)
		default:
			if runeValue < 0x20 {
				const hex = "0123456789abcdef"
				dst = append(dst, '\\', 'u', '0', '0', hex[runeValue>>4], hex[runeValue&0x0f])
			} else {
				dst = append(dst, value[:size]...)
			}
		}
		value = value[size:]
	}
	return append(dst, '"')
}

func wipeBytes(value []byte) {
	clear(value)
	runtime.KeepAlive(value)
}

func validateBrowserAction(action BrowserAction) error {
	switch action.Action {
	case "navigate":
		parsed, err := url.Parse(action.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return errors.New("only http(s) navigation is allowed")
		}
		if len(action.URL) > 4096 {
			return errors.New("navigation URL is too long")
		}
	case "click":
		if !validCoordinate(action.X) || !validCoordinate(action.Y) {
			return errors.New("click coordinates are invalid")
		}
	case "type":
		if len(action.Text) > 4096 {
			return errors.New("typed text is too long")
		}
	case "press":
		if strings.TrimSpace(action.Key) == "" || len(action.Key) > 64 {
			return errors.New("key is required")
		}
	case "scroll":
		if math.Abs(action.DeltaX) > 10000 || math.Abs(action.DeltaY) > 10000 {
			return errors.New("scroll delta is too large")
		}
	case "reload", "back", "forward":
	default:
		return errors.New("unsupported computer action")
	}
	return nil
}

func validateDesktopAction(action BrowserAction) error {
	switch action.Action {
	case "click":
		if !validCoordinate(action.X) || !validCoordinate(action.Y) {
			return errors.New("desktop click coordinates are invalid")
		}
	case "type":
		if len(action.Text) > 4096 {
			return errors.New("typed text is too long")
		}
	case "press":
		if strings.TrimSpace(action.Key) == "" || len(action.Key) > 64 {
			return errors.New("key is required")
		}
	case "scroll":
		if math.Abs(action.DeltaY) > 10000 {
			return errors.New("scroll delta is too large")
		}
	default:
		return errors.New("unsupported desktop action")
	}
	return nil
}

func validCoordinate(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 10000
}

func run(parent context.Context, program string, args ...string) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	command := newCommandContext(ctx, program, args...)
	return command.Run()
}

func runOutput(parent context.Context, program string, args ...string) (string, error) {
	return runOutputWithTimeout(parent, 2*time.Minute, program, args...)
}

func runOutputWithTimeout(parent context.Context, timeout time.Duration, program string, args ...string) (string, error) {
	return runOutputWithTimeoutEnv(parent, timeout, nil, program, args...)
}

func runOutputWithTimeoutEnv(parent context.Context, timeout time.Duration, env []string, program string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	command := newCommandContext(ctx, program, args...)
	if len(env) > 0 {
		command.Env = append(os.Environ(), env...)
	}
	output, err := command.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil && text != "" {
		return "", fmt.Errorf("%w: %s", err, compact(text))
	}
	return text, err
}

func compact(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 240 {
		return value[:240]
	}
	return value
}

func dockerDaemonUnavailableDetail(err error) string {
	if err == nil {
		return "Docker daemon unavailable"
	}
	detail := compact(err.Error())
	lower := strings.ToLower(detail)
	if strings.Contains(lower, "permission denied") {
		if runtime.GOOS == "linux" {
			return "Docker is installed but this user cannot talk to the daemon. Add your user to the docker group (`sudo usermod -aG docker $USER`) and start a new login session."
		}
		return "Docker daemon permission denied: " + detail
	}
	if runtime.GOOS == "linux" && (strings.Contains(lower, "cannot connect") || strings.Contains(lower, "is the docker daemon running") || strings.Contains(lower, "no such file or directory")) {
		return "Docker daemon is not running. Start it with `sudo systemctl start docker`, or install it with: " + LinuxDockerInstallCommand()
	}
	return "Docker daemon unavailable: " + detail
}

func (s Status) MarshalJSON() ([]byte, error) {
	type alias Status
	return json.Marshal(alias(s))
}
