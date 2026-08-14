package preferences

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeCanonicalizesAndFailsClosedForComputerControl(t *testing.T) {
	input := Defaults()
	input.Version = 99
	input.Appearance.Theme = " DARK "
	input.Appearance.Density = "compact"
	input.Appearance.FontScale = 4
	input.Usage.DefaultWorker = " CURSOR "
	input.Usage.ReasoningEffort = " Medium "
	input.Usage.PermissionMode = " PLAN "
	input.Computer.DefaultSurface = " BROWSER "
	input.Computer.Runtime = " COLIMA "
	input.Computer.AutoTakeover = true
	input.Computer.AutoAgentControl = true
	input.Safety.RetainTranscripts = true
	input.Safety.RetainActivity = true

	got := input.Normalize()
	if got.Version != CurrentVersion {
		t.Fatalf("version = %d, want %d", got.Version, CurrentVersion)
	}
	if got.Appearance.Theme != ThemeDark || got.Appearance.Density != DensityCompact || got.Appearance.FontScale != MaxFontScale {
		t.Fatalf("appearance was not normalized: %#v", got.Appearance)
	}
	if got.Usage.DefaultWorker != ProviderCursor || got.Usage.ReasoningEffort != ReasoningMedium || got.Usage.PermissionMode != PermissionPlan {
		t.Fatalf("usage was not normalized: %#v", got.Usage)
	}
	if got.Computer.DefaultSurface != SurfaceBrowser {
		t.Fatalf("surface = %q, want %q", got.Computer.DefaultSurface, SurfaceBrowser)
	}
	if got.Computer.Runtime != RuntimeColima {
		t.Fatalf("runtime = %q, want %q", got.Computer.Runtime, RuntimeColima)
	}
	if got.Computer.AutoTakeover || got.Computer.AutoAgentControl {
		t.Fatalf("normalize must never enable computer control: %#v", got.Computer)
	}
	if !got.Safety.RetainTranscripts || !got.Safety.RetainActivity {
		t.Fatalf("explicit retention choices were lost: %#v", got.Safety)
	}
}

func TestDefaultsUseColimaWithoutEnablingComputerControl(t *testing.T) {
	defaults := Defaults()
	if defaults.Computer.Runtime != RuntimeColima {
		t.Fatalf("default runtime = %q, want %q", defaults.Computer.Runtime, RuntimeColima)
	}
	if defaults.Computer.AutoTakeover || defaults.Computer.AutoAgentControl {
		t.Fatalf("Colima default must not grant computer control: %#v", defaults.Computer)
	}
	if defaults.Computer.CPUs != ComputerDefaultCPUs || defaults.Computer.RAMGiB != ComputerDefaultRAMGiB || defaults.Computer.DiskGiB != ComputerDefaultDiskGiB || defaults.Computer.SwapGiB != ComputerDefaultSwapGiB || defaults.Computer.OSImage != OSImageUbuntu2404 {
		t.Fatalf("computer resource defaults = %#v", defaults.Computer)
	}
}

func TestComputerResourcesCanBePatchedIncludingDisabledSwap(t *testing.T) {
	updated, err := MergePatch(Defaults(), []byte(`{"computer":{"cpus":8,"ram_gib":12,"disk_gib":50,"swap_gib":0,"os_image":"debian-13"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Computer.CPUs != 8 || updated.Computer.RAMGiB != 12 || updated.Computer.DiskGiB != 50 || updated.Computer.SwapGiB != 0 || updated.Computer.OSImage != OSImageDebian13 {
		t.Fatalf("computer resources = %#v", updated.Computer)
	}
}

func TestComputerResourcesValidateBoundsAndImageChoice(t *testing.T) {
	tests := []struct {
		name string
		edit func(*ComputerDefaults)
		want string
	}{
		{name: "cpus", edit: func(value *ComputerDefaults) { value.CPUs = 0 }, want: "computer.cpus"},
		{name: "ram", edit: func(value *ComputerDefaults) { value.RAMGiB = 1 }, want: "computer.ram_gib"},
		{name: "disk", edit: func(value *ComputerDefaults) { value.DiskGiB = 9 }, want: "computer.disk_gib"},
		{name: "swap", edit: func(value *ComputerDefaults) { value.SwapGiB = 17 }, want: "computer.swap_gib"},
		{name: "image", edit: func(value *ComputerDefaults) { value.OSImage = "debian-12" }, want: "computer.os_image"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := Defaults()
			test.edit(&value.Computer)
			if err := value.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
}

func TestLegacyPreferencesReceiveComputerResourceDefaults(t *testing.T) {
	legacy := []byte(`{"version":1,"appearance":{"theme":"system","density":"comfortable","font_scale":1},"usage":{"default_worker":"grok","reasoning_effort":"high","permission_mode":"default"},"computer":{"default_surface":"desktop","runtime":"colima","auto_takeover":false,"auto_agent_control":false},"safety":{"retain_transcripts":false,"retain_activity":false},"features":{}}`)
	got, err := Decode(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if got.Computer.CPUs != ComputerDefaultCPUs || got.Computer.RAMGiB != ComputerDefaultRAMGiB || got.Computer.DiskGiB != ComputerDefaultDiskGiB || got.Computer.SwapGiB != ComputerDefaultSwapGiB || got.Computer.OSImage != ComputerDefaultOSImage {
		t.Fatalf("legacy computer resources = %#v", got.Computer)
	}
}

func TestRemoteComputerURLIsOptionalAndCredentialsAreRejected(t *testing.T) {
	base := Defaults()
	updated, err := MergePatch(base, []byte(`{"computer":{"remote_url":" https://worker.tailnet.example/ "}}`))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Computer.RemoteURL != "https://worker.tailnet.example" {
		t.Fatalf("remote URL = %q", updated.Computer.RemoteURL)
	}
	if _, err := MergePatch(base, []byte(`{"computer":{"remote_url":"https://user:secret@worker.tailnet.example"}}`)); err == nil {
		t.Fatal("credential-bearing remote URL was accepted")
	}
	if _, err := MergePatch(base, []byte(`{"computer":{"remote_url":"https://worker.tailnet.example/?token=secret"}}`)); err == nil {
		t.Fatal("query-bearing remote URL was accepted")
	}
	if _, err := MergePatch(base, []byte(`{"computer":{"remote_url":"http://worker.tailnet.example"}}`)); err == nil {
		t.Fatal("non-loopback HTTP remote URL was accepted")
	}
	if updated, err := MergePatch(base, []byte(`{"computer":{"remote_url":"http://127.0.0.1:9323"}}`)); err != nil || updated.Computer.RemoteURL != "http://127.0.0.1:9323" {
		t.Fatalf("loopback HTTP test URL was rejected: %#v, %v", updated.Computer, err)
	}
}

func TestWorkspaceEngineMigratesFromLegacyDefaultWorker(t *testing.T) {
	legacy := []byte(`{"version":1,"onboarding":{"version":1,"completed":false},"appearance":{"theme":"system","density":"comfortable","font_scale":1},"usage":{"default_worker":"claude","reasoning_effort":"high","permission_mode":"default"},"computer":{"default_surface":"desktop","runtime":"colima","auto_takeover":false,"auto_agent_control":false},"safety":{"retain_transcripts":false,"retain_activity":false},"features":{}}`)

	got, err := Decode(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if got.Workspace.Engine != ProviderGrok {
		t.Fatalf("migrated workspace engine = %q, want safe lead fallback %q", got.Workspace.Engine, ProviderGrok)
	}
	if got.Usage.DefaultWorker != ProviderClaude {
		t.Fatalf("legacy default worker changed = %q", got.Usage.DefaultWorker)
	}
}

func TestWorkspaceEngineAndLegacyAliasPatchTogether(t *testing.T) {
	fromWorkspace, err := MergePatch(Defaults(), []byte(`{"workspace":{"engine":"codex_app_server"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if fromWorkspace.Workspace.Engine != ProviderCodexAppServer || fromWorkspace.Usage.DefaultWorker != ProviderCodexAppServer {
		t.Fatalf("workspace patch aliases = %#v", fromWorkspace)
	}

	fromLegacy, err := MergePatch(fromWorkspace, []byte(`{"usage":{"default_worker":"opencode"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if fromLegacy.Workspace.Engine != ProviderOpenCode || fromLegacy.Usage.DefaultWorker != ProviderOpenCode {
		t.Fatalf("legacy patch aliases = %#v", fromLegacy)
	}

	unchanged, err := MergePatch(fromLegacy, []byte(`{"workspace":{"engine":"grok"},"usage":{"default_worker":"claude"}}`))
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting aliases error = %v", err)
	}
	if !reflect.DeepEqual(unchanged, fromLegacy) {
		t.Fatalf("conflicting aliases changed preferences: got %#v want %#v", unchanged, fromLegacy)
	}
}

func TestWorkspaceModelFollowsLeadProviderAndAcceptsExplicitPickerValue(t *testing.T) {
	defaults := Defaults()
	if defaults.Workspace.Model != "grok-4.6" {
		t.Fatalf("default workspace model = %q, want grok-4.6", defaults.Workspace.Model)
	}

	codex, err := MergePatch(defaults, []byte(`{"workspace":{"engine":"codex_app_server"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if codex.Workspace.Model != "" {
		t.Fatalf("Codex default model = %q, want provider automatic", codex.Workspace.Model)
	}

	openCode, err := MergePatch(defaults, []byte(`{"workspace":{"engine":"opencode"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if openCode.Workspace.Model != "opencode/deepseek-v4-flash-free" {
		t.Fatalf("OpenCode default model = %q", openCode.Workspace.Model)
	}

	custom, err := MergePatch(defaults, []byte(`{"workspace":{"model":"grok-4.6-custom"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if custom.Workspace.Model != "grok-4.6-custom" {
		t.Fatalf("custom workspace model = %q", custom.Workspace.Model)
	}
	if _, err := MergePatch(defaults, []byte(`{"workspace":{"model":"bad\u0000model"}}`)); err == nil {
		t.Fatal("control-character model was accepted")
	}
}

func TestAutomaticWorkspaceModelSurvivesPersistence(t *testing.T) {
	automatic, err := MergePatch(Defaults(), []byte(`{"workspace":{"engine":"grok","model":""}}`))
	if err != nil {
		t.Fatal(err)
	}
	if automatic.Workspace.Model != "" {
		t.Fatalf("automatic model was normalized to %q", automatic.Workspace.Model)
	}
	encoded, err := Encode(automatic)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"model":""`) {
		t.Fatalf("automatic model was omitted from encoded preferences: %s", encoded)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Workspace.Model != "" {
		t.Fatalf("automatic model did not survive decode: %q", decoded.Workspace.Model)
	}
}

func TestWorkerOnlyProviderCannotBecomeWorkspaceLead(t *testing.T) {
	defaults := Defaults()
	updated, err := MergePatch(defaults, []byte(`{"usage":{"default_worker":"claude"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Workspace.Engine != ProviderGrok {
		t.Fatalf("worker-only patch changed workspace lead to %q", updated.Workspace.Engine)
	}
	if updated.Usage.DefaultWorker != ProviderClaude {
		t.Fatalf("worker selection was lost: %q", updated.Usage.DefaultWorker)
	}
	if _, err := MergePatch(defaults, []byte(`{"workspace":{"engine":"claude"}}`)); err == nil {
		t.Fatal("worker-only workspace lead was accepted")
	}
}

func TestValidateRejectsInvalidProviderAndAutomaticControl(t *testing.T) {
	invalidProvider := Defaults()
	invalidProvider.Usage.DefaultWorker = "unknown-worker"
	if err := invalidProvider.Validate(); err == nil || !strings.Contains(err.Error(), "default_worker") {
		t.Fatalf("invalid provider error = %v", err)
	}

	autoTakeover := Defaults()
	autoTakeover.Computer.AutoTakeover = true
	if err := autoTakeover.Validate(); err == nil || !strings.Contains(err.Error(), "auto_takeover") {
		t.Fatalf("auto takeover error = %v", err)
	}

	autoAgentControl := Defaults()
	autoAgentControl.Computer.AutoAgentControl = true
	if err := autoAgentControl.Validate(); err == nil || !strings.Contains(err.Error(), "auto_agent_control") {
		t.Fatalf("auto agent control error = %v", err)
	}
}

func TestMergePatchAcceptsOnlySupportedSafeFields(t *testing.T) {
	base := Defaults()
	updated, err := MergePatch(base, []byte(`{
		"onboarding": {"completed": true},
		"appearance": {"theme": "light", "font_scale": 1.1},
		"usage": {"default_worker": "claude"},
		"computer": {"default_surface": "browser", "runtime": "colima", "auto_takeover": false},
		"safety": {"retain_activity": true},
		"features": {"research_runs": true, "skill_learning": true}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Appearance.Theme != ThemeLight || updated.Appearance.FontScale != 1.1 || updated.Usage.DefaultWorker != ProviderClaude || updated.Computer.DefaultSurface != SurfaceBrowser || !updated.Safety.RetainActivity {
		t.Fatalf("patch was not applied: %#v", updated)
	}
	if updated.Computer.Runtime != RuntimeColima {
		t.Fatalf("runtime patch was not applied: %#v", updated.Computer)
	}
	if !updated.Onboarding.Completed || updated.Onboarding.Version != CurrentOnboardingVersion {
		t.Fatalf("onboarding patch was not persisted safely: %#v", updated.Onboarding)
	}
	if !updated.Features.ResearchRuns || !updated.Features.SkillLearning || updated.Features.NativeMacWorker {
		t.Fatalf("feature patch was not applied safely: %#v", updated.Features)
	}

	rejected, err := MergePatch(updated, []byte(`{"computer":{"auto_agent_control":true}}`))
	if err == nil || !strings.Contains(err.Error(), "auto_agent_control") {
		t.Fatalf("unsafe patch error = %v", err)
	}
	if !reflect.DeepEqual(rejected, updated) {
		t.Fatalf("unsafe patch changed preferences: got %#v want %#v", rejected, updated)
	}

	_, err = MergePatch(updated, []byte(`{"usage":{"surprise":"value"}}`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown nested field error = %v", err)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	want := Defaults()
	want.Appearance = Appearance{Theme: ThemeDark, Density: DensityCompact, FontScale: 1.15}
	want.Usage = UsageDefaults{DefaultWorker: ProviderCodexAppServer, ReasoningEffort: ReasoningLow, PermissionMode: PermissionPlan}
	want.Computer = ComputerDefaults{DefaultSurface: SurfaceBrowser, Runtime: RuntimeAuto, CPUs: 6, RAMGiB: 8, DiskGiB: 50, SwapGiB: 2, OSImage: OSImageDebian13}
	want.Safety = SafetyRetention{RetainTranscripts: true, RetainActivity: true}
	want.Features = FeatureToggles{LeadWorkerRuntime: true, WorkerIsolation: true, ResearchRuns: true}

	encoded, err := Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestOptionalFeaturesDefaultOffAndPatchIndependently(t *testing.T) {
	defaults := Defaults()
	if !reflect.DeepEqual(defaults.Features, FeatureToggles{}) {
		t.Fatalf("optional feature defaults are not off: %#v", defaults.Features)
	}
	updated, err := MergePatch(defaults, []byte(`{"features":{"routines":true,"heartbeat":false,"remote_nodes":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Features.Routines || updated.Features.Heartbeat || !updated.Features.RemoteNodes {
		t.Fatalf("independent feature toggles = %#v", updated.Features)
	}
	if updated.Features.RemoteControl || updated.Features.NativeMacWorker || updated.Features.ExistingBrowserProfile || updated.Features.MultipleConversations {
		t.Fatalf("unrequested authority was enabled: %#v", updated.Features)
	}
}

func TestCodexReasoningEffortsPersistBeyondHigh(t *testing.T) {
	for _, effort := range []string{ReasoningXHigh, ReasoningMax} {
		t.Run(effort, func(t *testing.T) {
			updated, err := MergePatch(Defaults(), []byte(`{"usage":{"reasoning_effort":"`+effort+`"}}`))
			if err != nil {
				t.Fatal(err)
			}
			if updated.Usage.ReasoningEffort != effort {
				t.Fatalf("reasoning effort = %q, want %q", updated.Usage.ReasoningEffort, effort)
			}
		})
	}
}

func TestOpenCodeWorkspaceRejectsControllerPermissionModes(t *testing.T) {
	input := Defaults()
	input.Workspace.Engine = ProviderOpenCode
	input.Usage.PermissionMode = PermissionPlan
	if err := input.Validate(); err == nil || !strings.Contains(err.Error(), "OpenCode workspace") {
		t.Fatalf("OpenCode permission validation error = %v", err)
	}
}

func TestOptionalFeatureDependenciesFailClosed(t *testing.T) {
	defaults := Defaults()
	for name, body := range map[string]string{
		"heartbeat":      `{"features":{"heartbeat":true}}`,
		"remote control": `{"features":{"remote_control":true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := MergePatch(defaults, []byte(body))
			if err == nil || !strings.Contains(err.Error(), "requires") {
				t.Fatalf("dependency error = %v", err)
			}
			if !reflect.DeepEqual(got, defaults) {
				t.Fatalf("rejected dependency patch changed preferences: %#v", got)
			}
		})
	}

	unsafe := defaults
	unsafe.Features.Heartbeat = true
	unsafe.Features.RemoteControl = true
	normalized := unsafe.Normalize()
	if normalized.Features.Heartbeat || normalized.Features.RemoteControl {
		t.Fatalf("normalize retained orphaned authority: %#v", normalized.Features)
	}
}

func TestDecodeInvalidDocumentReturnsSafeDefaults(t *testing.T) {
	got, err := Decode([]byte(`{"version":1,"appearance":{"theme":"neon","density":"comfortable","font_scale":1},"usage":{"default_worker":"grok","reasoning_effort":"high","permission_mode":"default"},"computer":{"default_surface":"desktop","auto_takeover":false,"auto_agent_control":false},"safety":{"retain_transcripts":false,"retain_activity":false}}`))
	if err == nil || !strings.Contains(err.Error(), "appearance.theme") {
		t.Fatalf("invalid document error = %v", err)
	}
	if !reflect.DeepEqual(got, Defaults()) {
		t.Fatalf("invalid document should fail closed: %#v", got)
	}
}

func TestLegacyPreferencesWithoutOnboardingNormalizeToIncompleteCurrentFlow(t *testing.T) {
	legacy := []byte(`{"version":1,"appearance":{"theme":"system","density":"comfortable","font_scale":1},"usage":{"default_worker":"grok","reasoning_effort":"high","permission_mode":"default"},"computer":{"default_surface":"desktop","auto_takeover":false,"auto_agent_control":false},"safety":{"retain_transcripts":false,"retain_activity":false},"features":{}}`)
	got, err := Decode(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if got.Onboarding.Version != CurrentOnboardingVersion || got.Onboarding.Completed {
		t.Fatalf("legacy onboarding migration = %#v", got.Onboarding)
	}
	if got.Computer.Runtime != RuntimeColima {
		t.Fatalf("legacy runtime migration = %#v", got.Computer)
	}
}
