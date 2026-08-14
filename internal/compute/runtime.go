package compute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	RuntimeAuto           = "auto"
	RuntimeDocker         = "docker"
	RuntimeDockerDesktop  = "docker_desktop"
	RuntimeColima         = "colima"
	RuntimeOrbStack       = "orbstack"
	RuntimeAppleContainer = "apple_container"

	DefaultColimaProfile = "openagentfleet"
	ColimaInstallCommand = "brew install colima docker"
)

var ErrRuntimeUnavailable = errors.New("requested runtime is not available")

// RuntimeInfo describes a local macOS runtime without implying that its
// complete Agent Computer contract has been validated. In particular, Apple
// Container is intentionally discoverable but not yet marked as a supported
// Agent Computer backend.
type RuntimeInfo struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Kind                  string `json:"kind"`
	Available             bool   `json:"available"`
	Healthy               bool   `json:"healthy"`
	Selected              bool   `json:"selected"`
	Context               string `json:"context,omitempty"`
	Endpoint              string `json:"endpoint,omitempty"`
	Version               string `json:"version,omitempty"`
	Detail                string `json:"detail,omitempty"`
	Experimental          bool   `json:"experimental"`
	OpenSource            bool   `json:"open_source"`
	SupportsAgentComputer bool   `json:"supports_agent_computer"`
	Installed             bool   `json:"installed"`
	Installable           bool   `json:"installable"`
	InstallCommand        string `json:"install_command,omitempty"`
}

var colimaInstallMu sync.Mutex

// RuntimeSelection is the resolved Docker-compatible runtime used by a
// Docker backend. Apple Container deliberately has no Docker context and
// must use a separate adapter before it can become a selection.
type RuntimeSelection struct {
	ID       string
	Name     string
	Context  string
	Endpoint string
	Detail   string
}

type dockerContext struct {
	Name     string
	Endpoint string
	Current  bool
}

func ResolveDockerRuntime(ctx context.Context, requested string) (RuntimeSelection, error) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested == "" {
		requested = RuntimeAuto
	}
	if requested == RuntimeAppleContainer {
		return RuntimeSelection{ID: RuntimeAppleContainer, Name: "Apple Container", Detail: "Apple Container needs its own runtime adapter; it is not a Docker context"}, fmt.Errorf("%w: Apple Container adapter is experimental", ErrRuntimeUnavailable)
	}
	if requested != RuntimeAuto && requested != RuntimeDocker && requested != RuntimeDockerDesktop && requested != RuntimeColima && requested != RuntimeOrbStack {
		return RuntimeSelection{}, fmt.Errorf("%w: unknown runtime %q", ErrRuntimeUnavailable, requested)
	}

	contexts, err := listDockerContexts(ctx)
	if err != nil {
		if requested == RuntimeAuto {
			return RuntimeSelection{ID: RuntimeDocker, Name: "Docker Engine", Context: strings.TrimSpace(os.Getenv("DOCKER_CONTEXT")), Detail: "Docker context inventory unavailable; using the Docker CLI default"}, nil
		}
		return RuntimeSelection{}, fmt.Errorf("list Docker contexts: %w", err)
	}

	var selected dockerContext
	found := false
	if requested == RuntimeAuto {
		if envContext := strings.TrimSpace(os.Getenv("DOCKER_CONTEXT")); envContext != "" {
			selected, found = findDockerContext(contexts, envContext)
			if !found && envContext == "default" {
				selected = dockerContext{Name: "default"}
				found = true
			}
		}
		if !found {
			for _, candidate := range contexts {
				if candidate.Current {
					selected = candidate
					found = true
					break
				}
			}
		}
		if !found {
			selected, found = findDockerContext(contexts, "default")
		}
	} else if requested == RuntimeDocker {
		if envContext := strings.TrimSpace(os.Getenv("DOCKER_CONTEXT")); envContext != "" {
			selected, found = findDockerContext(contexts, envContext)
		}
		if !found {
			for _, candidate := range contexts {
				if candidate.Current {
					selected = candidate
					found = true
					break
				}
			}
		}
	} else {
		selected, found = findRuntimeContext(contexts, requested)
	}
	if !found && requested == RuntimeColima {
		if _, err := findExecutable("colima"); err == nil {
			return stoppedColimaSelection(DefaultColimaProfile), nil
		}
	}
	if !found {
		return RuntimeSelection{ID: requested, Name: runtimeName(requested)}, fmt.Errorf("%w: no Docker context is configured for %s", ErrRuntimeUnavailable, runtimeName(requested))
	}

	runtimeID := classifyDockerContext(selected)
	if requested != RuntimeAuto && requested != RuntimeDocker {
		runtimeID = requested
	}
	return RuntimeSelection{
		ID:       runtimeID,
		Name:     runtimeName(runtimeID),
		Context:  selected.Name,
		Endpoint: selected.Endpoint,
	}, nil
}

// DiscoverRuntimes is intentionally lightweight enough for bootstrap. It
// inventories configured Docker contexts and probes only the selected Docker
// context; non-selected contexts are shown as configured rather than falsely
// reported healthy.
func DiscoverRuntimes(ctx context.Context, selectedID string) []RuntimeInfo {
	probeContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	contexts, _ := listDockerContexts(probeContext)
	dockerAvailable := false
	if _, err := findExecutable("docker"); err == nil {
		dockerAvailable = true
	}

	activeContext := currentDockerContext(contexts)
	if selectedID != RuntimeAuto && selectedID != RuntimeDocker && selectedID != "" {
		if selectedContext, ok := findRuntimeContext(contexts, selectedID); ok {
			activeContext = selectedContext
		}
	}
	result := make([]RuntimeInfo, 0, 5)
	result = append(result, discoverDockerRuntime(probeContext, dockerAvailable, contexts, activeContext, RuntimeDockerDesktop), discoverDockerRuntime(probeContext, dockerAvailable, contexts, activeContext, RuntimeColima), discoverDockerRuntime(probeContext, dockerAvailable, contexts, activeContext, RuntimeOrbStack))
	if selectedID == RuntimeDocker {
		selectedContext := activeContext
		result = append(result, RuntimeInfo{
			ID:                    RuntimeDocker,
			Name:                  runtimeName(RuntimeDocker),
			Kind:                  "docker",
			Available:             dockerAvailable,
			Healthy:               dockerAvailable && selectedContext.Name != "",
			Selected:              true,
			Context:               selectedContext.Name,
			Endpoint:              selectedContext.Endpoint,
			Detail:                "Custom or default Docker context",
			OpenSource:            true,
			SupportsAgentComputer: true,
		})
	}
	result = append(result, discoverAppleContainer(probeContext, selectedID == RuntimeAppleContainer))

	for index := range result {
		if result[index].ID == selectedID {
			result[index].Selected = true
		}
	}
	return result
}

func discoverDockerRuntime(ctx context.Context, dockerAvailable bool, contexts []dockerContext, activeContext dockerContext, runtimeID string) RuntimeInfo {
	info := RuntimeInfo{
		ID:                    runtimeID,
		Name:                  runtimeName(runtimeID),
		Kind:                  "docker",
		Available:             dockerAvailable,
		OpenSource:            runtimeID != RuntimeDockerDesktop,
		SupportsAgentComputer: true,
	}
	if runtimeID == RuntimeColima {
		_, colimaErr := findExecutable("colima")
		_, brewErr := findExecutable("brew")
		info.Installed = colimaErr == nil
		info.Available = dockerAvailable && info.Installed
		info.Installable = brewErr == nil && !info.Available
		info.InstallCommand = ColimaInstallCommand
	}
	selected, found := findRuntimeContext(contexts, runtimeID)
	if !found {
		if runtimeID == RuntimeColima {
			if info.Installed && dockerAvailable {
				info.Detail = "Installed; the dedicated openagentfleet profile starts on first Agent Computer use"
				return info
			}
			if info.Installed {
				info.Detail = "Colima is installed, but the Docker CLI is missing"
				return info
			}
			info.Detail = "Colima and the Docker CLI are required for the recommended Agent Computer runtime"
			return info
		}
		if runtimeID == RuntimeOrbStack {
			info.Detail = "OrbStack Docker context not found"
		} else if runtimeID == RuntimeDockerDesktop {
			info.Detail = "Docker Desktop context not found"
		}
		info.Available = false
		return info
	}
	info.Installed = true
	info.Available = dockerAvailable
	info.Context = selected.Name
	info.Endpoint = selected.Endpoint
	info.Detail = "Docker context configured"
	if activeContext.Name == selected.Name {
		info.Selected = true
		version, detail, err := dockerContextHealth(ctx, selected.Name)
		if err != nil {
			info.Healthy = false
			info.Detail = "Docker context unavailable: " + compact(err.Error())
		} else {
			info.Healthy = true
			info.Version = version
			info.Detail = detail
		}
	}
	return info
}

// InstallColima installs only the two official Homebrew formulae required by
// the Docker-backed Agent Computer. It is called only from an explicit local
// UI action; it never installs Homebrew itself or starts a VM.
func InstallColima(parent context.Context) error {
	brew, err := findExecutable("brew")
	if err != nil {
		return errors.New("Homebrew is not installed; install it from https://brew.sh, then run: " + ColimaInstallCommand)
	}
	colimaInstallMu.Lock()
	defer colimaInstallMu.Unlock()

	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	command := newCommandContext(ctx, brew, "install", "colima", "docker")
	output, err := command.CombinedOutput()
	if err != nil {
		detail := compact(string(output))
		if detail != "" {
			return fmt.Errorf("install Colima: %w: %s", err, detail)
		}
		return fmt.Errorf("install Colima: %w", err)
	}
	return nil
}

func stoppedColimaSelection(profile string) RuntimeSelection {
	if strings.TrimSpace(profile) == "" {
		profile = DefaultColimaProfile
	}
	home, _ := os.UserHomeDir()
	return RuntimeSelection{
		ID:       RuntimeColima,
		Name:     runtimeName(RuntimeColima),
		Context:  colimaContextName(profile),
		Endpoint: "unix://" + filepath.Join(home, ".colima", profile, "docker.sock"),
		Detail:   "Colima is installed and will start the dedicated " + profile + " profile when Agent Computer is requested",
	}
}

func colimaContextName(profile string) string {
	if strings.TrimSpace(profile) == "" || profile == "default" {
		return "colima"
	}
	return "colima-" + profile
}

func findExecutable(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	for _, directory := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		candidate := filepath.Join(directory, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

func discoverAppleContainer(ctx context.Context, selected bool) RuntimeInfo {
	info := RuntimeInfo{
		ID:                    RuntimeAppleContainer,
		Name:                  runtimeName(RuntimeAppleContainer),
		Kind:                  "apple_container",
		Selected:              selected,
		Experimental:          true,
		OpenSource:            true,
		SupportsAgentComputer: false,
		Detail:                "Apple Container adapter is not enabled yet; Xfce/Chromium frame and takeover validation is pending",
	}
	path, err := exec.LookPath("container")
	if err != nil {
		info.Detail = "Apple Container CLI is not installed"
		return info
	}
	info.Available = true
	if versionOutput, versionErr := runOutput(ctx, path, "system", "version", "--format", "json"); versionErr == nil {
		var components []struct {
			AppName string `json:"appName"`
			Version string `json:"version"`
		}
		if json.Unmarshal([]byte(versionOutput), &components) == nil {
			for _, component := range components {
				if component.AppName == "container" {
					info.Version = component.Version
					break
				}
			}
		}
	}
	if _, err := runOutput(ctx, path, "system", "status", "--format", "json"); err != nil {
		info.Detail = "Apple Container is installed; its system service is not healthy"
		return info
	}
	info.Healthy = true
	info.Detail = "Apple Container service is healthy; OpenAgentFleet adapter remains experimental"
	return info
}

func listDockerContexts(ctx context.Context) ([]dockerContext, error) {
	path, err := findExecutable("docker")
	if err != nil {
		return nil, err
	}
	output, err := runOutput(ctx, path, "context", "ls", "--format", "{{.Name}}\t{{.DockerEndpoint}}\t{{.Current}}")
	if err != nil {
		return nil, err
	}
	var contexts []dockerContext
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), "\t", 3)
		if len(fields) < 2 || strings.TrimSpace(fields[0]) == "" {
			continue
		}
		contexts = append(contexts, dockerContext{Name: strings.TrimSpace(fields[0]), Endpoint: strings.TrimSpace(fields[1]), Current: len(fields) == 3 && strings.EqualFold(strings.TrimSpace(fields[2]), "true")})
	}
	return contexts, nil
}

func findDockerContext(contexts []dockerContext, name string) (dockerContext, bool) {
	for _, candidate := range contexts {
		if candidate.Name == name {
			return candidate, true
		}
	}
	return dockerContext{}, false
}

func findRuntimeContext(contexts []dockerContext, runtimeID string) (dockerContext, bool) {
	for _, candidate := range contexts {
		if classifyDockerContext(candidate) == runtimeID {
			return candidate, true
		}
	}
	return dockerContext{}, false
}

func currentDockerContext(contexts []dockerContext) dockerContext {
	if envContext := strings.TrimSpace(os.Getenv("DOCKER_CONTEXT")); envContext != "" {
		if selected, ok := findDockerContext(contexts, envContext); ok {
			return selected
		}
	}
	for _, candidate := range contexts {
		if candidate.Current {
			return candidate
		}
	}
	return dockerContext{}
}

func classifyDockerContext(candidate dockerContext) string {
	name := strings.ToLower(candidate.Name)
	endpoint := strings.ToLower(candidate.Endpoint)
	switch {
	case name == "desktop-linux" || strings.Contains(name, "docker desktop"):
		return RuntimeDockerDesktop
	case strings.HasPrefix(name, "colima") || strings.Contains(endpoint, "/.colima/"):
		return RuntimeColima
	case strings.Contains(name, "orbstack") || strings.Contains(endpoint, "/.orbstack/"):
		return RuntimeOrbStack
	default:
		return RuntimeDocker
	}
}

func runtimeName(runtimeID string) string {
	switch runtimeID {
	case RuntimeDockerDesktop:
		return "Docker Desktop"
	case RuntimeColima:
		return "Colima + Docker"
	case RuntimeOrbStack:
		return "OrbStack + Docker"
	case RuntimeAppleContainer:
		return "Apple Container"
	case RuntimeDocker:
		return "Docker Engine"
	default:
		return "Automatic runtime"
	}
}

func dockerContextHealth(ctx context.Context, contextName string) (string, string, error) {
	args := []string{}
	if contextName != "" && contextName != "default" {
		args = append(args, "--context", contextName)
	}
	args = append(args, "info", "--format", "{{.ServerVersion}}|{{.OperatingSystem}}|{{.Architecture}}")
	output, err := runOutput(ctx, "docker", args...)
	if err != nil {
		return "", "", err
	}
	fields := strings.SplitN(strings.TrimSpace(output), "|", 3)
	if len(fields) < 3 {
		return strings.TrimSpace(output), "Docker context is healthy", nil
	}
	return fields[0], fmt.Sprintf("%s (%s)", fields[1], fields[2]), nil
}
