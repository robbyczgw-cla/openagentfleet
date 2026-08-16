// Package preferences defines the versioned, local-only preference contract
// shared by OpenAgentFleet's future persistence and UI layers. It deliberately has
// no filesystem, network, or runtime side effects.
package preferences

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"runtime"
	"strings"
)

func defaultComputerRuntime() string {
	if runtime.GOOS == "linux" {
		return RuntimeDocker
	}
	return RuntimeColima
}

const (
	// CurrentVersion is the only on-disk/API preference schema this package
	// understands. Future versions must be migrated explicitly by a caller.
	CurrentVersion = 1

	ThemeLight  = "light"
	ThemeDark   = "dark"
	ThemeSystem = "system"

	DensityComfortable = "comfortable"
	DensityCompact     = "compact"

	ProviderPi             = "pi"
	ProviderClaude         = "claude"
	ProviderCodex          = "codex"
	ProviderGrok           = "grok"
	ProviderOpenCode       = "opencode"
	ProviderCursor         = "cursor"
	ProviderCodexAppServer = "codex_app_server"

	ReasoningLow    = "low"
	ReasoningMedium = "medium"
	ReasoningHigh   = "high"
	ReasoningXHigh  = "xhigh"
	ReasoningMax    = "max"

	PermissionDefault = "default"
	PermissionAuto    = "auto"
	PermissionPlan    = "plan"

	SurfaceDesktop = "desktop"
	SurfaceBrowser = "browser"

	RuntimeAuto           = "auto"
	RuntimeDocker         = "docker"
	RuntimeDockerDesktop  = "docker_desktop"
	RuntimeColima         = "colima"
	RuntimeOrbStack       = "orbstack"
	RuntimeAppleContainer = "apple_container"

	// OS image identifiers are preference-level choices. Runtime support and
	// image provisioning are deliberately handled outside this package.
	OSImageUbuntu2404 = "ubuntu-24.04"
	OSImageUbuntu2604 = "ubuntu-26.04"
	OSImageDebian13   = "debian-13"

	// Computer resource defaults keep the first-run computer useful without
	// reserving more of the host than necessary. SwapGiB may be set to zero to
	// disable swap explicitly; the default is a small 1 GiB safety buffer.
	ComputerDefaultCPUs    = 4
	ComputerDefaultRAMGiB  = 4
	ComputerDefaultDiskGiB = 25
	ComputerDefaultSwapGiB = 1
	ComputerDefaultOSImage = OSImageUbuntu2404

	// Resource bounds prevent an accidental preferences edit from requesting
	// an unbounded VM or an unusably small computer.
	MinComputerCPUs    = 1
	MaxComputerCPUs    = 16
	MinComputerRAMGiB  = 2
	MaxComputerRAMGiB  = 64
	MinComputerDiskGiB = 10
	MaxComputerDiskGiB = 500
	MinComputerSwapGiB = 0
	MaxComputerSwapGiB = 16

	MinFontScale = 0.9
	MaxFontScale = 1.2

	CurrentOnboardingVersion = 1
)

var (
	allowedThemes    = set(ThemeLight, ThemeDark, ThemeSystem)
	allowedDensities = set(DensityComfortable, DensityCompact)
	allowedProviders = set(
		ProviderPi,
		ProviderClaude,
		ProviderCodex,
		ProviderGrok,
		ProviderOpenCode,
		ProviderCursor,
		ProviderCodexAppServer,
	)
	// Only these providers own a workspace lead. The other providers remain
	// valid bounded workers and must not become the lead through a legacy patch.
	allowedLeadProviders = set(
		ProviderGrok,
		ProviderOpenCode,
		ProviderCodexAppServer,
	)
	allowedReasoningEfforts = set(ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningXHigh, ReasoningMax)
	allowedPermissionModes  = set(PermissionDefault, PermissionAuto, PermissionPlan)
	allowedSurfaces         = set(SurfaceDesktop, SurfaceBrowser)
	allowedRuntimes         = set(RuntimeAuto, RuntimeDocker, RuntimeDockerDesktop, RuntimeColima, RuntimeOrbStack, RuntimeAppleContainer)
	allowedOSImages         = set(OSImageUbuntu2404, OSImageUbuntu2604, OSImageDebian13)
)

// Preferences is the complete versioned OpenAgentFleet preferences document.
// Values are deliberately nested so callers can patch coherent groups without
// granting a patch access to arbitrary future fields.
type Preferences struct {
	Version    int               `json:"version"`
	Onboarding OnboardingState   `json:"onboarding"`
	Workspace  WorkspaceDefaults `json:"workspace"`
	Appearance Appearance        `json:"appearance"`
	Usage      UsageDefaults     `json:"usage"`
	Computer   ComputerDefaults  `json:"computer"`
	Safety     SafetyRetention   `json:"safety"`
	Features   FeatureToggles    `json:"features"`
}

// WorkspaceDefaults contains product-wide execution choices. Engine and Model
// are the default lead selection for every agent that does not carry an
// explicit lead override.
type WorkspaceDefaults struct {
	Engine string `json:"engine"`
	// An explicit empty model means provider automatic. Keep it on the wire so
	// selecting Automatic survives a save/reload cycle.
	Model string `json:"model"`
}

// OnboardingState is local UI state only. Completing or skipping onboarding
// grants no capability and enables no optional feature.
type OnboardingState struct {
	Version   int  `json:"version"`
	Completed bool `json:"completed"`
}

// Appearance controls the presentational defaults used by OpenAgentFleet clients.
type Appearance struct {
	Theme     string  `json:"theme"`
	Density   string  `json:"density"`
	FontScale float64 `json:"font_scale"`
}

// UsageDefaults retains reasoning and permission defaults. DefaultWorker is a
// compatibility field for older worker-oriented clients; it is not allowed to
// replace the selected workspace lead.
type UsageDefaults struct {
	DefaultWorker   string `json:"default_worker"`
	ReasoningEffort string `json:"reasoning_effort"`
	PermissionMode  string `json:"permission_mode"`
}

// ComputerDefaults controls the initially visible computer surface. The two
// automatic-control fields exist so unsafe configuration attempts can be
// rejected explicitly; valid OpenAgentFleet preferences always keep them false.
type ComputerDefaults struct {
	DefaultSurface string `json:"default_surface"`
	Runtime        string `json:"runtime"`
	CPUs           int    `json:"cpus"`
	RAMGiB         int    `json:"ram_gib"`
	DiskGiB        int    `json:"disk_gib"`
	SwapGiB        int    `json:"swap_gib"`
	OSImage        string `json:"os_image"`
	// RemoteURL is an optional Advanced setting. Credentials are supplied
	// out-of-band by the controller and are never stored in preferences.
	RemoteURL        string `json:"remote_url"`
	AutoTakeover     bool   `json:"auto_takeover"`
	AutoAgentControl bool   `json:"auto_agent_control"`
}

// SafetyRetention makes retention choices explicit. Both defaults are false
// so a future persistence layer must receive an affirmative user choice before
// retaining transcripts or computer activity.
type SafetyRetention struct {
	RetainTranscripts bool `json:"retain_transcripts"`
	RetainActivity    bool `json:"retain_activity"`
}

// FeatureToggles controls optional product surfaces. Every toggle defaults to
// false so upgrading the app cannot silently grant new authority, start a
// background scheduler, expose a device capability, or let an agent modify its
// own durable behavior.
type FeatureToggles struct {
	LeadWorkerRuntime      bool `json:"lead_worker_runtime"`
	WorkerIsolation        bool `json:"worker_isolation"`
	Routines               bool `json:"routines"`
	Heartbeat              bool `json:"heartbeat"`
	RemoteNodes            bool `json:"remote_nodes"`
	RemoteControl          bool `json:"remote_control"`
	Extensions             bool `json:"extensions"`
	ResearchRuns           bool `json:"research_runs"`
	MemoryProposals        bool `json:"memory_proposals"`
	SkillLearning          bool `json:"skill_learning"`
	NativeMacWorker        bool `json:"native_mac_worker"`
	ExistingBrowserProfile bool `json:"existing_browser_profile"`
	MultipleConversations  bool `json:"multiple_conversations"`
}

// Patch is a restricted JSON merge patch for Preferences. Nil nested values
// mean "leave that group unchanged"; pointer fields preserve an explicit false
// for booleans.
type Patch struct {
	Onboarding *OnboardingPatch `json:"onboarding,omitempty"`
	Workspace  *WorkspacePatch  `json:"workspace,omitempty"`
	Appearance *AppearancePatch `json:"appearance,omitempty"`
	Usage      *UsagePatch      `json:"usage,omitempty"`
	Computer   *ComputerPatch   `json:"computer,omitempty"`
	Safety     *SafetyPatch     `json:"safety,omitempty"`
	Features   *FeaturesPatch   `json:"features,omitempty"`
}

type WorkspacePatch struct {
	Engine *string `json:"engine,omitempty"`
	Model  *string `json:"model,omitempty"`
}

type OnboardingPatch struct {
	Completed *bool `json:"completed,omitempty"`
}

type AppearancePatch struct {
	Theme     *string  `json:"theme,omitempty"`
	Density   *string  `json:"density,omitempty"`
	FontScale *float64 `json:"font_scale,omitempty"`
}

type UsagePatch struct {
	DefaultWorker   *string `json:"default_worker,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	PermissionMode  *string `json:"permission_mode,omitempty"`
}

type ComputerPatch struct {
	DefaultSurface   *string `json:"default_surface,omitempty"`
	Runtime          *string `json:"runtime,omitempty"`
	CPUs             *int    `json:"cpus,omitempty"`
	RAMGiB           *int    `json:"ram_gib,omitempty"`
	DiskGiB          *int    `json:"disk_gib,omitempty"`
	SwapGiB          *int    `json:"swap_gib,omitempty"`
	OSImage          *string `json:"os_image,omitempty"`
	RemoteURL        *string `json:"remote_url,omitempty"`
	AutoTakeover     *bool   `json:"auto_takeover,omitempty"`
	AutoAgentControl *bool   `json:"auto_agent_control,omitempty"`
}

type SafetyPatch struct {
	RetainTranscripts *bool `json:"retain_transcripts,omitempty"`
	RetainActivity    *bool `json:"retain_activity,omitempty"`
}

type FeaturesPatch struct {
	LeadWorkerRuntime      *bool `json:"lead_worker_runtime,omitempty"`
	WorkerIsolation        *bool `json:"worker_isolation,omitempty"`
	Routines               *bool `json:"routines,omitempty"`
	Heartbeat              *bool `json:"heartbeat,omitempty"`
	RemoteNodes            *bool `json:"remote_nodes,omitempty"`
	RemoteControl          *bool `json:"remote_control,omitempty"`
	Extensions             *bool `json:"extensions,omitempty"`
	ResearchRuns           *bool `json:"research_runs,omitempty"`
	MemoryProposals        *bool `json:"memory_proposals,omitempty"`
	SkillLearning          *bool `json:"skill_learning,omitempty"`
	NativeMacWorker        *bool `json:"native_mac_worker,omitempty"`
	ExistingBrowserProfile *bool `json:"existing_browser_profile,omitempty"`
	MultipleConversations  *bool `json:"multiple_conversations,omitempty"`
}

// Defaults returns a valid, privacy-preserving configuration.
func Defaults() Preferences {
	return Preferences{
		Version:    CurrentVersion,
		Onboarding: OnboardingState{Version: CurrentOnboardingVersion},
		Workspace:  WorkspaceDefaults{Engine: ProviderGrok, Model: defaultModelForProvider(ProviderGrok)},
		Appearance: Appearance{
			Theme:     ThemeSystem,
			Density:   DensityComfortable,
			FontScale: 1,
		},
		Usage: UsageDefaults{
			DefaultWorker:   ProviderGrok,
			ReasoningEffort: ReasoningHigh,
			PermissionMode:  PermissionDefault,
		},
		Computer: ComputerDefaults{
			DefaultSurface:   SurfaceDesktop,
			Runtime:          defaultComputerRuntime(),
			CPUs:             ComputerDefaultCPUs,
			RAMGiB:           ComputerDefaultRAMGiB,
			DiskGiB:          ComputerDefaultDiskGiB,
			SwapGiB:          ComputerDefaultSwapGiB,
			OSImage:          ComputerDefaultOSImage,
			AutoTakeover:     false,
			AutoAgentControl: false,
		},
		Safety: SafetyRetention{
			RetainTranscripts: false,
			RetainActivity:    false,
		},
		Features: FeatureToggles{},
	}
}

// Normalize returns a canonical, safe Preferences value. It maps recognized
// enum spellings to their canonical lowercase form, clamps a finite font scale,
// falls back to Defaults for unsupported scalar values, and always disables
// automatic takeover and agent control. Use Validate when invalid input must be
// reported instead of repaired.
func (p Preferences) Normalize() Preferences {
	normalized := Defaults()
	if p.Version == CurrentVersion {
		normalized.Version = CurrentVersion
	}
	normalized.Onboarding.Completed = p.Onboarding.Completed
	workspaceEngine := p.Workspace.Engine
	if strings.TrimSpace(workspaceEngine) == "" {
		// Version 1 documents predate workspace.engine. Keep a legacy lead only
		// when the old value is one of the supported lead providers.
		workspaceEngine = p.Usage.DefaultWorker
	}
	if value, ok := canonicalAllowed(workspaceEngine, allowedLeadProviders); ok {
		normalized.Workspace.Engine = value
	}
	normalized.Workspace.Model = normalizeModel(p.Workspace.Model)

	if value, ok := canonicalAllowed(p.Appearance.Theme, allowedThemes); ok {
		normalized.Appearance.Theme = value
	}
	if value, ok := canonicalAllowed(p.Appearance.Density, allowedDensities); ok {
		normalized.Appearance.Density = value
	}
	if scale := p.Appearance.FontScale; !math.IsNaN(scale) && !math.IsInf(scale, 0) && scale != 0 {
		normalized.Appearance.FontScale = clamp(scale, MinFontScale, MaxFontScale)
	}

	if value, ok := canonicalAllowed(p.Usage.DefaultWorker, allowedProviders); ok {
		normalized.Usage.DefaultWorker = value
	}
	if value, ok := canonicalAllowed(p.Usage.ReasoningEffort, allowedReasoningEfforts); ok {
		normalized.Usage.ReasoningEffort = value
	}
	if value, ok := canonicalAllowed(p.Usage.PermissionMode, allowedPermissionModes); ok {
		normalized.Usage.PermissionMode = value
	}

	if value, ok := canonicalAllowed(p.Computer.DefaultSurface, allowedSurfaces); ok {
		normalized.Computer.DefaultSurface = value
	}
	runtimeSelection := p.Computer.Runtime
	if runtimeSelection == "" {
		runtimeSelection = defaultComputerRuntime()
	}
	if value, ok := canonicalAllowed(runtimeSelection, allowedRuntimes); ok {
		normalized.Computer.Runtime = value
	}
	normalized.Computer.CPUs = normalizeComputerResource(p.Computer.CPUs, MinComputerCPUs, MaxComputerCPUs, ComputerDefaultCPUs)
	normalized.Computer.RAMGiB = normalizeComputerResource(p.Computer.RAMGiB, MinComputerRAMGiB, MaxComputerRAMGiB, ComputerDefaultRAMGiB)
	normalized.Computer.DiskGiB = normalizeComputerResource(p.Computer.DiskGiB, MinComputerDiskGiB, MaxComputerDiskGiB, ComputerDefaultDiskGiB)
	normalized.Computer.SwapGiB = normalizeComputerResource(p.Computer.SwapGiB, MinComputerSwapGiB, MaxComputerSwapGiB, ComputerDefaultSwapGiB)
	if value, ok := canonicalAllowed(p.Computer.OSImage, allowedOSImages); ok {
		normalized.Computer.OSImage = value
	}
	if remoteURL, ok := normalizeRemoteURL(p.Computer.RemoteURL); ok {
		normalized.Computer.RemoteURL = remoteURL
	}
	// Deliberately do not copy AutoTakeover or AutoAgentControl. A malformed
	// document must never become a way to seize the computer automatically.

	normalized.Safety.RetainTranscripts = p.Safety.RetainTranscripts
	normalized.Safety.RetainActivity = p.Safety.RetainActivity
	normalized.Features = p.Features
	if !normalized.Features.Routines {
		normalized.Features.Heartbeat = false
	}
	if !normalized.Features.RemoteNodes {
		normalized.Features.RemoteControl = false
	}
	return normalized
}

// Normalize is the functional form of Preferences.Normalize.
func Normalize(p Preferences) Preferences {
	return p.Normalize()
}

// Validate checks whether Preferences contains only supported, safe values.
// It accepts harmless differences in casing and surrounding whitespace because
// Normalize canonicalizes those values before they are encoded or used.
func (p Preferences) Validate() error {
	if p.Version != CurrentVersion {
		return fmt.Errorf("unsupported preferences version %d", p.Version)
	}
	if p.Onboarding.Version != 0 && p.Onboarding.Version != CurrentOnboardingVersion {
		return fmt.Errorf("unsupported onboarding version %d", p.Onboarding.Version)
	}
	workspaceEngine := ""
	if strings.TrimSpace(p.Workspace.Engine) != "" {
		var ok bool
		workspaceEngine, ok = canonicalAllowed(p.Workspace.Engine, allowedLeadProviders)
		if !ok {
			return fmt.Errorf("invalid workspace.engine %q", p.Workspace.Engine)
		}
	}
	if err := validateModel(p.Workspace.Model); err != nil {
		return fmt.Errorf("invalid workspace.model: %w", err)
	}
	if _, ok := canonicalAllowed(p.Appearance.Theme, allowedThemes); !ok {
		return fmt.Errorf("invalid appearance.theme %q", p.Appearance.Theme)
	}
	if _, ok := canonicalAllowed(p.Appearance.Density, allowedDensities); !ok {
		return fmt.Errorf("invalid appearance.density %q", p.Appearance.Density)
	}
	if math.IsNaN(p.Appearance.FontScale) || math.IsInf(p.Appearance.FontScale, 0) || p.Appearance.FontScale < MinFontScale || p.Appearance.FontScale > MaxFontScale {
		return fmt.Errorf("appearance.font_scale must be between %.1f and %.1f", MinFontScale, MaxFontScale)
	}
	if _, ok := canonicalAllowed(p.Usage.DefaultWorker, allowedProviders); !ok {
		return fmt.Errorf("invalid usage.default_worker %q", p.Usage.DefaultWorker)
	}
	if _, ok := canonicalAllowed(p.Usage.ReasoningEffort, allowedReasoningEfforts); !ok {
		return fmt.Errorf("invalid usage.reasoning_effort %q", p.Usage.ReasoningEffort)
	}
	if _, ok := canonicalAllowed(p.Usage.PermissionMode, allowedPermissionModes); !ok {
		return fmt.Errorf("invalid usage.permission_mode %q", p.Usage.PermissionMode)
	}
	if workspaceEngine == ProviderOpenCode && p.Usage.PermissionMode != PermissionDefault {
		return fmt.Errorf("OpenCode workspace requires usage.permission_mode %q", PermissionDefault)
	}
	if _, ok := canonicalAllowed(p.Computer.DefaultSurface, allowedSurfaces); !ok {
		return fmt.Errorf("invalid computer.default_surface %q", p.Computer.DefaultSurface)
	}
	runtimeSelection := p.Computer.Runtime
	if runtimeSelection == "" {
		runtimeSelection = defaultComputerRuntime()
	}
	if _, ok := canonicalAllowed(runtimeSelection, allowedRuntimes); !ok {
		return fmt.Errorf("invalid computer.runtime %q", p.Computer.Runtime)
	}
	if err := validateComputerResource("computer.cpus", p.Computer.CPUs, MinComputerCPUs, MaxComputerCPUs); err != nil {
		return err
	}
	if err := validateComputerResource("computer.ram_gib", p.Computer.RAMGiB, MinComputerRAMGiB, MaxComputerRAMGiB); err != nil {
		return err
	}
	if err := validateComputerResource("computer.disk_gib", p.Computer.DiskGiB, MinComputerDiskGiB, MaxComputerDiskGiB); err != nil {
		return err
	}
	if err := validateComputerResource("computer.swap_gib", p.Computer.SwapGiB, MinComputerSwapGiB, MaxComputerSwapGiB); err != nil {
		return err
	}
	if _, ok := canonicalAllowed(p.Computer.OSImage, allowedOSImages); !ok {
		return fmt.Errorf("invalid computer.os_image %q", p.Computer.OSImage)
	}
	if _, ok := normalizeRemoteURL(p.Computer.RemoteURL); !ok {
		return errors.New("computer.remote_url must be an http(s) URL without credentials, query, or fragment")
	}
	if p.Computer.AutoTakeover {
		return errors.New("computer.auto_takeover must remain false")
	}
	if p.Computer.AutoAgentControl {
		return errors.New("computer.auto_agent_control must remain false")
	}
	if p.Features.Heartbeat && !p.Features.Routines {
		return errors.New("features.heartbeat requires features.routines")
	}
	if p.Features.RemoteControl && !p.Features.RemoteNodes {
		return errors.New("features.remote_control requires features.remote_nodes")
	}
	return nil
}

// Encode validates and writes canonical JSON. It never serializes an invalid
// configuration, including one that requests automatic control.
func Encode(p Preferences) ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(p.Normalize())
}

// Encode is also available as a method for callers that already hold a
// Preferences value.
func (p Preferences) Encode() ([]byte, error) {
	return Encode(p)
}

// Decode reads only the current schema. Unknown fields, malformed JSON,
// unsupported versions, and invalid or unsafe values return Defaults together
// with an error, so a caller that accidentally ignores the error still receives
// a fail-closed value. An empty document represents first-run configuration and
// returns Defaults without error.
func Decode(data []byte) (Preferences, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return Defaults(), nil
	}
	if bytes.Equal(data, []byte("null")) {
		return Defaults(), errors.New("preferences document must be an object")
	}

	var result Preferences
	if err := decodeStrict(data, &result); err != nil {
		return Defaults(), fmt.Errorf("decode preferences: %w", err)
	}
	if err := hydrateMissingComputerResources(data, &result); err != nil {
		return Defaults(), fmt.Errorf("decode preferences computer resources: %w", err)
	}
	if err := result.Validate(); err != nil {
		return Defaults(), err
	}
	normalized := result.Normalize()
	// Missing model fields are old documents and receive the provider's
	// recommended default. A present empty field is an intentional Automatic
	// selection and must remain empty.
	var raw struct {
		Workspace *struct {
			Model *string `json:"model"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Defaults(), fmt.Errorf("decode preferences model state: %w", err)
	}
	if raw.Workspace == nil || raw.Workspace.Model == nil {
		normalized.Workspace.Model = defaultModelForProvider(normalized.Workspace.Engine)
	}
	return normalized, nil
}

// DecodePatch reads a restricted merge patch. Fields outside the documented
// Preferences groups, including version, are rejected rather than ignored.
func DecodePatch(data []byte) (Patch, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return Patch{}, errors.New("preferences patch must be an object")
	}

	var raw map[string]json.RawMessage
	if err := decodeStrict(data, &raw); err != nil {
		return Patch{}, fmt.Errorf("decode preferences patch: %w", err)
	}
	if raw == nil {
		return Patch{}, errors.New("preferences patch must be an object")
	}
	for field, value := range raw {
		switch field {
		case "onboarding", "workspace", "appearance", "usage", "computer", "safety", "features":
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return Patch{}, fmt.Errorf("preferences patch field %q must be an object", field)
			}
		default:
			return Patch{}, fmt.Errorf("unsupported preferences patch field %q", field)
		}
	}

	var patch Patch
	if err := decodeStrict(data, &patch); err != nil {
		return Patch{}, fmt.Errorf("decode preferences patch: %w", err)
	}
	return patch, nil
}

// MergePatch decodes and applies a restricted JSON merge patch. A rejected
// patch leaves the supplied valid base intact (in canonical form).
func MergePatch(base Preferences, data []byte) (Preferences, error) {
	patch, err := DecodePatch(data)
	if err != nil {
		if baseErr := base.Validate(); baseErr != nil {
			return Defaults(), fmt.Errorf("invalid preferences base: %w", baseErr)
		}
		return base.Normalize(), err
	}
	return base.ApplyPatch(patch)
}

// MergePatch is also available as a method for callers that hold the current
// Preferences value.
func (p Preferences) MergePatch(data []byte) (Preferences, error) {
	return MergePatch(p, data)
}

// ApplyPatch applies the supported Patch fields and validates the complete
// result. A rejected patch returns the normalized valid base unchanged.
func (p Preferences) ApplyPatch(patch Patch) (Preferences, error) {
	if err := p.Validate(); err != nil {
		return Defaults(), fmt.Errorf("invalid preferences base: %w", err)
	}
	result := p.Normalize()
	if patch.Onboarding != nil && patch.Onboarding.Completed != nil {
		result.Onboarding.Completed = *patch.Onboarding.Completed
	}
	if patch.Workspace != nil && patch.Workspace.Engine != nil && patch.Usage != nil && patch.Usage.DefaultWorker != nil {
		workspaceEngine, workspaceOK := canonicalAllowed(*patch.Workspace.Engine, allowedLeadProviders)
		legacyEngine, legacyOK := canonicalAllowed(*patch.Usage.DefaultWorker, allowedProviders)
		if workspaceOK && legacyOK && workspaceEngine != legacyEngine {
			return p.Normalize(), errors.New("workspace.engine conflicts with usage.default_worker")
		}
	}

	if patch.Appearance != nil {
		if patch.Appearance.Theme != nil {
			result.Appearance.Theme = *patch.Appearance.Theme
		}
		if patch.Appearance.Density != nil {
			result.Appearance.Density = *patch.Appearance.Density
		}
		if patch.Appearance.FontScale != nil {
			result.Appearance.FontScale = *patch.Appearance.FontScale
		}
	}
	if patch.Usage != nil {
		if patch.Usage.DefaultWorker != nil {
			result.Usage.DefaultWorker = *patch.Usage.DefaultWorker
			// Preserve old clients for lead providers, but never let a
			// worker-only provider replace the workspace lead.
			if legacyEngine, ok := canonicalAllowed(*patch.Usage.DefaultWorker, allowedLeadProviders); ok {
				result.Workspace.Engine = legacyEngine
				if patch.Workspace == nil || patch.Workspace.Model == nil {
					result.Workspace.Model = defaultModelForProvider(result.Workspace.Engine)
				}
			}
		}
		if patch.Usage.ReasoningEffort != nil {
			result.Usage.ReasoningEffort = *patch.Usage.ReasoningEffort
		}
		if patch.Usage.PermissionMode != nil {
			result.Usage.PermissionMode = *patch.Usage.PermissionMode
		}
	}
	if patch.Workspace != nil && patch.Workspace.Engine != nil {
		result.Workspace.Engine = *patch.Workspace.Engine
		// Keep the legacy response field useful to older clients when a lead is
		// selected explicitly.
		result.Usage.DefaultWorker = *patch.Workspace.Engine
		if patch.Workspace.Model == nil {
			result.Workspace.Model = defaultModelForProvider(result.Workspace.Engine)
		}
	}
	if patch.Workspace != nil && patch.Workspace.Model != nil {
		result.Workspace.Model = *patch.Workspace.Model
	}
	if patch.Computer != nil {
		if patch.Computer.DefaultSurface != nil {
			result.Computer.DefaultSurface = *patch.Computer.DefaultSurface
		}
		if patch.Computer.Runtime != nil {
			result.Computer.Runtime = *patch.Computer.Runtime
		}
		if patch.Computer.CPUs != nil {
			result.Computer.CPUs = *patch.Computer.CPUs
		}
		if patch.Computer.RAMGiB != nil {
			result.Computer.RAMGiB = *patch.Computer.RAMGiB
		}
		if patch.Computer.DiskGiB != nil {
			result.Computer.DiskGiB = *patch.Computer.DiskGiB
		}
		if patch.Computer.SwapGiB != nil {
			result.Computer.SwapGiB = *patch.Computer.SwapGiB
		}
		if patch.Computer.OSImage != nil {
			result.Computer.OSImage = *patch.Computer.OSImage
		}
		if patch.Computer.RemoteURL != nil {
			result.Computer.RemoteURL = *patch.Computer.RemoteURL
		}
		if patch.Computer.AutoTakeover != nil {
			result.Computer.AutoTakeover = *patch.Computer.AutoTakeover
		}
		if patch.Computer.AutoAgentControl != nil {
			result.Computer.AutoAgentControl = *patch.Computer.AutoAgentControl
		}
	}
	if patch.Safety != nil {
		if patch.Safety.RetainTranscripts != nil {
			result.Safety.RetainTranscripts = *patch.Safety.RetainTranscripts
		}
		if patch.Safety.RetainActivity != nil {
			result.Safety.RetainActivity = *patch.Safety.RetainActivity
		}
	}
	if patch.Features != nil {
		applyBool := func(target *bool, value *bool) {
			if value != nil {
				*target = *value
			}
		}
		applyBool(&result.Features.LeadWorkerRuntime, patch.Features.LeadWorkerRuntime)
		applyBool(&result.Features.WorkerIsolation, patch.Features.WorkerIsolation)
		applyBool(&result.Features.Routines, patch.Features.Routines)
		applyBool(&result.Features.Heartbeat, patch.Features.Heartbeat)
		applyBool(&result.Features.RemoteNodes, patch.Features.RemoteNodes)
		applyBool(&result.Features.RemoteControl, patch.Features.RemoteControl)
		applyBool(&result.Features.Extensions, patch.Features.Extensions)
		applyBool(&result.Features.ResearchRuns, patch.Features.ResearchRuns)
		applyBool(&result.Features.MemoryProposals, patch.Features.MemoryProposals)
		applyBool(&result.Features.SkillLearning, patch.Features.SkillLearning)
		applyBool(&result.Features.NativeMacWorker, patch.Features.NativeMacWorker)
		applyBool(&result.Features.ExistingBrowserProfile, patch.Features.ExistingBrowserProfile)
		applyBool(&result.Features.MultipleConversations, patch.Features.MultipleConversations)
	}

	if err := result.Validate(); err != nil {
		return p.Normalize(), err
	}
	return result.Normalize(), nil
}

func hydrateMissingComputerResources(data []byte, result *Preferences) error {
	var raw struct {
		Computer *struct {
			CPUs    *int    `json:"cpus"`
			RAMGiB  *int    `json:"ram_gib"`
			DiskGiB *int    `json:"disk_gib"`
			SwapGiB *int    `json:"swap_gib"`
			OSImage *string `json:"os_image"`
		} `json:"computer"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	if raw.Computer == nil || raw.Computer.CPUs == nil {
		result.Computer.CPUs = ComputerDefaultCPUs
	}
	if raw.Computer == nil || raw.Computer.RAMGiB == nil {
		result.Computer.RAMGiB = ComputerDefaultRAMGiB
	}
	if raw.Computer == nil || raw.Computer.DiskGiB == nil {
		result.Computer.DiskGiB = ComputerDefaultDiskGiB
	}
	if raw.Computer == nil || raw.Computer.SwapGiB == nil {
		result.Computer.SwapGiB = ComputerDefaultSwapGiB
	}
	if raw.Computer == nil || raw.Computer.OSImage == nil {
		result.Computer.OSImage = ComputerDefaultOSImage
	}
	return nil
}

func normalizeRemoteURL(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", true
	}
	parsed, err := url.ParseRequestURI(trimmed)
	if err != nil || parsed.User != nil || parsed.Host == "" {
		return "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return "", false
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	return strings.TrimRight(parsed.String(), "/"), true
}

func defaultModelForProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderGrok, "grok_build":
		return "grok-4.6"
	case ProviderOpenCode:
		return "opencode/deepseek-v4-flash-free"
	default:
		// Codex App Server owns its default model selection. An empty model is
		// intentional and means "use the connected Codex account default".
		return ""
	}
}

func normalizeModel(value string) string {
	return strings.TrimSpace(value)
}

func validateModel(value string) error {
	value = normalizeModel(value)
	if value == "" {
		return nil
	}
	if len(value) > 200 {
		return errors.New("model is too long")
	}
	if strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return errors.New("model contains control characters")
	}
	return nil
}

func validateComputerResource(name string, value, minimum, maximum int) error {
	if value < minimum || value > maximum {
		return fmt.Errorf("%s must be between %d and %d", name, minimum, maximum)
	}
	return nil
}

func normalizeComputerResource(value, minimum, maximum, fallback int) int {
	if value < minimum || value > maximum {
		return fallback
	}
	return value
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	parsed := net.ParseIP(strings.Trim(host, "[]"))
	return parsed != nil && parsed.IsLoopback()
}

func decodeStrict(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected additional JSON value")
		}
		return err
	}
	return nil
}

func set(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func canonicalAllowed(value string, allowed map[string]struct{}) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	_, ok := allowed[value]
	return value, ok
}

func clamp(value, minimum, maximum float64) float64 {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}
