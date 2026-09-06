package store

import (
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestRoutineTaskFollowupKeepsRoutineProject(t *testing.T) {
	instance, agent, _ := openProjectTestStore(t)
	project, err := instance.CreateProject(t.Context(), domain.ProjectDraft{Name: "Routine project", Brief: "Routine instructions", AgentIDs: []string{agent.Bot.ID}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := instance.CreateProject(t.Context(), domain.ProjectDraft{Name: "Chat project", Brief: "Chat instructions", AgentIDs: []string{agent.Bot.ID}})
	if err != nil {
		t.Fatal(err)
	}
	routine, err := instance.CreateRoutine(t.Context(), routineTestDraft(agent.Bot.ID, time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.SetProjectAssociation(t.Context(), project.ID, domain.ProjectSubjectRoutine, routine.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.SetProjectAssociation(t.Context(), other.ID, domain.ProjectSubjectConversation, agent.Conversation.ID); err != nil {
		t.Fatal(err)
	}
	parent, err := instance.CreateRun(t.Context(), agent.Conversation.ID, agent.Bot.ID, "grok", "Routine task")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.SnapshotProjectForRun(t.Context(), parent.ID, domain.ProjectSubjectRoutine, routine.ID, agent.Bot.ID); err != nil {
		t.Fatal(err)
	}
	child, err := instance.CreateRun(t.Context(), agent.Conversation.ID, agent.Bot.ID, "grok", "Retry routine task")
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.SnapshotTaskFollowupProject(t.Context(), parent.ID, child.ID, agent.Bot.ID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := instance.GetTaskProjectSnapshot(t.Context(), child.ID)
	if err != nil || snapshot.ProjectID != project.ID {
		t.Fatalf("retry project = %#v, %v", snapshot, err)
	}
}
