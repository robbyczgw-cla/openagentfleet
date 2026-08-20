package orchestration

import (
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestNormalizeAgentMetadataCollaborationDefaultsAndCaps(t *testing.T) {
	metadata, err := domain.NormalizeAgentMetadata(domain.AgentMetadata{
		Collaboration: &domain.AgentCollaboration{Enabled: true, MaxDepth: 9, MaxActivePeerTasks: 9, TimeoutSeconds: 5000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Collaboration == nil || !metadata.Collaboration.Enabled {
		t.Fatalf("collaboration = %#v", metadata.Collaboration)
	}
	if metadata.Collaboration.MaxDepth != domain.MaxAgentCollaborationMaxDepth {
		t.Fatalf("max depth = %d", metadata.Collaboration.MaxDepth)
	}
	if metadata.Collaboration.MaxActivePeerTasks != domain.MaxAgentCollaborationMaxActivePeerTasks {
		t.Fatalf("max active = %d", metadata.Collaboration.MaxActivePeerTasks)
	}
	if metadata.Collaboration.TimeoutSeconds != domain.MaxAgentCollaborationTimeoutSeconds {
		t.Fatalf("timeout = %d", metadata.Collaboration.TimeoutSeconds)
	}

	defaults, err := domain.NormalizeAgentMetadata(domain.AgentMetadata{
		Collaboration: &domain.AgentCollaboration{Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Collaboration.MaxDepth != domain.DefaultAgentCollaborationMaxDepth ||
		defaults.Collaboration.MaxActivePeerTasks != domain.DefaultAgentCollaborationMaxActivePeerTasks ||
		defaults.Collaboration.TimeoutSeconds != domain.DefaultAgentCollaborationTimeoutSeconds {
		t.Fatalf("defaults = %#v", defaults.Collaboration)
	}
}

func TestValidateAgentTaskRejectsSameAgent(t *testing.T) {
	_, err := ValidateAgentTask(AgentTaskRequest{
		SourceBotID:   "bot-a",
		TargetBotID:   "bot-a",
		Collaboration: enabledCollaboration(),
	})
	if err == nil {
		t.Fatal("same-agent task was accepted")
	}
}

func TestValidateAgentTaskRejectsDisabledCollaboration(t *testing.T) {
	_, err := ValidateAgentTask(AgentTaskRequest{
		SourceBotID:   "bot-a",
		TargetBotID:   "bot-b",
		Collaboration: &domain.AgentCollaboration{Enabled: false},
	})
	if err == nil {
		t.Fatal("disabled collaboration was accepted")
	}
	_, err = ValidateAgentTask(AgentTaskRequest{SourceBotID: "bot-a", TargetBotID: "bot-b"})
	if err == nil {
		t.Fatal("missing collaboration was accepted")
	}
}

func TestValidateAgentTaskRejectsAllowlistMiss(t *testing.T) {
	_, err := ValidateAgentTask(AgentTaskRequest{
		SourceBotID: "bot-a",
		TargetBotID: "bot-c",
		Collaboration: &domain.AgentCollaboration{
			Enabled:       true,
			AllowAgentIDs: []string{"bot-b"},
		},
	})
	if err == nil {
		t.Fatal("allowlist miss was accepted")
	}
	task, err := ValidateAgentTask(AgentTaskRequest{
		SourceBotID: "bot-a",
		TargetBotID: "bot-b",
		SourceRunID: "run-a",
		Collaboration: &domain.AgentCollaboration{
			Enabled:       true,
			AllowAgentIDs: []string{"bot-b"},
		},
	})
	if err != nil || task.Depth != 1 || task.OriginRunID != "run-a" || task.TimeoutSeconds != int(domain.DefaultAgentCollaborationTimeoutSeconds) {
		t.Fatalf("allowlist hit = %#v, %v", task, err)
	}
}

func TestValidateAgentTaskRejectsDepthOverflow(t *testing.T) {
	parent := &domain.Handoff{ID: "h2", SourceBotID: "bot-b", TargetBotID: "bot-c", Depth: 2, OriginRunID: "origin"}
	_, err := ValidateAgentTask(AgentTaskRequest{
		SourceBotID:   "bot-c",
		TargetBotID:   "bot-d",
		Collaboration: enabledCollaboration(),
		Parent:        parent,
		Chain: []domain.Handoff{
			{ID: "h1", SourceBotID: "bot-a", TargetBotID: "bot-b", Depth: 1, OriginRunID: "origin"},
			*parent,
		},
	})
	if err == nil {
		t.Fatal("depth overflow was accepted")
	}
	okParent := &domain.Handoff{ID: "h1", SourceBotID: "bot-a", TargetBotID: "bot-b", Depth: 1, OriginRunID: "origin"}
	task, err := ValidateAgentTask(AgentTaskRequest{
		SourceBotID:   "bot-b",
		TargetBotID:   "bot-c",
		Collaboration: enabledCollaboration(),
		Parent:        okParent,
		Chain:         []domain.Handoff{*okParent},
	})
	if err != nil || task.Depth != 2 || task.ParentHandoffID != "h1" || task.OriginRunID != "origin" {
		t.Fatalf("depth at maximum = %#v, %v", task, err)
	}
}

func TestValidateAgentTaskRejectsPingPong(t *testing.T) {
	parent := &domain.Handoff{ID: "h1", SourceBotID: "bot-a", TargetBotID: "bot-b", Depth: 1, OriginRunID: "origin"}
	_, err := ValidateAgentTask(AgentTaskRequest{
		SourceBotID:   "bot-b",
		TargetBotID:   "bot-a",
		Collaboration: enabledCollaboration(),
		Parent:        parent,
		Chain:         []domain.Handoff{*parent},
	})
	if err == nil {
		t.Fatal("ping-pong A-B-A was accepted")
	}
}

func TestValidateAgentTaskRejectsActiveTaskCap(t *testing.T) {
	_, err := ValidateAgentTask(AgentTaskRequest{
		SourceBotID:   "bot-a",
		TargetBotID:   "bot-b",
		Collaboration: &domain.AgentCollaboration{Enabled: true, MaxActivePeerTasks: 2},
		ActiveCount:   2,
	})
	if err == nil {
		t.Fatal("active task cap was accepted")
	}
	task, err := ValidateAgentTask(AgentTaskRequest{
		SourceBotID:   "bot-a",
		TargetBotID:   "bot-b",
		SourceRunID:   "run-a",
		Collaboration: &domain.AgentCollaboration{Enabled: true, MaxActivePeerTasks: 2},
		ActiveCount:   1,
	})
	if err != nil || task.TargetBotID != "bot-b" {
		t.Fatalf("under cap = %#v, %v", task, err)
	}
}

func TestUserMentionStillOneAgentWithoutCollaboration(t *testing.T) {
	if err := ValidateAgentHandoff("bot-a", "bot-b"); err != nil {
		t.Fatalf("user mention handoff error = %v", err)
	}
	id, err := SingleMentionBotID([]string{"bot-b"})
	if err != nil || id != "bot-b" {
		t.Fatalf("single mention = %q, %v", id, err)
	}
	if _, err := SingleMentionBotID([]string{"bot-b", "bot-c"}); err == nil {
		t.Fatal("two mentions were accepted")
	}
}

func enabledCollaboration() *domain.AgentCollaboration {
	return &domain.AgentCollaboration{Enabled: true}
}
