package httpapi

import (
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestProjectSessionCannotResumeAfterDetachingConversation(t *testing.T) {
	instance, conversation, server := openTasksAPI(t, "")
	project, err := instance.CreateProject(t.Context(), domain.ProjectDraft{Name: "Private", Brief: "Private brief", AgentIDs: []string{conversation.BotID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.SetProjectAssociation(t.Context(), project.ID, domain.ProjectSubjectConversation, conversation.ID); err != nil {
		t.Fatal(err)
	}
	parent := createAPITask(t, instance, conversation, "Original", "Original")
	if _, err := instance.SnapshotProjectForRun(t.Context(), parent.ID, domain.ProjectSubjectConversation, conversation.ID, conversation.BotID); err != nil {
		t.Fatal(err)
	}
	if err := instance.DeleteProjectAssociation(t.Context(), domain.ProjectSubjectConversation, conversation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.ArchiveProject(t.Context(), project.ID, project.Version); err != nil {
		t.Fatal(err)
	}
	// The old run reports its session after the association changes.
	if err := instance.BindHarnessSessionContext(t.Context(), parent.ID, parent.Provider, "private-session", server.HarnessWorkdir); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.UpsertHarnessSession(t.Context(), conversation.ID, parent.Provider, "private-session", server.HarnessWorkdir, "Old session", "ready"); err != nil {
		t.Fatal(err)
	}
	next := createAPITask(t, instance, conversation, "Ordinary", "Ordinary")
	if _, err := instance.SnapshotProjectForRun(t.Context(), next.ID, domain.ProjectSubjectConversation, conversation.ID, conversation.BotID); err != nil {
		t.Fatal(err)
	}
	if selected, err := server.selectRunSession(t.Context(), next, "", false); err != nil || selected != "" {
		t.Fatalf("automatic session = %q, %v", selected, err)
	}
	if _, err := server.selectRunSession(t.Context(), next, "private-session", false); err == nil {
		t.Fatal("explicit resume crossed project boundary")
	}
	if err := instance.BindHarnessSessionContext(t.Context(), next.ID, next.Provider, "private-session", server.HarnessWorkdir); err == nil {
		t.Fatal("session context was overwritten")
	}
}
