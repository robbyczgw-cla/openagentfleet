package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestTaskFollowupLineageStateAndIdempotency(t *testing.T) {
	instance, conversation := openTaskStore(t)
	parent := createTaskRun(t, instance, conversation, "Original brief", "wrapped original prompt")
	if err := instance.UpdateRun(t.Context(), parent.ID, "failed", "provider failed"); err != nil {
		t.Fatal(err)
	}
	request := CreateTaskFollowupInput{
		ParentTaskID: parent.ID, Kind: "retry", IdempotencyKey: "double-click",
		BotID: parent.BotID, Provider: "grok", Content: "Original brief", Prompt: "new bounded prompt",
	}
	first, err := instance.CreateTaskFollowup(t.Context(), request)
	if err != nil || !first.Created {
		t.Fatalf("first followup = %#v, %v", first, err)
	}
	second, err := instance.CreateTaskFollowup(t.Context(), request)
	if err != nil || second.Created || second.Run.ID != first.Run.ID || second.Message.ID != first.Message.ID {
		t.Fatalf("idempotent followup = %#v, %v", second, err)
	}
	conflict := request
	conflict.Content = "different request"
	if _, err := instance.CreateTaskFollowup(t.Context(), conflict); !errors.Is(err, ErrTaskFollowupConflict) {
		t.Fatalf("idempotency conflict = %v", err)
	}
	child, _, err := instance.GetTask(t.Context(), first.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentTaskID != parent.ID || child.RootTaskID != parent.ID || child.Attempt != 2 || child.FollowupKind != "retry" {
		t.Fatalf("child lineage = %#v", child)
	}
	if err := instance.UpdateRun(t.Context(), first.Run.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	revision, err := instance.CreateTaskFollowup(t.Context(), CreateTaskFollowupInput{
		ParentTaskID: first.Run.ID, Kind: "revise", IdempotencyKey: "revision",
		BotID: parent.BotID, Provider: "grok", Content: "Edited brief", Prompt: "edited wrapped prompt",
	})
	if err != nil || !revision.Created {
		t.Fatalf("revision = %#v, %v", revision, err)
	}
	revised, _, err := instance.GetTask(t.Context(), revision.Run.ID)
	if err != nil || revised.ParentTaskID != first.Run.ID || revised.RootTaskID != parent.ID || revised.Attempt != 3 || revised.Title != "Edited brief" {
		t.Fatalf("revised lineage = %#v, %v", revised, err)
	}
	if _, err := instance.CreateTaskFollowup(t.Context(), CreateTaskFollowupInput{
		ParentTaskID: parent.ID, Kind: "revise", IdempotencyKey: "wrong-state",
		BotID: parent.BotID, Provider: "grok", Content: "Edit", Prompt: "edit",
	}); !errors.Is(err, ErrTaskFollowupState) {
		t.Fatalf("wrong state = %v", err)
	}
	latest := revision.Run
	for attempt := 4; attempt <= MaxTaskAttempts; attempt++ {
		if err := instance.UpdateRun(t.Context(), latest.ID, "completed", ""); err != nil {
			t.Fatal(err)
		}
		next, err := instance.CreateTaskFollowup(t.Context(), CreateTaskFollowupInput{
			ParentTaskID: latest.ID, Kind: "revise", IdempotencyKey: fmt.Sprintf("revision-%d", attempt),
			BotID: parent.BotID, Provider: "grok", Content: "Edited brief", Prompt: "edited wrapped prompt",
		})
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		latest = next.Run
	}
	if err := instance.UpdateRun(t.Context(), latest.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.CreateTaskFollowup(t.Context(), CreateTaskFollowupInput{
		ParentTaskID: latest.ID, Kind: "revise", IdempotencyKey: "over-limit",
		BotID: parent.BotID, Provider: "grok", Content: "One more", Prompt: "one more",
	}); !errors.Is(err, ErrTaskAttemptLimit) {
		t.Fatalf("attempt limit = %v", err)
	}
}

func TestTaskFollowupConcurrentDoubleClickCreatesOneRun(t *testing.T) {
	instance, conversation := openTaskStore(t)
	parent := createTaskRun(t, instance, conversation, "Retry me", "private")
	if err := instance.UpdateRun(t.Context(), parent.ID, "stopped", ""); err != nil {
		t.Fatal(err)
	}
	request := CreateTaskFollowupInput{ParentTaskID: parent.ID, Kind: "retry", IdempotencyKey: "same", BotID: parent.BotID, Provider: "grok", Content: "Retry me", Prompt: "fresh"}
	type answer struct {
		result CreateTaskFollowupResult
		err    error
	}
	start := make(chan struct{})
	answers := make(chan answer, 2)
	for range 2 {
		go func() {
			<-start
			result, err := instance.CreateTaskFollowup(context.Background(), request)
			answers <- answer{result, err}
		}()
	}
	close(start)
	first, second := <-answers, <-answers
	if first.err != nil || second.err != nil || first.result.Run.ID != second.result.Run.ID || first.result.Created == second.result.Created {
		t.Fatalf("concurrent answers = %#v %#v", first, second)
	}
	tasks, _, err := instance.ListTasks(t.Context(), TaskFilter{})
	if err != nil || len(tasks) != 2 {
		t.Fatalf("task count = %d, %v", len(tasks), err)
	}
}

func openTaskStore(t *testing.T) (*Store, domain.Conversation) {
	t.Helper()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	return instance, conversation
}

func createTaskRun(t *testing.T, instance *Store, conversation domain.Conversation, title, prompt string) domain.Run {
	t.Helper()
	_, _, run, _, err := instance.CreateMessageWithAttachmentsAndRun(t.Context(), conversation.ID, conversation.BotID, "grok", title, prompt, nil)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestTaskResultAndOriginalTitlePersistAtomically(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "botd.sqlite")
	instance, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	run := createTaskRun(t, instance, conversation, "Original user brief", "SYSTEM_SECRET wrapped prompt")
	if _, err := instance.db.Exec(`CREATE TRIGGER reject_task_result BEFORE UPDATE ON task_results BEGIN SELECT RAISE(ABORT, 'reject result'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.CreateMessageForActiveRun(t.Context(), run.ID, run.ConversationID, "assistant", "must roll back"); err == nil {
		t.Fatal("assistant message persisted despite task result failure")
	}
	messages, err := instance.ListMessages(t.Context(), conversation.ID)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages after rollback = %#v, %v", messages, err)
	}
	if _, err := instance.db.Exec(`DROP TRIGGER reject_task_result`); err != nil {
		t.Fatal(err)
	}
	const exactResult = "# Result\n\nExact bytes: ü\n"
	if _, err := instance.CreateMessageForActiveRun(t.Context(), run.ID, run.ConversationID, "assistant", exactResult); err != nil {
		t.Fatal(err)
	}
	otherRun := createTaskRun(t, instance, conversation, "Other brief", "other private prompt")
	task, result, err := instance.GetTask(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Title != "Original user brief" || result != exactResult || task.ResultPreview != exactResult {
		t.Fatalf("task = %#v, result = %q", task, result)
	}
	if task.Title == run.Prompt {
		t.Fatal("task title exposed the wrapped provider prompt")
	}
	if _, otherResult, err := instance.GetTask(t.Context(), otherRun.ID); err != nil || otherResult != "" {
		t.Fatalf("result crossed run boundary: %q, %v", otherResult, err)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	_, persisted, err := reopened.GetTask(t.Context(), run.ID)
	if err != nil || persisted != exactResult {
		t.Fatalf("persisted result = %q, %v", persisted, err)
	}
}

func TestTaskFiltersPaginationAndPendingApprovalStatus(t *testing.T) {
	instance, conversation := openTaskStore(t)
	first := createTaskRun(t, instance, conversation, "Alpha task", "private alpha prompt")
	second := createTaskRun(t, instance, conversation, "Beta task", "private beta prompt")
	third := createTaskRun(t, instance, conversation, "Gamma task", "private gamma prompt")
	if err := instance.UpdateRun(t.Context(), first.ID, "failed", "provider exploded"); err != nil {
		t.Fatal(err)
	}
	if err := instance.UpdateRun(t.Context(), second.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	approval, err := instance.CreateApproval(t.Context(), second.ID, "grok", "terminal", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	waiting, _, err := instance.ListTasks(t.Context(), TaskFilter{Status: "waiting_approval"})
	if err != nil || len(waiting) != 1 || waiting[0].ID != second.ID || waiting[0].Status != "waiting_approval" {
		t.Fatalf("waiting tasks = %#v, %v", waiting, err)
	}
	detail, _, err := instance.GetTask(t.Context(), second.ID)
	if err != nil || detail.Status != "waiting_approval" {
		t.Fatalf("waiting detail = %#v, %v", detail, err)
	}
	if err := instance.ResolveApproval(t.Context(), approval.ID, "approved", "allow"); err != nil {
		t.Fatal(err)
	}
	detail, _, err = instance.GetTask(t.Context(), second.ID)
	if err != nil || detail.Status != "running" {
		t.Fatalf("resolved detail = %#v, %v", detail, err)
	}
	failed, _, err := instance.GetTask(t.Context(), first.ID)
	if err != nil || failed.Status != "failed" || failed.Error != "provider exploded" {
		t.Fatalf("failed detail = %#v, %v", failed, err)
	}
	queried, _, err := instance.ListTasks(t.Context(), TaskFilter{BotID: conversation.BotID, Query: "beta"})
	if err != nil || len(queried) != 1 || queried[0].ID != second.ID {
		t.Fatalf("filtered tasks = %#v, %v", queried, err)
	}
	page, more, err := instance.ListTasks(t.Context(), TaskFilter{Limit: 2})
	if err != nil || !more || len(page) != 2 || page[0].ID != third.ID || page[1].ID != second.ID {
		t.Fatalf("first page = %#v, more=%v, err=%v", page, more, err)
	}
	next, more, err := instance.ListTasks(t.Context(), TaskFilter{Limit: 2, BeforeCreatedAt: page[1].CreatedAt, BeforeID: page[1].ID})
	if err != nil || more || len(next) != 1 || next[0].ID != first.ID {
		t.Fatalf("next page = %#v, more=%v, err=%v", next, more, err)
	}
}

func TestTaskArtifactsAreImmutableBoundedAndRunIsolated(t *testing.T) {
	instance, conversation := openTaskStore(t)
	run := createTaskRun(t, instance, conversation, "Artifacts", "artifacts")
	other := createTaskRun(t, instance, conversation, "Other", "other")
	original, err := instance.SaveTaskArtifact(t.Context(), run.ID, "report.md", "text/plain", "text", []byte("version one"))
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := instance.SaveTaskArtifact(t.Context(), run.ID, "report.md", "text/plain", "text", []byte("version two"))
	if err != nil || duplicate.ID != original.ID {
		t.Fatalf("duplicate = %#v, %v", duplicate, err)
	}
	_, content, err := instance.GetTaskArtifact(t.Context(), run.ID, original.ID)
	if err != nil || string(content) != "version one" {
		t.Fatalf("immutable content = %q, %v", content, err)
	}
	if _, _, err := instance.GetTaskArtifact(t.Context(), other.ID, original.ID); err == nil {
		t.Fatal("artifact resolved through another run")
	}
	for index := 2; index <= maxTaskArtifacts; index++ {
		if _, err := instance.SaveTaskArtifact(t.Context(), run.ID, fmt.Sprintf("file-%02d.txt", index), "text/plain", "text", []byte("x")); err != nil {
			t.Fatalf("artifact %d: %v", index, err)
		}
	}
	if _, err := instance.SaveTaskArtifact(t.Context(), run.ID, "overflow.txt", "text/plain", "text", []byte("x")); !errors.Is(err, ErrTaskArtifactLimit) {
		t.Fatalf("overflow error = %v", err)
	}
	for _, name := range []string{"../secret", "nested/file", `nested\\file`, ".."} {
		if _, err := instance.SaveTaskArtifact(t.Context(), other.ID, name, "text/plain", "text", []byte("x")); err == nil {
			t.Fatalf("unsafe artifact name %q accepted", name)
		}
	}
	if _, err := instance.SaveTaskArtifact(t.Context(), other.ID, "large.bin", "application/octet-stream", "download", make([]byte, maxTaskArtifactSize+1)); err == nil {
		t.Fatal("oversized artifact accepted")
	}
}

func TestHandoffTaskTitleUsesOriginalBriefInsteadOfWrappedPrompt(t *testing.T) {
	instance, source := openTaskStore(t)
	target, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Reviewer", Title: "Reviews work"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := instance.CreateAgentHandoff(t.Context(), CreateAgentHandoffInput{
		SourceConversationID: source.ID,
		SourceBotID:          source.BotID,
		TargetBotID:          target.Bot.ID,
		TargetConversationID: target.Conversation.ID,
		Content:              "Review the export format",
		TargetProvider:       "grok",
		TargetPrompt:         "PRIVATE WRAPPER AND MEMORY\nReview the export format",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, _, err := instance.GetTask(t.Context(), result.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Title != "Review the export format" || strings.Contains(task.Title, "PRIVATE WRAPPER") {
		t.Fatalf("handoff task title = %q", task.Title)
	}
}
