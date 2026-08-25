package domain

import "testing"

func TestNormalizeAgentMetadataMigratesLegacyGrokWithoutReinterpretingWorkerIDs(t *testing.T) {
	metadata, err := NormalizeAgentMetadata(AgentMetadata{
		LeadHarness: "grok",
		Model:       "grok-4.5",
		WorkerIDs:   []string{"reviewer-a", "custom-worker"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Lead == nil || metadata.Lead.Harness != "grok_build" || metadata.Lead.Model != "grok-4.5" {
		t.Fatalf("legacy Grok migration = %#v", metadata.Lead)
	}
	if metadata.Lead.WebSearch != AgentWebSearchLive {
		t.Fatalf("legacy Grok web_search = %q, want live", metadata.Lead.WebSearch)
	}
	if len(metadata.Workers) != 0 {
		t.Fatalf("legacy worker ids became execution profiles: %#v", metadata.Workers)
	}
	if len(metadata.WorkerIDs) != 2 || metadata.WorkerIDs[0] != "reviewer-a" {
		t.Fatalf("legacy worker ids were lost: %#v", metadata.WorkerIDs)
	}
}

func TestNormalizeAgentMetadataAcceptsPiLead(t *testing.T) {
	metadata, err := NormalizeAgentMetadata(AgentMetadata{Lead: &AgentExecutionProfile{
		Harness: "pi", Model: "anthropic/claude-sonnet-4-5", Reasoning: "high", Permission: "workspace",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Lead == nil || metadata.Lead.Harness != "pi" || metadata.Lead.Permission != "workspace" {
		t.Fatalf("Pi lead = %#v", metadata.Lead)
	}
	if metadata.Lead.WebSearch != AgentWebSearchDisabled {
		t.Fatalf("Pi web_search = %q, want disabled", metadata.Lead.WebSearch)
	}
	if metadata.Lead.ServiceTier != "default" {
		t.Fatalf("Pi service tier = %q", metadata.Lead.ServiceTier)
	}
}

func TestNormalizeAgentMetadataDefaultsPiLeadPermissionAndDisablesSearch(t *testing.T) {
	metadata, err := NormalizeAgentMetadata(AgentMetadata{LeadHarness: "pi", Model: "openai/gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Lead == nil || metadata.Lead.Harness != "pi" || metadata.Lead.Permission != "workspace" {
		t.Fatalf("Pi lead defaults = %#v", metadata.Lead)
	}
	if metadata.Lead.WebSearch != AgentWebSearchDisabled {
		t.Fatalf("Pi default web_search = %q, want disabled", metadata.Lead.WebSearch)
	}
}

func TestNormalizeAgentMetadataRejectsUnsupportedPiLeadControls(t *testing.T) {
	for name, lead := range map[string]AgentExecutionProfile{
		"tier":       {Harness: "pi", ServiceTier: "priority", Permission: "workspace"},
		"permission": {Harness: "pi", Permission: "provider_default"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeAgentMetadata(AgentMetadata{Lead: &lead}); err == nil {
				t.Fatal("unsupported Pi lead configuration was accepted")
			}
		})
	}
}

func TestNormalizeAgentMetadataDefaultsLeadWebSearchToLive(t *testing.T) {
	for _, harness := range []string{"grok_build", "codex_app_server", "opencode"} {
		t.Run(harness, func(t *testing.T) {
			metadata, err := NormalizeAgentMetadata(AgentMetadata{Lead: &AgentExecutionProfile{Harness: harness}})
			if err != nil {
				t.Fatal(err)
			}
			if metadata.Lead == nil || metadata.Lead.WebSearch != AgentWebSearchLive {
				t.Fatalf("normalized lead = %#v, want web_search live", metadata.Lead)
			}
		})
	}
}

func TestNormalizeAgentMetadataAcceptsOpenCodeLead(t *testing.T) {
	metadata, err := NormalizeAgentMetadata(AgentMetadata{Lead: &AgentExecutionProfile{
		Harness: "opencode", Model: "openai/gpt-5", Reasoning: "high", ServiceTier: "default", Permission: "provider_default",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Lead == nil || metadata.Lead.Harness != "opencode" || metadata.Lead.Model != "openai/gpt-5" || metadata.Lead.Reasoning != "high" {
		t.Fatalf("OpenCode lead = %#v", metadata.Lead)
	}
	if metadata.Lead.Permission != "provider_default" {
		t.Fatalf("OpenCode permission = %q", metadata.Lead.Permission)
	}
}

func TestNormalizeAgentMetadataMigratesEarlyOpenCodeAskValue(t *testing.T) {
	metadata, err := NormalizeAgentMetadata(AgentMetadata{Lead: &AgentExecutionProfile{Harness: "opencode", Permission: "ask"}})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Lead == nil || metadata.Lead.Permission != "provider_default" {
		t.Fatalf("OpenCode migration = %#v", metadata.Lead)
	}
}

func TestNormalizeAgentMetadataRejectsUnsupportedOpenCodeLeadControls(t *testing.T) {
	for name, lead := range map[string]AgentExecutionProfile{
		"model":      {Harness: "opencode", Model: "gpt-5", Permission: "ask"},
		"tier":       {Harness: "opencode", Model: "openai/gpt-5", ServiceTier: "priority", Permission: "ask"},
		"permission": {Harness: "opencode", Model: "openai/gpt-5", Permission: "workspace"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeAgentMetadata(AgentMetadata{Lead: &lead}); err == nil {
				t.Fatal("unsupported OpenCode lead configuration was accepted")
			}
		})
	}
}

func TestNormalizeAgentMetadataAcceptsDisabledLeadWebSearch(t *testing.T) {
	metadata, err := NormalizeAgentMetadata(AgentMetadata{Lead: &AgentExecutionProfile{
		Harness:   "codex_app_server",
		WebSearch: " disabled ",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Lead == nil || metadata.Lead.WebSearch != AgentWebSearchDisabled {
		t.Fatalf("normalized lead = %#v, want web_search disabled", metadata.Lead)
	}
}

func TestNormalizeAgentMetadataRejectsInvalidOrWorkerWebSearch(t *testing.T) {
	tests := map[string]AgentMetadata{
		"invalid lead value": {Lead: &AgentExecutionProfile{Harness: "codex_app_server", WebSearch: "cached"}},
		"worker field": {Workers: []AgentExecutionProfile{{
			ID: "researcher", Harness: "claude", WebSearch: AgentWebSearchLive,
		}}},
	}
	for name, metadata := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeAgentMetadata(metadata); err == nil {
				t.Fatal("unsupported web_search configuration was accepted")
			}
		})
	}
}

func TestNormalizeAgentMetadataKeepsUnknownLegacyLeadLoadableButUnexecutable(t *testing.T) {
	metadata, err := NormalizeAgentMetadata(AgentMetadata{LeadHarness: "legacy-custom-lead"})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Lead != nil || metadata.LeadHarness != "legacy-custom-lead" {
		t.Fatalf("unknown legacy lead was reinterpreted: %#v", metadata)
	}
}

func TestNormalizeAgentMetadataRejectsUnsupportedLeadControls(t *testing.T) {
	for name, lead := range map[string]AgentExecutionProfile{
		"grok service tier": {Harness: "grok_build", Reasoning: "high", ServiceTier: "priority", Permission: "ask"},
		"lead max turns":    {Harness: "codex_app_server", Reasoning: "high", ServiceTier: "default", Permission: "ask", MaxTurns: 2},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeAgentMetadata(AgentMetadata{Lead: &lead}); err == nil {
				t.Fatal("unsupported lead control was accepted")
			}
		})
	}
}

func TestNormalizeAgentExecutionProfileBoundsLegacyWorkers(t *testing.T) {
	profile, err := normalizeAgentExecutionProfile("worker", AgentExecutionProfile{ID: "reviewer", Harness: "claude", Reasoning: "high", ServiceTier: "default", Permission: "read_only"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if profile.MaxTurns != DefaultAgentWorkerMaxTurns || profile.TimeoutSeconds != DefaultAgentWorkerTimeout {
		t.Fatalf("legacy worker was not bounded: %#v", profile)
	}
}

func TestNormalizeAgentMetadataLeadChangeDoesNotChangeComputer(t *testing.T) {
	binding := AgentComputer{ID: "desk-1", Backend: AgentComputerBackendRemote, Workspace: "/var/oaf/desk-1"}
	metadata, err := NormalizeAgentMetadata(AgentMetadata{
		Lead:     &AgentExecutionProfile{Harness: "grok_build"},
		Computer: &binding,
	})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Computer == nil || *metadata.Computer != binding {
		t.Fatalf("computer binding = %#v, want %#v", metadata.Computer, binding)
	}
	lead := *metadata.Lead
	lead.Harness = "codex_app_server"
	metadata.Lead = &lead
	switched, err := NormalizeAgentMetadata(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if switched.Lead == nil || switched.Lead.Harness != "codex_app_server" {
		t.Fatalf("lead after engine switch = %#v", switched.Lead)
	}
	if switched.Computer == nil || *switched.Computer != binding {
		t.Fatalf("computer changed after lead switch: %#v", switched.Computer)
	}
	withoutComputer, err := NormalizeAgentMetadata(AgentMetadata{Lead: &AgentExecutionProfile{Harness: "pi"}})
	if err != nil {
		t.Fatal(err)
	}
	if withoutComputer.Computer != nil {
		t.Fatalf("nil computer was materialized: %#v", withoutComputer.Computer)
	}
}
