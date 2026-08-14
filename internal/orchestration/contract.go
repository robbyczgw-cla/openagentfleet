// Package orchestration defines the side-effect-free routing contract between
// the OpenAgentFleet controller, a user-selected lead harness, and bounded
// workers. It validates plans and authorization state; it does not start
// harnesses, invoke models, or claim that worker delegation has occurred.
package orchestration

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

const CurrentVersion = 1

// Controller identifies the built-in process that owns policy, approvals, and
// run lifecycle. A Controller is deliberately not a LeadHarness or Worker.
type Controller string

const ControllerOpenAgentFleet Controller = "openagentfleet"

// LeadHarness is the external harness selected by the user for a run. The
// controller may launch and supervise it, but never silently chooses one.
type LeadHarness string

const (
	LeadGrokBuild      LeadHarness = "grok_build"
	LeadCodexAppServer LeadHarness = "codex_app_server"
	LeadOpenCode       LeadHarness = "opencode"
)

// Worker identifies a model-facing worker a lead may route bounded work to.
// The distinct type prevents accidental lead/worker substitution even where a
// provider, such as OpenCode, supports both roles under the same stable ID.
type Worker string

const (
	WorkerPi       Worker = "pi"
	WorkerClaude   Worker = "claude"
	WorkerCodex    Worker = "codex"
	WorkerGrok     Worker = "grok"
	WorkerOpenCode Worker = "opencode"
	WorkerCursor   Worker = "cursor"
)

// Capability is an exact grant from the controller to the lead. Capabilities
// are additive; an omitted capability is denied.
type Capability string

const (
	CapabilityWorkspaceRead  Capability = "workspace_read"
	CapabilityWorkspaceWrite Capability = "workspace_write"
	CapabilityDelegate       Capability = "worker_delegation"
	CapabilityBrowser        Capability = "browser"
	CapabilityDesktopView    Capability = "desktop_view"
	CapabilityComputerInput  Capability = "computer_input"
)

// ReasoningEffort is a provider-neutral request. Adapters may map a supported
// value to provider-specific flags, but must not silently increase it.
type ReasoningEffort string

const (
	ReasoningDefault ReasoningEffort = "default"
	ReasoningLow     ReasoningEffort = "low"
	ReasoningMedium  ReasoningEffort = "medium"
	ReasoningHigh    ReasoningEffort = "high"
	ReasoningXHigh   ReasoningEffort = "xhigh"
	ReasoningMax     ReasoningEffort = "max"
)

// ServiceTier is a provider-neutral request for the scheduling class of a run.
// Adapters may map a supported value onto provider-specific flags, but must
// never upgrade a requested tier or substitute a different one. The contract
// deliberately defines no ordering over tiers, so no code path can "round up".
type ServiceTier string

const (
	ServiceTierDefault  ServiceTier = "default"
	ServiceTierPriority ServiceTier = "priority"
	ServiceTierFlex     ServiceTier = "flex"
)

// PermissionMode bounds worker-side tool permissions. ProviderDefault is an
// explicit handoff to a provider's safe default, not a controller approval or
// an unrestricted/auto-approval mode.
type PermissionMode string

const (
	PermissionReadOnly        PermissionMode = "read_only"
	PermissionWorkspace       PermissionMode = "workspace"
	PermissionAsk             PermissionMode = "ask"
	PermissionProviderDefault PermissionMode = "provider_default"
)

// ApprovalStatus describes authorization without conflating a valid pending
// plan with a plan that is ready to execute.
type ApprovalStatus string

const (
	ApprovalNone     ApprovalStatus = "none"
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
)

// DecisionState is derived from a validated plan. A pending decision is safe
// to persist or show in UI but must not be sent to an executor.
type DecisionState string

const (
	DecisionPendingApproval DecisionState = "pending_approval"
	DecisionReady           DecisionState = "ready"
)

// WorkerOptions are provider-neutral routing hints for one worker. An empty
// Model means the worker's configured default model; it never means that the
// controller may substitute a different worker. An empty ServiceTier likewise
// means the adapter's configured tier: priority and flex are only ever reached
// by asking for them by name.
type WorkerOptions struct {
	Model       string          `json:"model,omitempty"`
	Reasoning   ReasoningEffort `json:"reasoning"`
	ServiceTier ServiceTier     `json:"service_tier,omitempty"`
	Permission  PermissionMode  `json:"permission"`
}

// LeadOptions are the provider-neutral execution knobs for the lead harness
// itself. They mirror WorkerOptions so the lead is described in the same
// vocabulary as the workers it routes to. Sharing the vocabulary grants the
// lead no authority: capabilities still come only from AuthorizationBoundary.
type LeadOptions struct {
	Model       string          `json:"model,omitempty"`
	Reasoning   ReasoningEffort `json:"reasoning"`
	ServiceTier ServiceTier     `json:"service_tier,omitempty"`
	Permission  PermissionMode  `json:"permission"`
}

// LeadRunProfile is the execution profile applied to the selected lead for one
// run. It intentionally carries no harness identity: RunPlan.Lead stays the
// single source of truth for which lead the user picked, so a profile can
// never redirect a run to a different harness.
//
// A wholly zero profile is valid and means the plan requests no lead-level
// profile at all; the adapter then uses its own configured defaults. Anything
// else is validated strictly, so an adapter never has to guess a missing value.
type LeadRunProfile struct {
	Options LeadOptions `json:"options"`
}

// WorkerRoute selects the primary worker used by the lead.
type WorkerRoute struct {
	Worker  Worker        `json:"worker"`
	Options WorkerOptions `json:"options"`
}

// DelegatedWorker is an optional, bounded unit of delegated work. Both bounds
// are mandatory so a lead cannot turn a delegation into an unbounded run.
type DelegatedWorker struct {
	Route              WorkerRoute `json:"route"`
	Scope              string      `json:"scope"`
	MaxTurns           uint16      `json:"max_turns"`
	MaxDurationSeconds uint32      `json:"max_duration_seconds"`
}

// ComputerRequirements declares surfaces needed by the run. ComputerInput
// means keyboard or pointer control and therefore also requires DesktopView.
type ComputerRequirements struct {
	Browser       bool `json:"browser"`
	DesktopView   bool `json:"desktop_view"`
	ComputerInput bool `json:"computer_input"`
}

// AuthorizationBoundary captures the two-level trust boundary. PolicyOwner
// must remain OpenAgentFleet. The selected lead receives only the listed
// capabilities and may route only to the listed workers.
type AuthorizationBoundary struct {
	PolicyOwner      Controller   `json:"policy_owner"`
	LeadCapabilities []Capability `json:"lead_capabilities"`
	AllowedWorkers   []Worker     `json:"allowed_workers"`
}

// HumanApproval records the exact interactive capabilities covered by one
// controller-owned approval. Sensitive capabilities must be listed even while
// Status is pending. An approved record requires a non-empty ApprovalID.
type HumanApproval struct {
	Status       ApprovalStatus `json:"status"`
	ApprovalID   string         `json:"approval_id,omitempty"`
	Capabilities []Capability   `json:"capabilities,omitempty"`
}

// RunPlan is the JSON-safe input contract for a two-level run. It contains no
// runtime handles and grants no authority merely by being deserialized.
type RunPlan struct {
	Version       int                   `json:"version"`
	Controller    Controller            `json:"controller"`
	Lead          LeadHarness           `json:"lead"`
	LeadProfile   LeadRunProfile        `json:"lead_profile"`
	Primary       WorkerRoute           `json:"primary"`
	Delegated     []DelegatedWorker     `json:"delegated,omitempty"`
	Workdir       string                `json:"workdir"`
	Computer      ComputerRequirements  `json:"computer"`
	Authorization AuthorizationBoundary `json:"authorization"`
	Approval      HumanApproval         `json:"human_approval"`
}

// RoutingDecision is a validated, non-executing routing result. Call
// ValidateForExecution before handing it to a future runtime adapter.
type RoutingDecision struct {
	Plan  RunPlan       `json:"plan"`
	State DecisionState `json:"state"`
}

// DefaultRunPlan returns a conservative plan with an explicit lead and worker.
// It grants workspace read access only and requires worker-side prompting for
// any additional permission. The lead and the primary worker both start on the
// default reasoning effort and the default service tier, so upgrading either
// one stays an explicit, auditable edit to the plan.
func DefaultRunPlan(lead LeadHarness, worker Worker, workdir string) RunPlan {
	return RunPlan{
		Version:    CurrentVersion,
		Controller: ControllerOpenAgentFleet,
		Lead:       lead,
		LeadProfile: LeadRunProfile{
			Options: LeadOptions{
				Reasoning:   ReasoningDefault,
				ServiceTier: ServiceTierDefault,
				Permission:  PermissionAsk,
			},
		},
		Primary: WorkerRoute{
			Worker: worker,
			Options: WorkerOptions{
				Reasoning:   ReasoningDefault,
				ServiceTier: ServiceTierDefault,
				Permission:  PermissionAsk,
			},
		},
		Workdir: workdir,
		Authorization: AuthorizationBoundary{
			PolicyOwner:      ControllerOpenAgentFleet,
			LeadCapabilities: []Capability{CapabilityWorkspaceRead},
			AllowedWorkers:   []Worker{worker},
		},
		Approval: HumanApproval{Status: ApprovalNone},
	}
}

// Decide validates a plan and derives whether it is ready or awaiting human
// approval. It has no side effects.
func Decide(plan RunPlan) (RoutingDecision, error) {
	if err := plan.Validate(); err != nil {
		return RoutingDecision{}, err
	}
	state := DecisionReady
	if plan.Approval.Status == ApprovalPending {
		state = DecisionPendingApproval
	}
	return RoutingDecision{Plan: plan, State: state}, nil
}

// Validate checks schema values, routing bounds, and authorization coverage.
// It accepts a pending approval so a safe plan can exist before the user acts.
func (p RunPlan) Validate() error {
	if p.Version != CurrentVersion {
		return fmt.Errorf("unsupported orchestration version %d", p.Version)
	}
	if p.Controller != ControllerOpenAgentFleet {
		return fmt.Errorf("controller must be %q", ControllerOpenAgentFleet)
	}
	if !validLead(p.Lead) {
		return fmt.Errorf("invalid lead harness %q", p.Lead)
	}
	if err := validateLeadProfile(p.Lead, p.LeadProfile); err != nil {
		return err
	}
	if !validWorker(p.Primary.Worker) {
		return fmt.Errorf("invalid primary worker %q", p.Primary.Worker)
	}
	if err := validateOptions("primary", p.Primary.Options); err != nil {
		return err
	}
	if err := validateWorkdir(p.Workdir); err != nil {
		return err
	}
	if p.Authorization.PolicyOwner != ControllerOpenAgentFleet {
		return fmt.Errorf("authorization policy_owner must be %q", ControllerOpenAgentFleet)
	}

	leadCaps, err := capabilitySet("authorization.lead_capabilities", p.Authorization.LeadCapabilities)
	if err != nil {
		return err
	}
	allowedWorkers, err := workerSet("authorization.allowed_workers", p.Authorization.AllowedWorkers)
	if err != nil {
		return err
	}
	if _, ok := allowedWorkers[p.Primary.Worker]; !ok {
		return fmt.Errorf("primary worker %q is not authorized for the lead", p.Primary.Worker)
	}
	if p.LeadProfile.Options.Permission == PermissionWorkspace {
		if _, ok := leadCaps[CapabilityWorkspaceWrite]; !ok {
			return fmt.Errorf("lead_profile workspace permission requires %q capability", CapabilityWorkspaceWrite)
		}
	}
	if p.Primary.Options.Permission == PermissionWorkspace {
		if _, ok := leadCaps[CapabilityWorkspaceWrite]; !ok {
			return fmt.Errorf("primary workspace permission requires %q capability", CapabilityWorkspaceWrite)
		}
	}

	selected := map[Worker]struct{}{p.Primary.Worker: {}}
	if len(p.Delegated) > 8 {
		return fmt.Errorf("delegated workers exceed maximum of 8")
	}
	if len(p.Delegated) > 0 {
		if _, ok := leadCaps[CapabilityDelegate]; !ok {
			return fmt.Errorf("delegated workers require %q capability", CapabilityDelegate)
		}
	}
	for i, delegated := range p.Delegated {
		label := fmt.Sprintf("delegated[%d]", i)
		if !validWorker(delegated.Route.Worker) {
			return fmt.Errorf("%s has invalid worker %q", label, delegated.Route.Worker)
		}
		if _, duplicate := selected[delegated.Route.Worker]; duplicate {
			return fmt.Errorf("worker %q is selected more than once", delegated.Route.Worker)
		}
		selected[delegated.Route.Worker] = struct{}{}
		if _, ok := allowedWorkers[delegated.Route.Worker]; !ok {
			return fmt.Errorf("%s worker %q is not authorized for the lead", label, delegated.Route.Worker)
		}
		if err := validateOptions(label, delegated.Route.Options); err != nil {
			return err
		}
		if delegated.Route.Options.Permission == PermissionWorkspace {
			if _, ok := leadCaps[CapabilityWorkspaceWrite]; !ok {
				return fmt.Errorf("%s workspace permission requires %q capability", label, CapabilityWorkspaceWrite)
			}
		}
		if strings.TrimSpace(delegated.Scope) == "" {
			return fmt.Errorf("%s scope is required", label)
		}
		if delegated.MaxTurns == 0 || delegated.MaxTurns > 100 {
			return fmt.Errorf("%s max_turns must be between 1 and 100", label)
		}
		if delegated.MaxDurationSeconds == 0 || delegated.MaxDurationSeconds > 3600 {
			return fmt.Errorf("%s max_duration_seconds must be between 1 and 3600", label)
		}
	}

	if p.Computer.ComputerInput && !p.Computer.DesktopView {
		return fmt.Errorf("computer input requires desktop view")
	}
	requiredApproval := requiredInteractiveCapabilities(p.Computer)
	requiredSet := make(map[Capability]struct{}, len(requiredApproval))
	for _, capability := range requiredApproval {
		requiredSet[capability] = struct{}{}
	}
	for _, capability := range []Capability{CapabilityBrowser, CapabilityDesktopView, CapabilityComputerInput} {
		if _, granted := leadCaps[capability]; granted {
			if _, requested := requiredSet[capability]; !requested {
				return fmt.Errorf("lead capability %q is broader than the computer requirements", capability)
			}
		}
	}
	for _, capability := range requiredApproval {
		if _, ok := leadCaps[capability]; !ok {
			return fmt.Errorf("computer requirement needs lead capability %q", capability)
		}
	}
	if err := validateApproval(p.Approval, requiredApproval); err != nil {
		return err
	}
	return nil
}

// ValidateForExecution rejects pending approval. A future executor should call
// this immediately before using any harness, browser, or computer adapter.
func (d RoutingDecision) ValidateForExecution() error {
	if err := d.Plan.Validate(); err != nil {
		return err
	}
	if d.State != DecisionReady {
		return fmt.Errorf("routing decision is not ready: %s", d.State)
	}
	if d.Plan.Approval.Status == ApprovalPending {
		return fmt.Errorf("human approval is still pending")
	}
	return nil
}

func validateOptions(label string, options WorkerOptions) error {
	if !validReasoning(options.Reasoning) {
		return fmt.Errorf("%s has invalid reasoning effort %q", label, options.Reasoning)
	}
	// Empty is the legacy representation of an omitted tier. Preserve that
	// omission exactly; only explicit values are validated and forwarded.
	if options.ServiceTier != "" && !validServiceTier(options.ServiceTier) {
		return fmt.Errorf("%s has invalid service tier %q", label, options.ServiceTier)
	}
	if !validPermission(options.Permission) {
		return fmt.Errorf("%s has invalid permission mode %q", label, options.Permission)
	}
	return validateModel(label, options.Model)
}

func validateLeadProfile(lead LeadHarness, profile LeadRunProfile) error {
	options := profile.Options
	if options == (LeadOptions{}) {
		return nil
	}
	if !validReasoning(options.Reasoning) {
		return fmt.Errorf("lead_profile has invalid reasoning effort %q", options.Reasoning)
	}
	if !validServiceTier(options.ServiceTier) {
		return fmt.Errorf("lead_profile has invalid service tier %q", options.ServiceTier)
	}
	if !validPermission(options.Permission) {
		return fmt.Errorf("lead_profile has invalid permission mode %q", options.Permission)
	}
	if err := validateModel("lead_profile", options.Model); err != nil {
		return err
	}
	if lead == LeadOpenCode {
		if options.ServiceTier != ServiceTierDefault {
			return fmt.Errorf("OpenCode lead service tier must be %q", ServiceTierDefault)
		}
		if options.Permission != PermissionAsk {
			return fmt.Errorf("OpenCode lead permission mode must be %q because opencode run has no safe permission override", PermissionAsk)
		}
		if options.Model != "" && !validOpenCodeModel(options.Model) {
			return fmt.Errorf("OpenCode lead model must use provider/model format")
		}
	}
	return nil
}

func validOpenCodeModel(value string) bool {
	provider, model, found := strings.Cut(value, "/")
	return found && provider != "" && model != "" &&
		!strings.ContainsAny(provider, " \t\r\n") && !strings.ContainsAny(model, " \t\r\n")
}

func validateModel(label, model string) error {
	if len(model) > 128 || hasControl(model) {
		return fmt.Errorf("%s model must be at most 128 characters without control characters", label)
	}
	if model != strings.TrimSpace(model) {
		return fmt.Errorf("%s model must not have surrounding whitespace", label)
	}
	return nil
}

func validateWorkdir(workdir string) error {
	if strings.TrimSpace(workdir) == "" || hasControl(workdir) {
		return fmt.Errorf("workdir must be a non-empty absolute path")
	}
	if !filepath.IsAbs(workdir) || filepath.Clean(workdir) != workdir {
		return fmt.Errorf("workdir must be a clean absolute path")
	}
	return nil
}

func validateApproval(approval HumanApproval, required []Capability) error {
	approvedCaps, err := capabilitySet("human_approval.capabilities", approval.Capabilities)
	if err != nil {
		return err
	}
	if len(required) == 0 {
		if approval.Status != ApprovalNone || approval.ApprovalID != "" || len(approval.Capabilities) != 0 {
			return fmt.Errorf("human approval must be empty when no interactive capability is requested")
		}
		return nil
	}
	if approval.Status != ApprovalPending && approval.Status != ApprovalApproved {
		return fmt.Errorf("interactive capabilities require pending or approved human approval")
	}
	if strings.TrimSpace(approval.ApprovalID) == "" {
		return fmt.Errorf("interactive capabilities require an approval_id")
	}
	for _, capability := range required {
		if _, ok := approvedCaps[capability]; !ok {
			return fmt.Errorf("interactive capability %q is not covered by human approval", capability)
		}
	}
	if len(approvedCaps) != len(required) {
		return fmt.Errorf("human approval contains capabilities outside the computer requirements")
	}
	return nil
}

func requiredInteractiveCapabilities(requirements ComputerRequirements) []Capability {
	var required []Capability
	if requirements.Browser {
		required = append(required, CapabilityBrowser)
	}
	if requirements.DesktopView {
		required = append(required, CapabilityDesktopView)
	}
	if requirements.ComputerInput {
		required = append(required, CapabilityComputerInput)
	}
	return required
}

func capabilitySet(label string, values []Capability) (map[Capability]struct{}, error) {
	result := make(map[Capability]struct{}, len(values))
	for _, value := range values {
		if !validCapability(value) {
			return nil, fmt.Errorf("%s contains invalid capability %q", label, value)
		}
		if _, duplicate := result[value]; duplicate {
			return nil, fmt.Errorf("%s contains duplicate capability %q", label, value)
		}
		result[value] = struct{}{}
	}
	return result, nil
}

func workerSet(label string, values []Worker) (map[Worker]struct{}, error) {
	result := make(map[Worker]struct{}, len(values))
	for _, value := range values {
		if !validWorker(value) {
			return nil, fmt.Errorf("%s contains invalid worker %q", label, value)
		}
		if _, duplicate := result[value]; duplicate {
			return nil, fmt.Errorf("%s contains duplicate worker %q", label, value)
		}
		result[value] = struct{}{}
	}
	return result, nil
}

func validLead(value LeadHarness) bool {
	return value == LeadGrokBuild || value == LeadCodexAppServer || value == LeadOpenCode
}

func validWorker(value Worker) bool {
	switch value {
	case WorkerPi, WorkerClaude, WorkerCodex, WorkerGrok, WorkerOpenCode, WorkerCursor:
		return true
	default:
		return false
	}
}

func validCapability(value Capability) bool {
	switch value {
	case CapabilityWorkspaceRead, CapabilityWorkspaceWrite, CapabilityDelegate,
		CapabilityBrowser, CapabilityDesktopView, CapabilityComputerInput:
		return true
	default:
		return false
	}
}

func validReasoning(value ReasoningEffort) bool {
	switch value {
	case ReasoningDefault, ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningXHigh, ReasoningMax:
		return true
	default:
		return false
	}
}

func validServiceTier(value ServiceTier) bool {
	return value == ServiceTierDefault || value == ServiceTierPriority || value == ServiceTierFlex
}

func validPermission(value PermissionMode) bool {
	return value == PermissionReadOnly || value == PermissionWorkspace || value == PermissionAsk || value == PermissionProviderDefault
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}
