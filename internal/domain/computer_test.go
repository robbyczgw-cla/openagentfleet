package domain

import "testing"

func TestNormalizeAgentComputerDefaults(t *testing.T) {
	computer, err := NormalizeAgentComputer(AgentComputer{})
	if err != nil {
		t.Fatal(err)
	}
	if computer.ID != DefaultAgentComputerID || computer.Backend != AgentComputerBackendDocker {
		t.Fatalf("defaults = %+v", computer)
	}
}

func TestNormalizeAgentComputerRejectsUnknownBackend(t *testing.T) {
	if _, err := NormalizeAgentComputer(AgentComputer{Backend: "firecracker"}); err == nil {
		t.Fatal("unknown backend accepted")
	}
}

func TestEngineChangeDoesNotChangeComputerOrIdentity(t *testing.T) {
	original := AgentMetadata{
		Lead:     &AgentExecutionProfile{Harness: "grok_build", Model: "grok-4.6"},
		Computer: &AgentComputer{ID: "desk-1", Backend: "docker", Workspace: "/workspace/desk-1"},
	}
	normalized, err := NormalizeAgentMetadata(original)
	if err != nil {
		t.Fatal(err)
	}
	normalized.Lead = &AgentExecutionProfile{Harness: "codex_app_server", Model: "gpt-5"}
	switched, err := NormalizeAgentMetadata(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if switched.Lead == nil || switched.Lead.Harness != "codex_app_server" {
		t.Fatalf("engine did not switch: %+v", switched.Lead)
	}
	if switched.Computer == nil || switched.Computer.ID != "desk-1" || switched.Computer.Backend != "docker" {
		t.Fatalf("computer mutated: %+v", switched.Computer)
	}
	if switched.EffectiveComputer().ID != "desk-1" {
		t.Fatalf("effective computer = %+v", switched.EffectiveComputer())
	}
}

func TestEffectiveComputerDefaultsWhenMissing(t *testing.T) {
	metadata := AgentMetadata{}
	computer := metadata.EffectiveComputer()
	if computer.ID != DefaultAgentComputerID || computer.Backend != AgentComputerBackendDocker {
		t.Fatalf("effective = %+v", computer)
	}
}

func TestCanonicalEventType(t *testing.T) {
	if CanonicalEventType(EventRunStarted) != EventAgentTurnStarted {
		t.Fatal("run.started mapping")
	}
	if CanonicalEventType("agent.thinking") != EventAgentThinking {
		t.Fatal("passthrough")
	}
}
