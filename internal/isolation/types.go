package isolation

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/robbyczgw-cla/openagentfleet/internal/ospath"
)

var (
	// ErrInvalidSpec means a plan omitted a mandatory or safe value.
	ErrInvalidSpec = errors.New("invalid isolation spec")
	// ErrForbiddenMount means a mount would expose a host-sensitive path or is
	// outside a controller-approved root.
	ErrForbiddenMount = errors.New("forbidden isolation mount")
	// ErrNativeDisabled means native host execution was not explicitly enabled
	// by controller policy and this exact request.
	ErrNativeDisabled = errors.New("native-host isolation is disabled")
	// ErrProfileUnsupported is returned for a known profile whose executor has
	// deliberately not shipped yet.
	ErrProfileUnsupported = errors.New("isolation profile is unsupported")
	// ErrEgressEnforcerRequired means Docker alone cannot enforce the requested
	// network allowlist.
	ErrEgressEnforcerRequired = errors.New("network allowlist requires an external egress enforcer")
)

// Profile identifies the execution boundary requested by the controller.
// No profile silently falls back to another profile.
type Profile string

const (
	ProfileDocker         Profile = "docker"
	ProfileNativeHost     Profile = "native_host"
	ProfileAppleContainer Profile = "apple_container"
)

// MountMode is deliberately explicit. No mount is implicitly writable.
type MountMode string

const (
	MountReadOnly  MountMode = "read_only"
	MountReadWrite MountMode = "read_write"
)

// NetworkMode describes a policy, not a promise that the local Docker CLI can
// magically enforce domain filtering. Docker can enforce NetworkOff. An
// allowlist requires a separately configured egress-enforcing network/proxy.
type NetworkMode string

const (
	NetworkOff       NetworkMode = "off"
	NetworkAllowlist NetworkMode = "allowlist"
)

// SecretSource is an opaque, controller-owned secret reference source. It is
// never a secret value and must not be interpreted by a worker directly.
type SecretSource string

const (
	SecretSourceKeychain SecretSource = "keychain"
	SecretSourceHandoff  SecretSource = "handoff"
)

// Mount maps one exact controller-approved host path into a sandbox.
type Mount struct {
	HostPath  string    `json:"host_path"`
	GuestPath string    `json:"guest_path"`
	Mode      MountMode `json:"mode"`
}

// Tmpfs is a writable, guest-private filesystem. It avoids making the root
// filesystem writable merely to provide a temporary directory.
type Tmpfs struct {
	GuestPath string `json:"guest_path"`
	SizeBytes uint64 `json:"size_bytes"`
	Mode      uint32 `json:"mode"`
}

// EgressRule is a CIDR-and-port request for an external egress enforcer. DNS
// names are intentionally not accepted because Docker's run arguments alone
// cannot enforce a domain allowlist safely.
type EgressRule struct {
	CIDR     string `json:"cidr"`
	Protocol string `json:"protocol"`
	Port     uint16 `json:"port"`
}

// NetworkPolicy defaults to NetworkOff. Allowlist requests are validated, but
// Plan returns ErrEgressEnforcerRequired until a runtime implements an actual
// firewall or authenticated egress proxy.
type NetworkPolicy struct {
	Mode  NetworkMode  `json:"mode"`
	Allow []EgressRule `json:"allow,omitempty"`
}

// ResourceLimits are mandatory for Docker plans. CPUMilli keeps Docker argument
// rendering deterministic and avoids a floating-point policy surface.
type ResourceLimits struct {
	CPUMilli    uint32 `json:"cpu_milli"`
	MemoryBytes uint64 `json:"memory_bytes"`
	PIDs        uint32 `json:"pids"`
}

// SecretReference is intentionally unable to carry secret material. The
// opaque Reference is forwarded only as a reference environment value; a later
// controller-owned broker may resolve it without placing a secret in this plan.
type SecretReference struct {
	Environment string       `json:"environment"`
	Source      SecretSource `json:"source"`
	Reference   string       `json:"reference"`
}

// Spec is one session's requested worker sandbox. Command is argv, never a
// shell string. Environment values other than opaque SecretReferences are not
// part of this contract.
type Spec struct {
	SessionID string  `json:"session_id"`
	Profile   Profile `json:"profile"`

	Image   string   `json:"image,omitempty"`
	Command []string `json:"command"`
	Workdir string   `json:"workdir"`
	User    string   `json:"user,omitempty"`

	Mounts  []Mount           `json:"mounts,omitempty"`
	Tmpfs   []Tmpfs           `json:"tmpfs,omitempty"`
	Network NetworkPolicy     `json:"network"`
	Limits  ResourceLimits    `json:"limits"`
	Secrets []SecretReference `json:"secret_references,omitempty"`

	// NativeReason is an auditable explanation for the exceptional weaker
	// native-host path. It cannot enable native use by itself.
	NativeReason string `json:"native_reason,omitempty"`
}

// Policy is set by the OpenAgentFleet controller, never by an individual
// worker. All host paths must be inside ApprovedMountRoots. Write mounts also
// require a more specific WritableMountRoots grant.
type Policy struct {
	ApprovedMountRoots []string
	WritableMountRoots []string
	StateSecretRoots   []string
	AllowNativeHost    bool
}

// DefaultPolicy is intentionally unusable for host mounts until the controller
// explicitly supplies an approved root. Native host execution stays disabled.
func DefaultPolicy() Policy {
	return Policy{}
}

// Plan is a deterministic, non-executing backend request. DockerArgs excludes
// the docker binary and must be passed as argv, never through a shell.
type Plan struct {
	Profile    Profile
	SpecHash   string
	DockerArgs []string
	Native     *NativePlan
	Cleanup    CleanupPlan
}

// NativePlan is a declaration for a future native executor. It carries no
// privilege escalation, mount, or network bypass facility.
type NativePlan struct {
	Command       []string
	Workdir       string
	SecretRefs    []SecretReference
	NativeReason  string
	SecurityNotes []string
}

// CleanupPlan contains only a label-scoped container lifecycle. It never owns
// host workspaces, mounted paths, profiles, or secrets.
type CleanupPlan struct {
	ContainerName string
	OwnerLabels   []string
	StopArgs      []string
	RemoveArgs    []string
	OrphanQuery   []string
}

func (p Policy) normalized() (Policy, error) {
	var err error
	p.ApprovedMountRoots, err = normalizeRoots(p.ApprovedMountRoots)
	if err != nil {
		return Policy{}, fmt.Errorf("approved mount roots: %w", err)
	}
	p.WritableMountRoots, err = normalizeRoots(p.WritableMountRoots)
	if err != nil {
		return Policy{}, fmt.Errorf("writable mount roots: %w", err)
	}
	p.StateSecretRoots, err = normalizeRoots(p.StateSecretRoots)
	if err != nil {
		return Policy{}, fmt.Errorf("state secret roots: %w", err)
	}
	for _, root := range append(append([]string{}, p.ApprovedMountRoots...), p.WritableMountRoots...) {
		if forbiddenHostPath(root) {
			return Policy{}, fmt.Errorf("%w: forbidden controller root %q", ErrForbiddenMount, root)
		}
	}
	return p, nil
}

func normalizeRoots(paths []string) ([]string, error) {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if err := validateAbsolutePath(path); err != nil {
			return nil, err
		}
		clean := filepath.Clean(path)
		if _, exists := seen[clean]; !exists {
			seen[clean] = struct{}{}
			result = append(result, clean)
		}
	}
	sort.Strings(result)
	return result, nil
}

func validateAbsolutePath(path string) error {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) || !ospath.IsAbs(path) {
		return fmt.Errorf("%w: path must be absolute", ErrInvalidSpec)
	}
	return nil
}
