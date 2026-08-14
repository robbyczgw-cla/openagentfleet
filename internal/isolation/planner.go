package isolation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	maxSessionIDLength = 64
	maxCommandArgs     = 128
	maxPathLength      = 4096
)

var (
	sessionIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	userPattern      = regexp.MustCompile(`^[1-9][0-9]*:[1-9][0-9]*$`)
	envPattern       = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
	refPattern       = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,128}$`)
)

// Planner turns controller-owned specs into backend plans. It never invokes a
// runtime. Its host-path checks resolve symlinks before applying policy, so a
// benign-looking workspace symlink cannot reach a forbidden path.
type Planner struct {
	policy Policy
}

// NewPlanner validates and freezes controller policy. It performs no Docker or
// process calls. Controller mount roots must already exist when a plan is made.
func NewPlanner(policy Policy) (Planner, error) {
	normalized, err := policy.normalized()
	if err != nil {
		return Planner{}, err
	}
	return Planner{policy: normalized}, nil
}

// Validate checks a spec, including resolved filesystem paths, without
// creating a container or invoking a worker.
func (p Planner) Validate(spec Spec) error {
	_, err := p.prepare(spec)
	return err
}

// Plan validates a spec and returns a deterministic non-executing request.
// Docker allowlists fail closed until a real egress enforcer is wired in.
func (p Planner) Plan(spec Spec) (Plan, error) {
	prepared, err := p.prepare(spec)
	if err != nil {
		return Plan{}, err
	}
	hash := prepared.hash()
	cleanup := cleanupPlan(prepared.SessionID)

	switch prepared.Profile {
	case ProfileDocker:
		if prepared.Network.Mode == NetworkAllowlist {
			return Plan{}, ErrEgressEnforcerRequired
		}
		return Plan{
			Profile:    ProfileDocker,
			SpecHash:   hash,
			DockerArgs: dockerArgs(prepared),
			Cleanup:    cleanup,
		}, nil
	case ProfileNativeHost:
		return Plan{
			Profile:  ProfileNativeHost,
			SpecHash: hash,
			Native: &NativePlan{
				Command:       append([]string(nil), prepared.Command...),
				Workdir:       prepared.Workdir,
				SecretRefs:    append([]SecretReference(nil), prepared.Secrets...),
				NativeReason:  prepared.NativeReason,
				SecurityNotes: []string{"native host is explicitly enabled but is weaker than a disposable container", "a future native executor must preserve environment filtering, resource limits, offline network enforcement, and explicit controller approvals"},
			},
			Cleanup: cleanup,
		}, nil
	case ProfileAppleContainer:
		return Plan{}, fmt.Errorf("%w: Apple Container worker execution is reserved until backend acceptance tests exist", ErrProfileUnsupported)
	default:
		return Plan{}, fmt.Errorf("%w: unknown profile %q", ErrInvalidSpec, prepared.Profile)
	}
}

func (p Planner) prepare(spec Spec) (Spec, error) {
	if err := validateSessionID(spec.SessionID); err != nil {
		return Spec{}, err
	}
	if err := validateCommand(spec.Command); err != nil {
		return Spec{}, err
	}
	if err := validateGuestPath(spec.Workdir, "workdir"); err != nil {
		return Spec{}, err
	}
	if err := validateLimits(spec.Limits); err != nil {
		return Spec{}, err
	}
	if err := validateNetwork(spec.Network); err != nil {
		return Spec{}, err
	}
	if err := validateSecrets(spec.Secrets); err != nil {
		return Spec{}, err
	}

	switch spec.Profile {
	case ProfileDocker:
		if strings.TrimSpace(spec.Image) == "" || strings.ContainsAny(spec.Image, "\t\r\n\x00") {
			return Spec{}, fmt.Errorf("%w: docker image is required", ErrInvalidSpec)
		}
		if !userPattern.MatchString(spec.User) {
			return Spec{}, fmt.Errorf("%w: docker user must be non-root numeric uid:gid", ErrInvalidSpec)
		}
		if len(spec.Tmpfs) == 0 {
			return Spec{}, fmt.Errorf("%w: docker plan requires at least one guest-private tmpfs", ErrInvalidSpec)
		}
	case ProfileNativeHost:
		if !p.policy.AllowNativeHost {
			return Spec{}, ErrNativeDisabled
		}
		if strings.TrimSpace(spec.NativeReason) == "" {
			return Spec{}, fmt.Errorf("%w: native host requires an auditable reason", ErrNativeDisabled)
		}
		if len(spec.Mounts) != 0 || len(spec.Tmpfs) != 0 {
			return Spec{}, fmt.Errorf("%w: native host does not accept container mounts or tmpfs", ErrInvalidSpec)
		}
		if spec.Network.Mode != NetworkOff {
			return Spec{}, fmt.Errorf("%w: native host network policy is not enforceable in this planner", ErrEgressEnforcerRequired)
		}
	case ProfileAppleContainer:
		// Validate input enough to give a configuration error before declaring
		// the backend unsupported. There is no fallback to Docker or native.
		if strings.TrimSpace(spec.Image) == "" {
			return Spec{}, fmt.Errorf("%w: Apple Container image is required", ErrInvalidSpec)
		}
	default:
		return Spec{}, fmt.Errorf("%w: unknown profile %q", ErrInvalidSpec, spec.Profile)
	}

	resolvedPolicy, err := p.resolvePolicyRoots()
	if err != nil {
		return Spec{}, err
	}
	if spec.Profile == ProfileNativeHost {
		resolvedWorkdir, err := validateAndResolveNativeWorkdir(spec.Workdir, resolvedPolicy)
		if err != nil {
			return Spec{}, err
		}
		spec.Workdir = resolvedWorkdir
	}
	spec.Mounts, err = validateAndResolveMounts(spec.Mounts, resolvedPolicy)
	if err != nil {
		return Spec{}, err
	}
	spec.Tmpfs, err = validateTmpfs(spec.Tmpfs, spec.Mounts)
	if err != nil {
		return Spec{}, err
	}
	sort.Slice(spec.Secrets, func(i, j int) bool { return spec.Secrets[i].Environment < spec.Secrets[j].Environment })
	sort.Slice(spec.Network.Allow, func(i, j int) bool {
		left := spec.Network.Allow[i]
		right := spec.Network.Allow[j]
		if left.CIDR != right.CIDR {
			return left.CIDR < right.CIDR
		}
		if left.Protocol != right.Protocol {
			return left.Protocol < right.Protocol
		}
		return left.Port < right.Port
	})
	return spec, nil
}

func validateAndResolveNativeWorkdir(path string, policy Policy) (string, error) {
	if len(policy.ApprovedMountRoots) == 0 {
		return "", fmt.Errorf("%w: native host requires a controller-approved workdir root", ErrForbiddenMount)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%w: resolve native workdir %q: %v", ErrForbiddenMount, path, err)
	}
	resolved = filepath.Clean(resolved)
	if forbiddenHostPath(resolved) || pathWithinAny(resolved, policy.StateSecretRoots) || !pathWithinAny(resolved, policy.ApprovedMountRoots) {
		return "", fmt.Errorf("%w: native workdir %q", ErrForbiddenMount, resolved)
	}
	return resolved, nil
}

func (p Planner) resolvePolicyRoots() (Policy, error) {
	resolved := p.policy
	var err error
	resolved.ApprovedMountRoots, err = resolveRoots(p.policy.ApprovedMountRoots)
	if err != nil {
		return Policy{}, fmt.Errorf("approved mount roots: %w", err)
	}
	resolved.WritableMountRoots, err = resolveRoots(p.policy.WritableMountRoots)
	if err != nil {
		return Policy{}, fmt.Errorf("writable mount roots: %w", err)
	}
	resolved.StateSecretRoots, err = resolveRoots(p.policy.StateSecretRoots)
	if err != nil {
		return Policy{}, fmt.Errorf("state secret roots: %w", err)
	}
	return resolved, nil
}

func resolveRoots(roots []string) ([]string, error) {
	result := make([]string, 0, len(roots))
	for _, root := range roots {
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", root, err)
		}
		resolved = filepath.Clean(resolved)
		if forbiddenHostPath(resolved) {
			return nil, fmt.Errorf("%w: resolved controller root %q", ErrForbiddenMount, resolved)
		}
		result = append(result, resolved)
	}
	sort.Strings(result)
	return result, nil
}

func validateSessionID(id string) error {
	if len(id) == 0 || len(id) > maxSessionIDLength || !sessionIDPattern.MatchString(id) {
		return fmt.Errorf("%w: session id must be lower-case alphanumeric, underscore, or dash", ErrInvalidSpec)
	}
	return nil
}

func validateCommand(command []string) error {
	if len(command) == 0 || len(command) > maxCommandArgs {
		return fmt.Errorf("%w: command must contain 1-%d argv entries", ErrInvalidSpec, maxCommandArgs)
	}
	for _, value := range command {
		if strings.TrimSpace(value) == "" || strings.ContainsRune(value, 0) {
			return fmt.Errorf("%w: command entries must be non-empty argv values", ErrInvalidSpec)
		}
	}
	return nil
}

func validateLimits(limits ResourceLimits) error {
	if limits.CPUMilli == 0 || limits.MemoryBytes == 0 || limits.PIDs == 0 {
		return fmt.Errorf("%w: cpu, memory, and pid limits are mandatory", ErrInvalidSpec)
	}
	return nil
}

func validateNetwork(network NetworkPolicy) error {
	switch network.Mode {
	case NetworkOff:
		if len(network.Allow) != 0 {
			return fmt.Errorf("%w: offline network cannot contain allow rules", ErrInvalidSpec)
		}
	case NetworkAllowlist:
		if len(network.Allow) == 0 {
			return fmt.Errorf("%w: allowlist requires at least one CIDR-and-port rule", ErrInvalidSpec)
		}
		for _, rule := range network.Allow {
			if _, _, err := net.ParseCIDR(rule.CIDR); err != nil {
				return fmt.Errorf("%w: allowlist CIDR %q: %v", ErrInvalidSpec, rule.CIDR, err)
			}
			if rule.Protocol != "tcp" && rule.Protocol != "udp" {
				return fmt.Errorf("%w: allowlist protocol must be tcp or udp", ErrInvalidSpec)
			}
			if rule.Port == 0 {
				return fmt.Errorf("%w: allowlist port must be non-zero", ErrInvalidSpec)
			}
		}
	default:
		return fmt.Errorf("%w: unknown network mode %q", ErrInvalidSpec, network.Mode)
	}
	return nil
}

func validateSecrets(secrets []SecretReference) error {
	seen := map[string]struct{}{}
	for _, secret := range secrets {
		if !envPattern.MatchString(secret.Environment) || !refPattern.MatchString(secret.Reference) {
			return fmt.Errorf("%w: secret references require safe environment names and opaque reference identifiers", ErrInvalidSpec)
		}
		if secret.Source != SecretSourceKeychain && secret.Source != SecretSourceHandoff {
			return fmt.Errorf("%w: unknown secret reference source %q", ErrInvalidSpec, secret.Source)
		}
		if _, exists := seen[secret.Environment]; exists {
			return fmt.Errorf("%w: duplicate secret reference environment %q", ErrInvalidSpec, secret.Environment)
		}
		seen[secret.Environment] = struct{}{}
	}
	return nil
}

func validateGuestPath(path, description string) error {
	if err := validateAbsolutePath(path); err != nil {
		return fmt.Errorf("%s: %w", description, err)
	}
	if len(path) > maxPathLength || filepath.Clean(path) == "/" || forbiddenGuestPath(filepath.Clean(path)) {
		return fmt.Errorf("%w: forbidden %s path %q", ErrInvalidSpec, description, path)
	}
	return nil
}

func validateAndResolveMounts(mounts []Mount, policy Policy) ([]Mount, error) {
	if len(mounts) == 0 {
		return nil, nil
	}
	if len(policy.ApprovedMountRoots) == 0 {
		return nil, fmt.Errorf("%w: controller has no approved mount roots", ErrForbiddenMount)
	}
	guestPaths := map[string]struct{}{}
	result := make([]Mount, 0, len(mounts))
	for _, mount := range mounts {
		if err := validateAbsolutePath(mount.HostPath); err != nil || len(mount.HostPath) > maxPathLength {
			return nil, fmt.Errorf("%w: host path %q", ErrForbiddenMount, mount.HostPath)
		}
		if err := validateGuestPath(mount.GuestPath, "mount target"); err != nil {
			return nil, err
		}
		resolved, err := filepath.EvalSymlinks(mount.HostPath)
		if err != nil {
			return nil, fmt.Errorf("%w: resolve host path %q: %v", ErrForbiddenMount, mount.HostPath, err)
		}
		resolved = filepath.Clean(resolved)
		if forbiddenHostPath(resolved) || pathWithinAny(resolved, policy.StateSecretRoots) {
			return nil, fmt.Errorf("%w: host path %q", ErrForbiddenMount, resolved)
		}
		if !pathWithinAny(resolved, policy.ApprovedMountRoots) {
			return nil, fmt.Errorf("%w: host path %q is outside controller-approved roots", ErrForbiddenMount, resolved)
		}
		if mount.Mode != MountReadOnly && mount.Mode != MountReadWrite {
			return nil, fmt.Errorf("%w: unknown mount mode %q", ErrInvalidSpec, mount.Mode)
		}
		if mount.Mode == MountReadWrite && !pathWithinAny(resolved, policy.WritableMountRoots) {
			return nil, fmt.Errorf("%w: writable host path %q is outside controller-approved writable roots", ErrForbiddenMount, resolved)
		}
		guest := filepath.Clean(mount.GuestPath)
		if pathOverlapsAny(guest, guestPaths) {
			return nil, fmt.Errorf("%w: duplicate guest mount target %q", ErrInvalidSpec, guest)
		}
		guestPaths[guest] = struct{}{}
		result = append(result, Mount{HostPath: resolved, GuestPath: guest, Mode: mount.Mode})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].GuestPath == result[j].GuestPath {
			return result[i].HostPath < result[j].HostPath
		}
		return result[i].GuestPath < result[j].GuestPath
	})
	return result, nil
}

func validateTmpfs(tmpfs []Tmpfs, mounts []Mount) ([]Tmpfs, error) {
	guestPaths := map[string]struct{}{}
	for _, mount := range mounts {
		guestPaths[mount.GuestPath] = struct{}{}
	}
	result := make([]Tmpfs, 0, len(tmpfs))
	for _, item := range tmpfs {
		if err := validateGuestPath(item.GuestPath, "tmpfs"); err != nil {
			return nil, err
		}
		if item.SizeBytes == 0 || item.Mode == 0 || item.Mode > 0o1777 {
			return nil, fmt.Errorf("%w: tmpfs %q needs a bounded size and valid mode", ErrInvalidSpec, item.GuestPath)
		}
		guest := filepath.Clean(item.GuestPath)
		if pathOverlapsAny(guest, guestPaths) {
			return nil, fmt.Errorf("%w: overlapping tmpfs or mount target %q", ErrInvalidSpec, guest)
		}
		guestPaths[guest] = struct{}{}
		result = append(result, Tmpfs{GuestPath: guest, SizeBytes: item.SizeBytes, Mode: item.Mode})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].GuestPath < result[j].GuestPath })
	return result, nil
}

func forbiddenHostPath(path string) bool {
	clean := filepath.Clean(path)
	if clean == "/" || pathWithinAny(clean, []string{"/etc", "/private/etc", "/var/run", "/run", "/dev", "/proc", "/sys", "/root", "/Users", "/home"}) {
		return true
	}
	if strings.HasSuffix(clean, "/docker.sock") || strings.Contains(clean, "/.docker/") || strings.HasSuffix(clean, "/.docker") || strings.Contains(clean, "/.colima/") || strings.HasSuffix(clean, "/.colima") || strings.Contains(clean, "/.orbstack/") || strings.HasSuffix(clean, "/.orbstack") {
		return true
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && pathWithinAny(clean, []string{filepath.Clean(home)}) {
		return true
	}
	return false
}

func forbiddenGuestPath(path string) bool {
	return pathWithinAny(path, []string{"/etc", "/proc", "/sys", "/dev", "/root", "/var/run"})
}

func pathWithinAny(path string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

func pathOverlapsAny(path string, existing map[string]struct{}) bool {
	for other := range existing {
		if pathWithinAny(path, []string{other}) || pathWithinAny(other, []string{path}) {
			return true
		}
	}
	return false
}

func (s Spec) hash() string {
	var values []string
	values = append(values, string(s.Profile), s.SessionID, s.Image, s.Workdir, s.User, s.NativeReason, string(s.Network.Mode))
	values = append(values, strconv.FormatUint(uint64(s.Limits.CPUMilli), 10), strconv.FormatUint(s.Limits.MemoryBytes, 10), strconv.FormatUint(uint64(s.Limits.PIDs), 10))
	values = append(values, s.Command...)
	for _, mount := range s.Mounts {
		values = append(values, string(mount.Mode), mount.HostPath, mount.GuestPath)
	}
	for _, item := range s.Tmpfs {
		values = append(values, item.GuestPath, strconv.FormatUint(item.SizeBytes, 10), strconv.FormatUint(uint64(item.Mode), 10))
	}
	for _, rule := range s.Network.Allow {
		values = append(values, rule.CIDR, rule.Protocol, strconv.FormatUint(uint64(rule.Port), 10))
	}
	for _, secret := range s.Secrets {
		values = append(values, secret.Environment, string(secret.Source), secret.Reference)
	}
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(sum[:])
}
