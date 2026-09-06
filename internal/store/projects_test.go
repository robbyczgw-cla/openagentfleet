package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestUnassociatedProjectSubjectWriteWaitsWithoutDeferredUpgrade(t *testing.T) {
	instance, first, _ := openProjectTestStore(t)
	run, err := instance.CreateRun(t.Context(), first.Conversation.ID, first.Bot.ID, "test", "ordinary chat")
	if err != nil {
		t.Fatal(err)
	}
	writer, err := instance.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	type result struct {
		snapshot *domain.ProjectTaskSnapshot
		err      error
	}
	done := make(chan result, 1)
	go func() {
		snapshot, snapshotErr := instance.SnapshotProjectForRun(t.Context(), run.ID, domain.ProjectSubjectConversation, first.Conversation.ID, first.Bot.ID)
		done <- result{snapshot: snapshot, err: snapshotErr}
	}()
	select {
	case got := <-done:
		_, _ = writer.ExecContext(context.Background(), "ROLLBACK")
		_ = writer.Close()
		t.Fatalf("subject write did not wait for writer: %#v, %v", got.snapshot, got.err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := writer.ExecContext(t.Context(), "ROLLBACK"); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.snapshot != nil {
			t.Fatalf("unassociated snapshot after writer release = %#v, %v", got.snapshot, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subject write did not resume after writer release")
	}
	var subjects int
	if err := instance.db.QueryRowContext(t.Context(), `SELECT count(*) FROM project_run_subjects WHERE run_id=?`, run.ID).Scan(&subjects); err != nil {
		t.Fatal(err)
	}
	if subjects != 1 {
		t.Fatalf("unassociated run wrote %d subject rows", subjects)
	}
}

func TestAssociatedProjectSnapshotWaitsForWriterAndRemainsAtomic(t *testing.T) {
	instance, first, _ := openProjectTestStore(t)
	project, err := instance.CreateProject(t.Context(), domain.ProjectDraft{
		Name: "Release", Brief: "fixed revision", AgentIDs: []string{first.Bot.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.SetProjectAssociation(t.Context(), project.ID, domain.ProjectSubjectConversation, first.Conversation.ID); err != nil {
		t.Fatal(err)
	}
	run, err := instance.CreateRun(t.Context(), first.Conversation.ID, first.Bot.ID, "test", "associated chat")
	if err != nil {
		t.Fatal(err)
	}
	writer, err := instance.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	type result struct {
		snapshot *domain.ProjectTaskSnapshot
		err      error
	}
	done := make(chan result, 1)
	go func() {
		snapshot, snapshotErr := instance.SnapshotProjectForRun(t.Context(), run.ID, domain.ProjectSubjectConversation, first.Conversation.ID, first.Bot.ID)
		done <- result{snapshot: snapshot, err: snapshotErr}
	}()
	select {
	case got := <-done:
		_, _ = writer.ExecContext(context.Background(), "ROLLBACK")
		_ = writer.Close()
		t.Fatalf("associated snapshot did not wait for writer: %#v, %v", got.snapshot, got.err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := writer.ExecContext(t.Context(), "ROLLBACK"); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.snapshot == nil || got.snapshot.ProjectID != project.ID || got.snapshot.Brief != project.Brief {
			t.Fatalf("associated snapshot after writer release = %#v, %v", got.snapshot, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("associated snapshot did not resume after writer release")
	}
	var subjects, snapshots int
	if err := instance.db.QueryRowContext(t.Context(), `SELECT
(SELECT count(*) FROM project_run_subjects WHERE run_id=?),
(SELECT count(*) FROM project_task_snapshots WHERE run_id=?)`, run.ID, run.ID).Scan(&subjects, &snapshots); err != nil {
		t.Fatal(err)
	}
	if subjects != 1 || snapshots != 1 {
		t.Fatalf("atomic rows = subjects %d snapshots %d", subjects, snapshots)
	}
}

func TestUnassignedRoutineProvenanceSelectsLaterProjectOnRetry(t *testing.T) {
	instance, agent, _ := openProjectTestStore(t)
	routine, err := instance.CreateRoutine(t.Context(), routineTestDraft(agent.Bot.ID, time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	parent, err := instance.CreateRun(t.Context(), agent.Conversation.ID, agent.Bot.ID, "grok", "routine before project assignment")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := instance.SnapshotProjectForRun(t.Context(), parent.ID, domain.ProjectSubjectRoutine, routine.ID, agent.Bot.ID)
	if err != nil || snapshot != nil {
		t.Fatalf("unassigned routine snapshot = %#v, %v", snapshot, err)
	}
	var storedType domain.ProjectSubjectType
	var storedID string
	if err := instance.db.QueryRowContext(t.Context(), `SELECT subject_type,subject_id FROM project_run_subjects WHERE run_id=?`, parent.ID).Scan(&storedType, &storedID); err != nil {
		t.Fatal(err)
	}
	if storedType != domain.ProjectSubjectRoutine || storedID != routine.ID {
		t.Fatalf("stored provenance = %s/%s", storedType, storedID)
	}

	routineProject, err := instance.CreateProject(t.Context(), domain.ProjectDraft{
		Name: "Later routine project", Brief: "routine retry instructions", AgentIDs: []string{agent.Bot.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	chatProject, err := instance.CreateProject(t.Context(), domain.ProjectDraft{
		Name: "Chat project", Brief: "wrong retry instructions", AgentIDs: []string{agent.Bot.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.SetProjectAssociation(t.Context(), routineProject.ID, domain.ProjectSubjectRoutine, routine.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.SetProjectAssociation(t.Context(), chatProject.ID, domain.ProjectSubjectConversation, agent.Conversation.ID); err != nil {
		t.Fatal(err)
	}
	child, err := instance.CreateRun(t.Context(), agent.Conversation.ID, agent.Bot.ID, "grok", "retry")
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.SnapshotTaskFollowupProject(t.Context(), parent.ID, child.ID, agent.Bot.ID); err != nil {
		t.Fatal(err)
	}
	childSnapshot, err := instance.GetTaskProjectSnapshot(t.Context(), child.ID)
	if err != nil || childSnapshot.ProjectID != routineProject.ID || childSnapshot.Brief != routineProject.Brief {
		t.Fatalf("retry snapshot = %#v, %v", childSnapshot, err)
	}
}

func openProjectTestStore(t *testing.T) (*Store, domain.Agent, domain.Agent) {
	t.Helper()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	first, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Ada", Title: "Planner"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Lin", Title: "Reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.MigrateProjects(t.Context()); err != nil {
		t.Fatal(err)
	}
	return instance, first, second
}

func TestProjectEditUsesVersionAndAppendsOnlyChangedBriefs(t *testing.T) {
	instance, first, second := openProjectTestStore(t)
	created, err := instance.CreateProject(t.Context(), domain.ProjectDraft{
		Name: "Launch", Brief: "# Goal\nShip safely.", AgentIDs: []string{first.Bot.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || created.BriefRevision != 1 || len(created.Members) != 1 {
		t.Fatalf("created project = %#v", created)
	}

	membershipEdit, err := instance.UpdateProject(t.Context(), created.ID, domain.ProjectUpdate{
		Name: created.Name, Brief: created.Brief, AgentIDs: []string{first.Bot.ID, second.Bot.ID}, ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if membershipEdit.Version != 2 || membershipEdit.BriefRevision != 1 {
		t.Fatalf("membership edit changed wrong versions: %#v", membershipEdit)
	}
	if _, err := instance.UpdateProject(t.Context(), created.ID, domain.ProjectUpdate{
		Name: "Stale", Brief: "stale", AgentIDs: []string{first.Bot.ID}, ExpectedVersion: 1,
	}); !errors.Is(err, ErrProjectVersionConflict) {
		t.Fatalf("stale update = %v", err)
	}

	briefEdit, err := instance.UpdateProject(t.Context(), created.ID, domain.ProjectUpdate{
		Name: "Launch plan", Brief: "# Goal\nShip after review.", AgentIDs: []string{first.Bot.ID, second.Bot.ID}, ExpectedVersion: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if briefEdit.Version != 3 || briefEdit.BriefRevision != 2 {
		t.Fatalf("brief edit versions = %#v", briefEdit)
	}
	revisions, err := instance.ListProjectBriefRevisions(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 2 || revisions[0].Brief != briefEdit.Brief || revisions[1].Brief != created.Brief {
		t.Fatalf("brief history = %#v", revisions)
	}
	if _, err := instance.db.ExecContext(t.Context(), `UPDATE project_brief_revisions SET brief='rewritten' WHERE project_id=? AND revision=1`, created.ID); err == nil {
		t.Fatal("database allowed a brief revision rewrite")
	}
}

func TestProjectSnapshotRequiresExplicitAssociationAndMembership(t *testing.T) {
	instance, first, second := openProjectTestStore(t)
	project, err := instance.CreateProject(t.Context(), domain.ProjectDraft{
		Name: "Private launch", Brief: "member-visible brief", AgentIDs: []string{first.Bot.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstRun, err := instance.CreateRun(t.Context(), first.Conversation.ID, first.Bot.ID, "test", "work")
	if err != nil {
		t.Fatal(err)
	}
	withoutAssociation, err := instance.SnapshotProjectForRun(t.Context(), firstRun.ID, domain.ProjectSubjectConversation, first.Conversation.ID, first.Bot.ID)
	if err != nil || withoutAssociation != nil {
		t.Fatalf("membership inferred association: %#v, %v", withoutAssociation, err)
	}
	if _, err := instance.SetProjectAssociation(t.Context(), project.ID, domain.ProjectSubjectConversation, second.Conversation.ID); !errors.Is(err, ErrProjectAgentNotMember) {
		t.Fatalf("associated non-member conversation: %v", err)
	}
	if _, err := instance.SetProjectAssociation(t.Context(), project.ID, domain.ProjectSubjectConversation, first.Conversation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.SnapshotProjectForRun(t.Context(), firstRun.ID, domain.ProjectSubjectConversation, first.Conversation.ID, second.Bot.ID); !errors.Is(err, ErrProjectRunAgentMismatch) {
		t.Fatalf("unrelated agent snapshot = %v", err)
	}

	snapshot, err := instance.SnapshotProjectForRun(t.Context(), firstRun.ID, domain.ProjectSubjectConversation, first.Conversation.ID, first.Bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || snapshot.BriefRevision != 1 || snapshot.Brief != project.Brief {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if _, err := instance.db.ExecContext(t.Context(), `UPDATE project_task_snapshots SET brief='rewritten' WHERE run_id=?`, firstRun.ID); err == nil {
		t.Fatal("database allowed a task snapshot rewrite")
	}

	privateMemory := "PRIVATE_AGENT_MEMORY_MUST_NOT_LEAK"
	if _, err := instance.CreateBotMemory(t.Context(), first.Bot.ID, domain.BotMemoryDraft{
		Category: domain.MemoryCategoryFact, Status: domain.MemoryStatusApproved, Source: domain.MemorySourceUser,
		Content: privateMemory, Priority: 3,
	}); err != nil {
		t.Fatal(err)
	}
	prompt, err := instance.ProjectBriefPromptForRun(t.Context(), firstRun.ID, first.Bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, project.Brief) || strings.Contains(prompt, privateMemory) {
		t.Fatalf("project prompt crossed memory boundary: %q", prompt)
	}
	if prompt, err := instance.ProjectBriefPromptForRun(t.Context(), firstRun.ID, second.Bot.ID); !errors.Is(err, ErrProjectRunAgentMismatch) || prompt != "" {
		t.Fatalf("unrelated agent prompt = %q, %v", prompt, err)
	}
	if err := instance.DeleteProjectAssociation(t.Context(), domain.ProjectSubjectConversation, first.Conversation.ID); err != nil {
		t.Fatal(err)
	}
	prompt, err = instance.ProjectBriefPromptForRun(t.Context(), firstRun.ID, first.Bot.ID)
	if err != nil || !strings.Contains(prompt, project.Brief) {
		t.Fatalf("association removal rewrote queued task context: %q, %v", prompt, err)
	}
	secondRun, err := instance.CreateRun(t.Context(), first.Conversation.ID, first.Bot.ID, "test", "after disassociation")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot, err := instance.SnapshotProjectForRun(t.Context(), secondRun.ID, domain.ProjectSubjectConversation, first.Conversation.ID, first.Bot.ID); err != nil || snapshot != nil {
		t.Fatalf("disassociated conversation received a new snapshot: %#v, %v", snapshot, err)
	}
}

func TestProjectRevocationAndArchiveBlockNewSnapshotsButKeepQueuedSnapshot(t *testing.T) {
	instance, first, second := openProjectTestStore(t)
	project, err := instance.CreateProject(t.Context(), domain.ProjectDraft{
		Name: "Migration", Brief: "revision one", AgentIDs: []string{first.Bot.ID, second.Bot.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.SetProjectAssociation(t.Context(), project.ID, domain.ProjectSubjectConversation, first.Conversation.ID); err != nil {
		t.Fatal(err)
	}
	queued, err := instance.CreateRun(t.Context(), first.Conversation.ID, first.Bot.ID, "test", "queued")
	if err != nil {
		t.Fatal(err)
	}
	before, err := instance.SnapshotProjectForRun(t.Context(), queued.ID, domain.ProjectSubjectConversation, first.Conversation.ID, first.Bot.ID)
	if err != nil {
		t.Fatal(err)
	}

	project, err = instance.UpdateProject(t.Context(), project.ID, domain.ProjectUpdate{
		Name: project.Name, Brief: "revision two", AgentIDs: []string{second.Bot.ID}, ExpectedVersion: project.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := instance.GetTaskProjectSnapshot(t.Context(), queued.ID)
	if err != nil || after.Brief != before.Brief || after.BriefRevision != before.BriefRevision {
		t.Fatalf("stored snapshot changed after revocation: %#v, %v", after, err)
	}
	if _, err := instance.SnapshotProjectForRun(t.Context(), queued.ID, domain.ProjectSubjectConversation, first.Conversation.ID, first.Bot.ID); !errors.Is(err, ErrProjectAgentNotMember) {
		t.Fatalf("revoked member passed enqueue retry: %v", err)
	}
	if prompt, err := instance.ProjectBriefPromptForRun(t.Context(), queued.ID, first.Bot.ID); !errors.Is(err, ErrProjectAgentNotMember) || prompt != "" {
		t.Fatalf("revoked member reached execution prompt: %q, %v", prompt, err)
	}
	newRun, err := instance.CreateRun(t.Context(), first.Conversation.ID, first.Bot.ID, "test", "new")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.SnapshotProjectForRun(t.Context(), newRun.ID, domain.ProjectSubjectConversation, first.Conversation.ID, first.Bot.ID); !errors.Is(err, ErrProjectAgentNotMember) {
		t.Fatalf("revoked member received snapshot: %v", err)
	}
	project, err = instance.UpdateProject(t.Context(), project.ID, domain.ProjectUpdate{
		Name: project.Name, Brief: project.Brief, AgentIDs: []string{first.Bot.ID, second.Bot.ID}, ExpectedVersion: project.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := instance.ProjectBriefPromptForRun(t.Context(), queued.ID, first.Bot.ID)
	if err != nil || !strings.Contains(prompt, "revision one") || strings.Contains(prompt, "revision two") {
		t.Fatalf("restored member did not receive original snapshot: %q, %v", prompt, err)
	}

	project, err = instance.ArchiveProject(t.Context(), project.ID, project.Version)
	if err != nil {
		t.Fatal(err)
	}
	if project.Status != domain.ProjectStatusArchived {
		t.Fatalf("archived project = %#v", project)
	}
	if prompt, err := instance.ProjectBriefPromptForRun(t.Context(), queued.ID, first.Bot.ID); !errors.Is(err, ErrProjectArchived) || prompt != "" {
		t.Fatalf("archived project reached execution prompt: %q, %v", prompt, err)
	}
	if active, err := instance.ListProjects(t.Context(), false); err != nil || len(active) != 0 {
		t.Fatalf("active projects = %#v, %v", active, err)
	}
	if all, err := instance.ListProjects(t.Context(), true); err != nil || len(all) != 1 {
		t.Fatalf("all projects = %#v, %v", all, err)
	}
	if _, err := instance.SetProjectAssociation(t.Context(), project.ID, domain.ProjectSubjectConversation, second.Conversation.ID); !errors.Is(err, ErrProjectArchived) {
		t.Fatalf("archived project association = %v", err)
	}
	if _, err := instance.UpdateProject(t.Context(), project.ID, domain.ProjectUpdate{
		Name: project.Name, Brief: project.Brief, AgentIDs: []string{second.Bot.ID}, ExpectedVersion: project.Version,
	}); !errors.Is(err, ErrProjectArchived) {
		t.Fatalf("archived project edit = %v", err)
	}
}

func TestProjectValidationRejectsUnknownDuplicateAndOversizedInput(t *testing.T) {
	instance, first, _ := openProjectTestStore(t)
	tests := []domain.ProjectDraft{
		{Name: "No members", Brief: "brief"},
		{Name: "Duplicate", Brief: "brief", AgentIDs: []string{first.Bot.ID, first.Bot.ID}},
		{Name: "Unknown", Brief: "brief", AgentIDs: []string{"bot-missing"}},
		{Name: "Oversized", Brief: strings.Repeat("x", domain.ProjectBriefMaxBytes+1), AgentIDs: []string{first.Bot.ID}},
	}
	for _, draft := range tests {
		if _, err := instance.CreateProject(t.Context(), draft); err == nil {
			t.Fatalf("invalid project accepted: %s", draft.Name)
		}
	}
}
