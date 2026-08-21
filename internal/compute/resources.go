package compute

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/robbyczgw-cla/openagentfleet/internal/preferences"
)

// ResourceConfig is the controller-side, validated resource contract for one
// visible Agent Computer. For Colima, CPU/RAM/disk are VM resources. Docker
// also receives matching per-container CPU/RAM bounds so Docker Desktop and
// OrbStack do not silently ignore the user's selection.
type ResourceConfig struct {
	CPUs      int    `json:"cpus"`
	MemoryGiB int    `json:"memory_gib"`
	DiskGiB   int    `json:"disk_gib"`
	SwapGiB   int    `json:"swap_gib"`
	OSImage   string `json:"os_image"`
}

const (
	defaultAgentComputerImage = "openagentfleet-agent-computer:ubuntu-24.04"
	imageUbuntu2404           = "ubuntu:24.04"
	imageUbuntu2604           = "ubuntu:26.04"
	imageDebian13             = "debian:13"
)

func DefaultResourceConfig() ResourceConfig {
	return ResourceConfig{
		CPUs:      preferences.ComputerDefaultCPUs,
		MemoryGiB: preferences.ComputerDefaultRAMGiB,
		DiskGiB:   preferences.ComputerDefaultDiskGiB,
		SwapGiB:   preferences.ComputerDefaultSwapGiB,
		OSImage:   preferences.ComputerDefaultOSImage,
	}
}

func ResourceConfigFromPreferences(value preferences.ComputerDefaults) ResourceConfig {
	resource := ResourceConfig{
		CPUs:      value.CPUs,
		MemoryGiB: value.RAMGiB,
		DiskGiB:   value.DiskGiB,
		SwapGiB:   value.SwapGiB,
		OSImage:   value.OSImage,
	}
	if resource.CPUs == 0 && resource.MemoryGiB == 0 && resource.DiskGiB == 0 && resource.SwapGiB == 0 && strings.TrimSpace(resource.OSImage) == "" {
		return DefaultResourceConfig()
	}
	return resource.Normalize()
}

func (r ResourceConfig) Normalize() ResourceConfig {
	defaults := DefaultResourceConfig()
	if r.CPUs < preferences.MinComputerCPUs || r.CPUs > preferences.MaxComputerCPUs {
		r.CPUs = defaults.CPUs
	}
	if r.MemoryGiB < preferences.MinComputerRAMGiB || r.MemoryGiB > preferences.MaxComputerRAMGiB {
		r.MemoryGiB = defaults.MemoryGiB
	}
	if r.DiskGiB < preferences.MinComputerDiskGiB || r.DiskGiB > preferences.MaxComputerDiskGiB {
		r.DiskGiB = defaults.DiskGiB
	}
	if r.SwapGiB < preferences.MinComputerSwapGiB || r.SwapGiB > preferences.MaxComputerSwapGiB {
		r.SwapGiB = defaults.SwapGiB
	}
	if _, err := baseImageForOS(r.OSImage); err != nil {
		r.OSImage = defaults.OSImage
	}
	return r
}

func (r ResourceConfig) Validate() error {
	if r.CPUs < preferences.MinComputerCPUs || r.CPUs > preferences.MaxComputerCPUs {
		return fmt.Errorf("computer CPUs must be between %d and %d", preferences.MinComputerCPUs, preferences.MaxComputerCPUs)
	}
	if r.MemoryGiB < preferences.MinComputerRAMGiB || r.MemoryGiB > preferences.MaxComputerRAMGiB {
		return fmt.Errorf("computer memory must be between %d and %d GiB", preferences.MinComputerRAMGiB, preferences.MaxComputerRAMGiB)
	}
	if r.DiskGiB < preferences.MinComputerDiskGiB || r.DiskGiB > preferences.MaxComputerDiskGiB {
		return fmt.Errorf("computer disk must be between %d and %d GiB", preferences.MinComputerDiskGiB, preferences.MaxComputerDiskGiB)
	}
	if r.SwapGiB < preferences.MinComputerSwapGiB || r.SwapGiB > preferences.MaxComputerSwapGiB {
		return fmt.Errorf("computer swap must be between %d and %d GiB", preferences.MinComputerSwapGiB, preferences.MaxComputerSwapGiB)
	}
	if _, err := baseImageForOS(r.OSImage); err != nil {
		return err
	}
	return nil
}

func (r ResourceConfig) BaseImage() string {
	image, err := baseImageForOS(r.OSImage)
	if err != nil {
		return imageUbuntu2404
	}
	return image
}

func (r ResourceConfig) ImageTag() string {
	if _, err := baseImageForOS(r.OSImage); err != nil {
		return defaultAgentComputerImage
	}
	return "openagentfleet-agent-computer:" + r.OSImage
}

func baseImageForOS(osImage string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(osImage)) {
	case preferences.OSImageUbuntu2404:
		return imageUbuntu2404, nil
	case preferences.OSImageUbuntu2604:
		return imageUbuntu2604, nil
	case preferences.OSImageDebian13:
		return imageDebian13, nil
	default:
		return "", fmt.Errorf("unsupported Agent Computer OS image %q", osImage)
	}
}

// HostStorageError is intentionally specific so the UI can present a retryable
// "free space" error instead of a generic Docker/Chromium failure.
type HostStorageError struct {
	Path      string
	FreeBytes uint64
	NeedBytes uint64
	Purpose   string
}

func (e *HostStorageError) Error() string {
	if e == nil {
		return "insufficient host storage"
	}
	return fmt.Sprintf("not enough free host storage for Agent Computer %s: %s free, at least %s required (%s)", e.Path, formatGiB(e.FreeBytes), formatGiB(e.NeedBytes), e.Purpose)
}

func formatGiB(bytes uint64) string {
	const gib = uint64(1024 * 1024 * 1024)
	if bytes < gib {
		return fmt.Sprintf("%d MiB", bytes/(1024*1024))
	}
	return fmt.Sprintf("%.1f GiB", float64(bytes)/float64(gib))
}

var diskFreeBytes = platformDiskFreeBytes

const (
	colimaImageBudgetGiB      = 6
	minimumColimaFreeGiB      = 8
	minimumDataFreeGiB        = 2
	linuxDockerImageBudgetGiB = 8
)

func (d *Docker) currentRuntimeID() string {
	d.runtimeMu.RLock()
	defer d.runtimeMu.RUnlock()
	if strings.TrimSpace(d.RuntimeID) == "" {
		switch runtime.GOOS {
		case "linux":
			return RuntimeDocker
		case "windows":
			return RuntimeDockerDesktop
		default:
			return RuntimeColima
		}
	}
	return d.RuntimeID
}

func (d *Docker) checkHostStorage(resource ResourceConfig) error {
	resource = resource.Normalize()
	if d.currentRuntimeID() == RuntimeColima {
		return d.checkColimaHostStorage(resource)
	}
	return d.checkDockerEngineStorage(resource)
}

func (d *Docker) checkColimaHostStorage(resource ResourceConfig) error {
	profileRoot, err := colimaStorageRoot()
	if err != nil {
		return err
	}
	colimaNeedGiB := resource.DiskGiB + resource.SwapGiB + colimaImageBudgetGiB
	if colimaNeedGiB < minimumColimaFreeGiB {
		colimaNeedGiB = minimumColimaFreeGiB
	}
	if err := requireHostFree(profileRoot, uint64(colimaNeedGiB)*1024*1024*1024, "the Colima VM, swap and image layers"); err != nil {
		return err
	}
	dataPath := d.Workspace
	if strings.TrimSpace(dataPath) == "" {
		dataPath = profileRoot
	}
	return requireHostFree(dataPath, uint64(minimumDataFreeGiB)*1024*1024*1024, "the Agent Computer workspace and browser profile")
}

func (d *Docker) checkDockerEngineStorage(resource ResourceConfig) error {
	_ = resource
	dataPath := d.Workspace
	if strings.TrimSpace(dataPath) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home for Agent Computer storage: %w", err)
		}
		dataPath = home
	}
	if err := requireHostFree(dataPath, uint64(minimumDataFreeGiB)*1024*1024*1024, "the Agent Computer workspace and browser profile"); err != nil {
		return err
	}
	imageRoot := linuxDockerStorageRoot()
	if imageRoot == "" {
		imageRoot = dataPath
	}
	return requireHostFree(imageRoot, uint64(linuxDockerImageBudgetGiB)*1024*1024*1024, "the Agent Computer image layers")
}

func linuxDockerStorageRoot() string {
	for _, candidate := range []string{"/var/lib/docker", "/var/lib/containerd"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func requireHostFree(path string, need uint64, purpose string) error {
	free, resolved, err := diskFreeBytes(path)
	if err != nil {
		return fmt.Errorf("check free host storage at %s: %w", path, err)
	}
	if free < need {
		return &HostStorageError{Path: resolved, FreeBytes: free, NeedBytes: need, Purpose: purpose}
	}
	return nil
}

func colimaStorageRoot() (string, error) {
	root := strings.TrimSpace(os.Getenv("COLIMA_HOME"))
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home for Colima storage: %w", err)
		}
		return filepath.Join(home, ".colima"), nil
	}
	if root == "~" || strings.HasPrefix(root, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home for Colima storage: %w", err)
		}
		root = filepath.Join(home, strings.TrimPrefix(root, "~/"))
	}
	return filepath.Abs(root)
}

func swapUnsupportedError(profile string) error {
	return errors.New("Colima guest swap could not be configured for profile " + profile)
}
