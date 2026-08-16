package orchestration

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultRunPlanIsReadyAndJSONSafe(t *testing.T) {
	plan := DefaultRunPlan(LeadCodexAppServer, WorkerClaude, "/workspace/project")
	if plan.LeadProfile.Options.Reasoning != ReasoningDefault ||
		plan.LeadProfile.Options.ServiceTier != ServiceTierDefault ||
		plan.LeadProfile.Options.Permission != PermissionAsk {
		t.Fatalf("default lead options = %#v, want conservative defaults", plan.LeadProfile.Options)
	}
	if plan.Primary.Options.ServiceTier != ServiceTierDefault {
		t.Fatalf("default worker service tier = %q, want %q", plan.Primary.Options.ServiceTier, ServiceTierDefault)
	}
	decision, err := Decide(plan)
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if decision.State != DecisionReady {
		t.Fatalf("state = %q, want %q", decision.State, DecisionReady)
	}
	if err := decision.ValidateForExecution(); err != nil {
		t.Fatalf("ValidateForExecution() error = %v", err)
	}

	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded RunPlan
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded.Validate() error = %v", err)
	}
}

func TestExecutionProfilesAcceptBothLeadsAndAllServiceTiers(t *testing.T) {
	for _, lead := range []LeadHarness{LeadGrokBuild, LeadCodexAppServer} {
		for _, tier := range []ServiceTier{ServiceTierDefault, ServiceTierPriority, ServiceTierFlex} {
			t.Run(string(lead)+"/"+string(tier), func(t *testing.T) {
				plan := DefaultRunPlan(lead, WorkerCodex, "/workspace")
				plan.LeadProfile.Options = LeadOptions{
					Model:       "lead-model",
					Reasoning:   ReasoningMax,
					ServiceTier: tier,
					Permission:  PermissionAsk,
				}
				plan.Primary.Options = WorkerOptions{
					Model:       "worker-model",
					Reasoning:   ReasoningMax,
					ServiceTier: tier,
					Permission:  PermissionReadOnly,
				}
				if err := plan.Validate(); err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
			})
		}
	}
}

func TestPiLeadIsValidAndNotOpenCode(t *testing.T) {
	plan := DefaultRunPlan(LeadPi, WorkerClaude, "/workspace")
	plan.LeadProfile.Options = LeadOptions{
		Model:       "",
		Reasoning:   ReasoningHigh,
		ServiceTier: ServiceTierPriority,
		Permission:  PermissionAsk,
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("Pi lead Validate() error = %v", err)
	}
	if plan.Lead != LeadPi {
		t.Fatalf("lead = %q, want %q", plan.Lead, LeadPi)
	}
}

func TestZeroLeadProfileAndOmittedWorkerTierRemainBackwardCompatible(t *testing.T) {
	plan := DefaultRunPlan(LeadGrokBuild, WorkerPi, "/workspace")
	plan.LeadProfile = LeadRunProfile{}
	plan.Primary.Options.ServiceTier = ""
	if err := plan.Validate(); err != nil {
		t.Fatalf("legacy plan rejected: %v", err)
	}
}

func TestOpenCodeLeadProfileMapsOnlySupportedControls(t *testing.T) {
	plan := DefaultRunPlan(LeadOpenCode, WorkerClaude, "/workspace")
	plan.LeadProfile.Options.Model = "openai/gpt-5"
	plan.LeadProfile.Options.Reasoning = ReasoningHigh
	if err := plan.Validate(); err != nil {
		t.Fatalf("valid OpenCode lead rejected: %v", err)
	}

	for name, mutate := range map[string]func(*RunPlan){
		"model":      func(plan *RunPlan) { plan.LeadProfile.Options.Model = "gpt-5" },
		"tier":       func(plan *RunPlan) { plan.LeadProfile.Options.ServiceTier = ServiceTierPriority },
		"permission": func(plan *RunPlan) { plan.LeadProfile.Options.Permission = PermissionWorkspace },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := plan
			mutate(&invalid)
			if err := invalid.Validate(); err == nil {
				t.Fatal("unsupported OpenCode lead option was accepted")
			}
		})
	}
}

func TestExecutionProfileValidationRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RunPlan)
		want   string
	}{
		{
			name: "lead model whitespace",
			mutate: func(plan *RunPlan) {
				plan.LeadProfile.Options.Model = " lead-model"
			},
			want: "model",
		},
		{
			name: "lead reasoning",
			mutate: func(plan *RunPlan) {
				plan.LeadProfile.Options.Reasoning = ReasoningEffort("ultra")
			},
			want: "reasoning effort",
		},
		{
			name: "lead service tier",
			mutate: func(plan *RunPlan) {
				plan.LeadProfile.Options.ServiceTier = ServiceTier("turbo")
			},
			want: "service tier",
		},
		{
			name: "lead permission",
			mutate: func(plan *RunPlan) {
				plan.LeadProfile.Options.Permission = PermissionMode("auto")
			},
			want: "permission mode",
		},
		{
			name: "worker model control character",
			mutate: func(plan *RunPlan) {
				plan.Primary.Options.Model = "worker\nmodel"
			},
			want: "model",
		},
		{
			name: "worker reasoning",
			mutate: func(plan *RunPlan) {
				plan.Primary.Options.Reasoning = ReasoningEffort("extreme")
			},
			want: "reasoning effort",
		},
		{
			name: "worker service tier",
			mutate: func(plan *RunPlan) {
				plan.Primary.Options.ServiceTier = ServiceTier("express")
			},
			want: "service tier",
		},
		{
			name: "worker permission",
			mutate: func(plan *RunPlan) {
				plan.Primary.Options.Permission = PermissionMode("unrestricted")
			},
			want: "permission mode",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := DefaultRunPlan(LeadCodexAppServer, WorkerClaude, "/workspace")
			test.mutate(&plan)
			err := plan.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestLeadAndWorkerEnumsCannotBeConfused(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RunPlan)
	}{
		{
			name: "controller used as lead",
			mutate: func(plan *RunPlan) {
				plan.Lead = LeadHarness(ControllerOpenAgentFleet)
			},
		},
		{
			name: "lead used as worker",
			mutate: func(plan *RunPlan) {
				plan.Primary.Worker = Worker(LeadGrokBuild)
			},
		},
		{
			name: "worker used as lead",
			mutate: func(plan *RunPlan) {
				plan.Lead = LeadHarness(WorkerGrok)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := DefaultRunPlan(LeadGrokBuild, WorkerGrok, "/workspace")
			test.mutate(&plan)
			if err := plan.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want enum boundary error")
			}
		})
	}
}

func TestDelegationMustBeBoundedUniqueAndAuthorized(t *testing.T) {
	valid := DefaultRunPlan(LeadGrokBuild, WorkerGrok, "/workspace")
	valid.Authorization.LeadCapabilities = append(valid.Authorization.LeadCapabilities, CapabilityDelegate)
	valid.Authorization.AllowedWorkers = append(valid.Authorization.AllowedWorkers, WorkerClaude)
	valid.Delegated = []DelegatedWorker{{
		Route: WorkerRoute{
			Worker: WorkerClaude,
			Options: WorkerOptions{
				Model:      "claude-sonnet",
				Reasoning:  ReasoningHigh,
				Permission: PermissionReadOnly,
			},
		},
		Scope:              "Review only the changed Go package",
		MaxTurns:           12,
		MaxDurationSeconds: 600,
	}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid delegation rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*RunPlan)
		want   string
	}{
		{
			name: "missing delegation capability",
			mutate: func(plan *RunPlan) {
				plan.Authorization.LeadCapabilities = []Capability{CapabilityWorkspaceRead}
			},
			want: "worker_delegation",
		},
		{
			name: "duplicate selected worker",
			mutate: func(plan *RunPlan) {
				plan.Delegated[0].Route.Worker = WorkerGrok
			},
			want: "selected more than once",
		},
		{
			name: "unbounded turns",
			mutate: func(plan *RunPlan) {
				plan.Delegated[0].MaxTurns = 0
			},
			want: "max_turns",
		},
		{
			name: "unbounded duration",
			mutate: func(plan *RunPlan) {
				plan.Delegated[0].MaxDurationSeconds = 0
			},
			want: "max_duration_seconds",
		},
		{
			name: "worker outside lead authorization",
			mutate: func(plan *RunPlan) {
				plan.Authorization.AllowedWorkers = []Worker{WorkerGrok}
			},
			want: "not authorized",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := valid
			plan.Authorization.LeadCapabilities = append([]Capability(nil), valid.Authorization.LeadCapabilities...)
			plan.Authorization.AllowedWorkers = append([]Worker(nil), valid.Authorization.AllowedWorkers...)
			plan.Delegated = append([]DelegatedWorker(nil), valid.Delegated...)
			test.mutate(&plan)
			err := plan.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestInteractiveCapabilitiesRequireExplicitApproval(t *testing.T) {
	plan := DefaultRunPlan(LeadCodexAppServer, WorkerCodex, "/workspace")
	plan.Computer = ComputerRequirements{Browser: true, DesktopView: true, ComputerInput: true}
	plan.Authorization.LeadCapabilities = append(plan.Authorization.LeadCapabilities,
		CapabilityBrowser, CapabilityDesktopView, CapabilityComputerInput)

	if err := plan.Validate(); err == nil {
		t.Fatal("Validate() error = nil without interactive approval")
	}

	plan.Approval = HumanApproval{
		Status:       ApprovalPending,
		ApprovalID:   "approval-1",
		Capabilities: []Capability{CapabilityBrowser, CapabilityDesktopView, CapabilityComputerInput},
	}
	decision, err := Decide(plan)
	if err != nil {
		t.Fatalf("Decide(pending) error = %v", err)
	}
	if decision.State != DecisionPendingApproval {
		t.Fatalf("state = %q, want %q", decision.State, DecisionPendingApproval)
	}
	if err := decision.ValidateForExecution(); err == nil {
		t.Fatal("pending decision was accepted for execution")
	}

	plan.Approval.Status = ApprovalApproved
	decision, err = Decide(plan)
	if err != nil {
		t.Fatalf("Decide(approved) error = %v", err)
	}
	if err := decision.ValidateForExecution(); err != nil {
		t.Fatalf("approved decision rejected for execution: %v", err)
	}
}

func TestInteractiveCapabilityMustBeGrantedAndCovered(t *testing.T) {
	plan := DefaultRunPlan(LeadGrokBuild, WorkerPi, "/workspace")
	plan.Computer.Browser = true
	plan.Approval = HumanApproval{
		Status:       ApprovalApproved,
		ApprovalID:   "approval-2",
		Capabilities: []Capability{CapabilityBrowser},
	}
	if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "lead capability") {
		t.Fatalf("Validate() error = %v, want missing lead capability", err)
	}

	plan.Authorization.LeadCapabilities = append(plan.Authorization.LeadCapabilities, CapabilityBrowser)
	plan.Approval.Capabilities = nil
	if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "not covered") {
		t.Fatalf("Validate() error = %v, want missing approval coverage", err)
	}
}

func TestInteractiveGrantsAndApprovalsCannotBeBroaderThanRequirements(t *testing.T) {
	plan := DefaultRunPlan(LeadGrokBuild, WorkerPi, "/workspace")
	plan.Authorization.LeadCapabilities = append(plan.Authorization.LeadCapabilities, CapabilityBrowser)
	if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "broader") {
		t.Fatalf("Validate() error = %v, want over-broad lead capability", err)
	}

	plan.Computer.Browser = true
	plan.Approval = HumanApproval{
		Status:       ApprovalApproved,
		ApprovalID:   "approval-3",
		Capabilities: []Capability{CapabilityBrowser, CapabilityDesktopView},
	}
	if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("Validate() error = %v, want over-broad approval", err)
	}
}

func TestComputerInputRequiresDesktopView(t *testing.T) {
	plan := DefaultRunPlan(LeadGrokBuild, WorkerCursor, "/workspace")
	plan.Computer.ComputerInput = true
	if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "requires desktop view") {
		t.Fatalf("Validate() error = %v, want desktop dependency", err)
	}
}

func TestBroadAutoApprovalIsNotAPermissionMode(t *testing.T) {
	plan := DefaultRunPlan(LeadCodexAppServer, WorkerOpenCode, "/workspace")
	plan.Primary.Options.Permission = PermissionMode("auto")
	if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "permission mode") {
		t.Fatalf("Validate() error = %v, want invalid permission", err)
	}
}

func TestWorkspacePermissionRequiresWriteCapability(t *testing.T) {
	plan := DefaultRunPlan(LeadCodexAppServer, WorkerOpenCode, "/workspace")
	plan.Primary.Options.Permission = PermissionWorkspace
	if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "workspace_write") {
		t.Fatalf("Validate() error = %v, want workspace write capability", err)
	}
	plan.Authorization.LeadCapabilities = append(plan.Authorization.LeadCapabilities, CapabilityWorkspaceWrite)
	if err := plan.Validate(); err != nil {
		t.Fatalf("Validate() with write grant error = %v", err)
	}
}

func TestValidationRejectsMalformedBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RunPlan)
	}{
		{
			name: "controller policy owner changed",
			mutate: func(plan *RunPlan) {
				plan.Authorization.PolicyOwner = Controller("external")
			},
		},
		{
			name: "relative workdir",
			mutate: func(plan *RunPlan) {
				plan.Workdir = "workspace/project"
			},
		},
		{
			name: "duplicate capability",
			mutate: func(plan *RunPlan) {
				plan.Authorization.LeadCapabilities = append(plan.Authorization.LeadCapabilities, CapabilityWorkspaceRead)
			},
		},
		{
			name: "duplicate allowed worker",
			mutate: func(plan *RunPlan) {
				plan.Authorization.AllowedWorkers = append(plan.Authorization.AllowedWorkers, plan.Primary.Worker)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := DefaultRunPlan(LeadCodexAppServer, WorkerClaude, "/workspace/project")
			test.mutate(&plan)
			if err := plan.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want boundary error")
			}
		})
	}
}
