package httpapi

import (
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
)

func TestExecutionUsesQueuedProjectRevision(t *testing.T) {
	instance, conversation, server := openTasksAPI(t, "")
	project, err := instance.CreateProject(t.Context(), domain.ProjectDraft{
		Name: "Release", Brief: "Keep the original API.", AgentIDs: []string{conversation.BotID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.SetProjectAssociation(t.Context(), project.ID, domain.ProjectSubjectConversation, conversation.ID); err != nil {
		t.Fatal(err)
	}
	run := createAPITask(t, instance, conversation, "Review release", "Check the API")
	if _, err := instance.SnapshotProjectForRun(t.Context(), run.ID, domain.ProjectSubjectConversation, conversation.ID, conversation.BotID); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.UpdateProject(t.Context(), project.ID, domain.ProjectUpdate{
		Name: project.Name, Brief: "Replace the API.", AgentIDs: []string{conversation.BotID}, ExpectedVersion: project.Version,
	}); err != nil {
		t.Fatal(err)
	}
	executor := &recordingHarnessExecutor{}
	server.runExecutorOverride = executor
	server.executeRunWithContext(t.Context(), run, "Agent instructions", "", "", "", "", "", 0, nil)
	calls := executor.snapshot()
	if len(calls) != 1 {
		t.Fatalf("calls = %#v", calls)
	}
	prompt := calls[0].Options.SystemPrompt
	if !strings.Contains(prompt, "Keep the original API.") || strings.Contains(prompt, "Replace the API.") {
		t.Fatalf("execution did not use queued revision: %q", prompt)
	}
}

func TestProjectExecutorRechecksBeforeNextProviderCall(t *testing.T) {
	instance, conversation, server := openTasksAPI(t, "")
	project, err := instance.CreateProject(t.Context(), domain.ProjectDraft{
		Name: "Review", Brief: "Private brief", AgentIDs: []string{conversation.BotID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.SetProjectAssociation(t.Context(), project.ID, domain.ProjectSubjectConversation, conversation.ID); err != nil {
		t.Fatal(err)
	}
	run := createAPITask(t, instance, conversation, "Review", "Check changes")
	if _, err := instance.SnapshotProjectForRun(t.Context(), run.ID, domain.ProjectSubjectConversation, conversation.ID, conversation.BotID); err != nil {
		t.Fatal(err)
	}
	next := &recordingHarnessExecutor{}
	executor := projectRunExecutor{server: server, run: run, next: next}
	if _, err := executor.RunWithOptions(t.Context(), run.Provider, run.Prompt, server.HarnessWorkdir, harness.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.ArchiveProject(t.Context(), project.ID, project.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.RunWithOptions(t.Context(), run.Provider, "Synthesize", server.HarnessWorkdir, harness.RunOptions{}); err == nil {
		t.Fatal("revoked project allowed another provider call")
	}
	if calls := next.snapshot(); len(calls) != 1 {
		t.Fatalf("provider calls = %#v", calls)
	}
}

func TestArchivedProjectStopsQueuedExecution(t *testing.T) {
	instance, conversation, server := openTasksAPI(t, "")
	project, err := instance.CreateProject(t.Context(), domain.ProjectDraft{
		Name: "Private release", Brief: "Internal instructions", AgentIDs: []string{conversation.BotID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.SetProjectAssociation(t.Context(), project.ID, domain.ProjectSubjectConversation, conversation.ID); err != nil {
		t.Fatal(err)
	}
	run := createAPITask(t, instance, conversation, "Review release", "Check the API")
	if _, err := instance.SnapshotProjectForRun(t.Context(), run.ID, domain.ProjectSubjectConversation, conversation.ID, conversation.BotID); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.ArchiveProject(t.Context(), project.ID, project.Version); err != nil {
		t.Fatal(err)
	}
	executor := &recordingHarnessExecutor{}
	server.runExecutorOverride = executor
	server.executeRunWithContext(t.Context(), run, "", "", "", "", "", "", 0, nil)
	if calls := executor.snapshot(); len(calls) != 0 {
		t.Fatalf("archived brief reached executor: %#v", calls)
	}
	stored, err := instance.GetRun(t.Context(), run.ID)
	if err != nil || stored.Status != "blocked" {
		t.Fatalf("run = %#v, %v", stored, err)
	}
}
