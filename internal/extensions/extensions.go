// Package extensions models optional plugin and connector lifecycle metadata.
//
// It intentionally has no process, network, filesystem, credential, or MCP
// execution capability. A future controller can use this package as a
// validation and audit boundary before it implements an installer or runtime.
package extensions

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const CurrentManifestVersion = 1

var (
	extensionIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
	capabilityPattern  = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
	versionPattern     = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	secretRefPattern   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
)

// Kind determines the user-facing role of an optional extension. It does not
// grant any ability by itself.
type Kind string

const (
	KindPlugin    Kind = "plugin"
	KindConnector Kind = "connector"
)

// State tracks metadata lifecycle, never a running process.
type State string

const (
	StateInstalled   State = "installed"
	StateEnabled     State = "enabled"
	StateDisabled    State = "disabled"
	StateUpdating    State = "updating"
	StateUninstalled State = "uninstalled"
	StateFailed      State = "failed"
)

type HealthState string

const (
	HealthUnknown   HealthState = "unknown"
	HealthHealthy   HealthState = "healthy"
	HealthDegraded  HealthState = "degraded"
	HealthUnhealthy HealthState = "unhealthy"
)

// Provenance lets a UI show exactly where an extension came from. Digest must
// be a SHA-256 of the externally obtained immutable release artifact.
type Provenance struct {
	OriginURL    string `json:"origin_url"`
	Publisher    string `json:"publisher"`
	DigestSHA256 string `json:"digest_sha256"`
	License      string `json:"license"`
	LicenseURL   string `json:"license_url,omitempty"`
	Verified     bool   `json:"verified"`
}

// SecretRef identifies a keychain/vault reference required by an extension.
// It deliberately has no field that can contain a secret value.
type SecretRef struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Description string `json:"description,omitempty"`
}

// Manifest is immutable extension metadata. Version is an exact SemVer pin;
// ranges, "latest", and floating branches are rejected.
type Manifest struct {
	SchemaVersion int         `json:"schema_version"`
	ID            string      `json:"id"`
	Kind          Kind        `json:"kind"`
	Name          string      `json:"name"`
	Version       string      `json:"version"`
	Provenance    Provenance  `json:"provenance"`
	Capabilities  []string    `json:"capabilities"`
	SecretRefs    []SecretRef `json:"secret_refs,omitempty"`
}

// Policy is deliberately deny-first. Its zero value denies enable and any
// unknown provenance. A product settings surface must opt into both.
type Policy struct {
	ExperimentalExtensionsEnabled bool `json:"experimental_extensions_enabled"`
	AllowUnverifiedProvenance     bool `json:"allow_unverified_provenance"`
}

// HealthReport is an observation supplied by a future health-check adapter;
// this package never probes an endpoint or starts an extension.
type HealthReport struct {
	State      HealthState `json:"state"`
	Detail     string      `json:"detail,omitempty"`
	ObservedAt time.Time   `json:"observed_at"`
}

type AuditEvent struct {
	Action string    `json:"action"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

// Extension retains provenance after uninstall so history remains auditable.
// Enabled is duplicated intentionally for an obvious UI guard and must always
// agree with StateEnabled.
type Extension struct {
	Manifest    Manifest     `json:"manifest"`
	State       State        `json:"state"`
	Enabled     bool         `json:"enabled"`
	Health      HealthReport `json:"health"`
	InstalledAt time.Time    `json:"installed_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	Revision    uint64       `json:"revision"`
	Audit       []AuditEvent `json:"audit"`
}

// UpdatePlan is a reviewable, non-executing candidate update. It contains no
// installer command, archive path, credentials, or executable payload.
type UpdatePlan struct {
	ExtensionID string    `json:"extension_id"`
	FromVersion string    `json:"from_version"`
	Candidate   Manifest  `json:"candidate"`
	CreatedAt   time.Time `json:"created_at"`
}

func Install(manifest Manifest, policy Policy, now time.Time) (Extension, error) {
	if err := manifest.Validate(policy); err != nil {
		return Extension{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return Extension{
		Manifest: canonicalManifest(manifest), State: StateInstalled, Enabled: false,
		Health:      HealthReport{State: HealthUnknown, ObservedAt: now},
		InstalledAt: now, UpdatedAt: now, Revision: 1,
		Audit: []AuditEvent{{Action: "installed", Detail: "disabled by default", At: now}},
	}, nil
}

func (m Manifest) Validate(policy Policy) error {
	if m.SchemaVersion != CurrentManifestVersion {
		return fmt.Errorf("unsupported manifest schema version %d", m.SchemaVersion)
	}
	if !extensionIDPattern.MatchString(m.ID) {
		return fmt.Errorf("invalid extension id %q", m.ID)
	}
	if m.Kind != KindPlugin && m.Kind != KindConnector {
		return fmt.Errorf("invalid extension kind %q", m.Kind)
	}
	if strings.TrimSpace(m.Name) == "" || len(m.Name) > 120 {
		return errors.New("manifest name must be 1-120 characters")
	}
	if !versionPattern.MatchString(m.Version) {
		return fmt.Errorf("version must be an exact semver pin, got %q", m.Version)
	}
	if err := m.Provenance.Validate(policy); err != nil {
		return fmt.Errorf("provenance: %w", err)
	}
	if len(m.Capabilities) > 64 {
		return errors.New("too many capabilities")
	}
	seen := make(map[string]struct{}, len(m.Capabilities))
	for _, capability := range m.Capabilities {
		if !capabilityPattern.MatchString(capability) {
			return fmt.Errorf("invalid capability %q", capability)
		}
		if _, ok := seen[capability]; ok {
			return fmt.Errorf("duplicate capability %q", capability)
		}
		seen[capability] = struct{}{}
	}
	if len(m.SecretRefs) > 32 {
		return errors.New("too many secret references")
	}
	secretNames := make(map[string]struct{}, len(m.SecretRefs))
	for _, ref := range m.SecretRefs {
		if !secretRefPattern.MatchString(ref.Name) {
			return fmt.Errorf("invalid secret reference name %q", ref.Name)
		}
		if len(ref.Description) > 240 {
			return fmt.Errorf("secret reference %q description exceeds 240 characters", ref.Name)
		}
		if _, ok := secretNames[ref.Name]; ok {
			return fmt.Errorf("duplicate secret reference %q", ref.Name)
		}
		secretNames[ref.Name] = struct{}{}
	}
	return nil
}

func (p Provenance) Validate(policy Policy) error {
	if err := ValidatePublicHTTPSURL(p.OriginURL); err != nil {
		return fmt.Errorf("origin_url: %w", err)
	}
	if strings.TrimSpace(p.Publisher) == "" || len(p.Publisher) > 120 {
		return errors.New("publisher must be 1-120 characters")
	}
	if len(p.DigestSHA256) != sha256.Size*2 {
		return errors.New("digest_sha256 must be a SHA-256 hex digest")
	}
	if _, err := hex.DecodeString(p.DigestSHA256); err != nil || strings.ToLower(p.DigestSHA256) != p.DigestSHA256 {
		return errors.New("digest_sha256 must be lowercase hexadecimal")
	}
	if strings.TrimSpace(p.License) == "" || len(p.License) > 160 {
		return errors.New("license must be 1-160 characters")
	}
	if p.LicenseURL != "" {
		if err := ValidatePublicHTTPSURL(p.LicenseURL); err != nil {
			return fmt.Errorf("license_url: %w", err)
		}
	}
	if !p.Verified && !policy.AllowUnverifiedProvenance {
		return errors.New("unverified provenance is disabled by policy")
	}
	return nil
}

func (e Extension) Enable(policy Policy, now time.Time) (Extension, error) {
	if !policy.ExperimentalExtensionsEnabled {
		return Extension{}, errors.New("extensions are experimental and disabled by policy")
	}
	if err := e.Manifest.Validate(policy); err != nil {
		return Extension{}, err
	}
	if e.State == StateUninstalled {
		return Extension{}, errors.New("cannot enable an uninstalled extension")
	}
	if e.State == StateEnabled {
		return e, nil
	}
	return e.transition(StateEnabled, true, "enabled", "metadata enabled; execution remains external", now), nil
}

func (e Extension) Disable(now time.Time) (Extension, error) {
	if e.State == StateUninstalled {
		return Extension{}, errors.New("cannot disable an uninstalled extension")
	}
	if e.State == StateDisabled || e.State == StateInstalled {
		return e, nil
	}
	return e.transition(StateDisabled, false, "disabled", "execution must be stopped by an external runtime", now), nil
}

func (e Extension) RecordHealth(report HealthReport, now time.Time) (Extension, error) {
	if e.State == StateUninstalled {
		return Extension{}, errors.New("cannot report health for an uninstalled extension")
	}
	if report.State != HealthUnknown && report.State != HealthHealthy && report.State != HealthDegraded && report.State != HealthUnhealthy {
		return Extension{}, fmt.Errorf("invalid health state %q", report.State)
	}
	if len(report.Detail) > 1000 {
		return Extension{}, errors.New("health detail exceeds 1000 characters")
	}
	if report.ObservedAt.IsZero() {
		report.ObservedAt = now
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	e.Health = report
	e.UpdatedAt = now
	e.Revision++
	e.Audit = append(e.Audit, AuditEvent{Action: "health_recorded", Detail: string(report.State), At: now})
	return e, nil
}

func (e Extension) PlanUpdate(candidate Manifest, policy Policy, now time.Time) (UpdatePlan, error) {
	if e.State == StateUninstalled {
		return UpdatePlan{}, errors.New("cannot update an uninstalled extension")
	}
	if err := candidate.Validate(policy); err != nil {
		return UpdatePlan{}, err
	}
	if candidate.ID != e.Manifest.ID || candidate.Kind != e.Manifest.Kind {
		return UpdatePlan{}, errors.New("update candidate must keep extension id and kind")
	}
	if candidate.Version == e.Manifest.Version {
		return UpdatePlan{}, errors.New("update candidate must change the exact version pin")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return UpdatePlan{ExtensionID: e.Manifest.ID, FromVersion: e.Manifest.Version, Candidate: canonicalManifest(candidate), CreatedAt: now}, nil
}

// ApplyUpdate records an externally reviewed update. It does not download,
// install, or execute anything. An enabled extension remains disabled after an
// update so any future runtime must be explicitly re-authorized.
func (e Extension) ApplyUpdate(plan UpdatePlan, policy Policy, now time.Time) (Extension, error) {
	if e.State == StateUninstalled {
		return Extension{}, errors.New("cannot apply an update to an uninstalled extension")
	}
	if plan.ExtensionID != e.Manifest.ID || plan.FromVersion != e.Manifest.Version {
		return Extension{}, errors.New("update plan no longer matches installed extension")
	}
	if err := plan.Candidate.Validate(policy); err != nil {
		return Extension{}, err
	}
	if plan.Candidate.ID != e.Manifest.ID || plan.Candidate.Kind != e.Manifest.Kind {
		return Extension{}, errors.New("update candidate must keep extension id and kind")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	e.Manifest = canonicalManifest(plan.Candidate)
	e.State = StateDisabled
	e.Enabled = false
	e.Health = HealthReport{State: HealthUnknown, ObservedAt: now}
	e.UpdatedAt = now
	e.Revision++
	e.Audit = append(e.Audit, AuditEvent{Action: "updated", Detail: "disabled pending explicit re-enable", At: now})
	return e, nil
}

// Uninstall only records intent and clears the enabled state. A future
// installer owns any physical deletion and should report its result separately.
func (e Extension) Uninstall(now time.Time) Extension {
	if e.State == StateUninstalled {
		return e
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return e.transition(StateUninstalled, false, "uninstalled", "metadata retained for audit", now)
}

func (e Extension) transition(state State, enabled bool, action, detail string, now time.Time) Extension {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	e.State = state
	e.Enabled = enabled
	e.UpdatedAt = now
	e.Revision++
	e.Audit = append(e.Audit, AuditEvent{Action: action, Detail: detail, At: now})
	return e
}

// ValidatePublicHTTPSURL is intentionally resolver-free: it rejects direct
// loopback/private IP literals and unsafe URL forms, but a caller that resolves
// DNS must still apply network egress policy before fetching the address.
func ValidatePublicHTTPSURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("invalid URL")
	}
	if u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return errors.New("must be a public HTTPS URL without user info")
	}
	if u.Port() != "" && u.Port() != "443" {
		return errors.New("only the default HTTPS port is allowed")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()) {
		return errors.New("private or loopback address is not allowed")
	}
	return nil
}

func canonicalManifest(m Manifest) Manifest {
	m.Capabilities = append([]string(nil), m.Capabilities...)
	sort.Strings(m.Capabilities)
	m.SecretRefs = append([]SecretRef(nil), m.SecretRefs...)
	sort.Slice(m.SecretRefs, func(i, j int) bool { return m.SecretRefs[i].Name < m.SecretRefs[j].Name })
	return m
}
