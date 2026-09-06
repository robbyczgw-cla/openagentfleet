package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

func performTaskRequest(server *Server, method, path, body, idempotencyKey string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response := httptest.NewRecorder()
	server.handleTasks(response, request)
	return response
}

func TestTaskRetryCopiesSelectedInputAttachmentsAndIsIdempotent(t *testing.T) {
	instance, conversation, server := openTasksAPI(t, "")
	uploadDir := filepath.Join(server.HarnessWorkdir, ".openagentfleet", "uploads")
	if err := os.MkdirAll(uploadDir, 0o700); err != nil {
		t.Fatal(err)
	}
	originalPath := filepath.Join(uploadDir, "original.txt")
	if err := os.WriteFile(originalPath, []byte("original bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	original, err := instance.CreateAttachment(t.Context(), domain.Attachment{ConversationID: conversation.ID, Name: "notes.txt", MediaType: "text/plain", Size: 14, StoragePath: originalPath})
	if err != nil {
		t.Fatal(err)
	}
	_, _, parent, _, err := instance.CreateMessageWithAttachmentsAndRun(t.Context(), conversation.ID, conversation.BotID, "grok", "Try this", "private prompt", []string{original.ID})
	if err != nil {
		t.Fatal(err)
	}
	parentWorkdir := t.TempDir()
	if err := instance.SetRunWorkdir(t.Context(), parent.ID, parentWorkdir); err != nil {
		t.Fatal(err)
	}
	if err := instance.UpdateRun(t.Context(), parent.ID, "failed", "failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.SaveTaskArtifact(t.Context(), parent.ID, "old.txt", "text/plain", "text", []byte("old result")); err != nil {
		t.Fatal(err)
	}
	body := `{"agent_id":"` + conversation.BotID + `","attachment_ids":["` + original.ID + `"]}`
	first := performTaskRequest(server, http.MethodPost, "/api/tasks/"+parent.ID+"/retry", body, "click-1")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first retry = %d %s", first.Code, first.Body.String())
	}
	var firstPayload struct {
		Run domain.Run `json:"run"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstPayload); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(originalPath); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.PatchPreferences(t.Context(), []byte(`{"workspace":{"engine":"pi"}}`)); err != nil {
		t.Fatal(err)
	}
	second := performTaskRequest(server, http.MethodPost, "/api/tasks/"+parent.ID+"/retry", body, "click-1")
	var secondPayload struct {
		Run domain.Run `json:"run"`
	}
	if second.Code != http.StatusAccepted || json.Unmarshal(second.Body.Bytes(), &secondPayload) != nil || secondPayload.Run.ID != firstPayload.Run.ID {
		t.Fatalf("second retry = %d %s", second.Code, second.Body.String())
	}
	childInput, err := instance.GetTaskInput(t.Context(), firstPayload.Run.ID)
	if err != nil || len(childInput.Attachments) != 1 || childInput.Attachments[0].ID == original.ID || childInput.Attachments[0].MessageID == original.MessageID {
		t.Fatalf("copied task input = %#v, %v", childInput, err)
	}
	childWorkdir, err := instance.GetRunWorkdir(t.Context(), firstPayload.Run.ID)
	if err != nil || childWorkdir != parentWorkdir {
		t.Fatalf("child workdir = %q, %v", childWorkdir, err)
	}
	copiedBytes, err := os.ReadFile(childInput.Attachments[0].StoragePath)
	if err != nil || string(copiedBytes) != "original bytes" {
		t.Fatalf("copied bytes = %q, %v", copiedBytes, err)
	}
	parentInput, err := instance.GetTaskInput(t.Context(), parent.ID)
	if err != nil || len(parentInput.Attachments) != 1 || parentInput.Attachments[0].ID != original.ID {
		t.Fatalf("parent input changed = %#v, %v", parentInput, err)
	}
	artifacts, err := instance.ListTaskArtifacts(t.Context(), parent.ID)
	if err != nil || len(artifacts) != 1 || artifacts[0].Name != "old.txt" {
		t.Fatalf("parent artifacts changed = %#v, %v", artifacts, err)
	}
	tasks, _, err := instance.ListTasks(t.Context(), store.TaskFilter{})
	if err != nil || len(tasks) != 2 {
		t.Fatalf("double click created %d tasks: %v", len(tasks), err)
	}
	attachments, err := instance.ListAttachments(t.Context(), conversation.ID)
	if err != nil || len(attachments) != 2 {
		t.Fatalf("double click retained %d attachment records: %v", len(attachments), err)
	}
}

func TestDispatchMessageRunsTrustedHookBeforeLifecycleAndStartsFresh(t *testing.T) {
	instance, conversation, server := openTasksAPI(t, "")
	if _, err := instance.UpsertHarnessSession(t.Context(), conversation.ID, "grok", "old-session", server.HarnessWorkdir, "old", "ready"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/messages", nil)
	response := httptest.NewRecorder()
	hookCalls := 0
	server.dispatchMessage(response, request, messageRequest{ConversationID: conversation.ID, Content: "fresh task", Provider: "grok"}, messageDispatchOptions{
		FreshSession: true,
		AfterRunCreated: func(ctx context.Context, run domain.Run) error {
			hookCalls++
			stored, err := instance.GetRun(ctx, run.ID)
			if err != nil {
				return err
			}
			if stored.Status != "queued" || run.SessionID != "" {
				return fmt.Errorf("hook run status=%s session=%q", stored.Status, run.SessionID)
			}
			return nil
		},
	})
	if response.Code != http.StatusAccepted || hookCalls != 1 {
		t.Fatalf("dispatch = %d %s, hook calls=%d", response.Code, response.Body.String(), hookCalls)
	}
}

func openTasksAPI(t *testing.T, token string) (*store.Store, domain.Conversation, *Server) {
	t.Helper()
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
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
	return instance, conversation, &Server{Store: instance, HarnessWorkdir: t.TempDir(), RemoteToken: token}
}

func createAPITask(t *testing.T, instance *store.Store, conversation domain.Conversation, title, prompt string) domain.Run {
	t.Helper()
	_, _, run, _, err := instance.CreateMessageWithAttachmentsAndRun(t.Context(), conversation.ID, conversation.BotID, "grok", title, prompt, nil)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestTasksAPIAuthDetailIsolationAndCursor(t *testing.T) {
	instance, conversation, server := openTasksAPI(t, "controller-token")
	first := createAPITask(t, instance, conversation, "First brief", "PRIVATE_SYSTEM_PROMPT_ONE")
	second := createAPITask(t, instance, conversation, "Second brief", "PRIVATE_SYSTEM_PROMPT_TWO")
	third := createAPITask(t, instance, conversation, "Third brief", "PRIVATE_SYSTEM_PROMPT_THREE")
	if err := instance.UpdateRun(t.Context(), first.ID, "failed", "provider exploded"); err != nil {
		t.Fatal(err)
	}
	const exactResult = "final result\nwith whitespace\n"
	if _, err := instance.CreateMessageForActiveRun(t.Context(), second.ID, second.ConversationID, "assistant", exactResult); err != nil {
		t.Fatal(err)
	}
	artifact, err := instance.SaveTaskArtifact(t.Context(), second.ID, "result.txt", "text/plain; charset=utf-8", "text", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	unauthorized := performRequest(handler, http.MethodGet, "/api/tasks", "", "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	page := performRequest(handler, http.MethodGet, "/api/tasks?limit=1", "", "controller-token")
	if page.Code != http.StatusOK {
		t.Fatalf("first page = %d %s", page.Code, page.Body.String())
	}
	var firstPage struct {
		Items      []domain.TaskSummary `json:"items"`
		NextCursor string               `json:"next_cursor"`
	}
	if err := json.Unmarshal(page.Body.Bytes(), &firstPage); err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Items) != 1 || firstPage.Items[0].ID != third.ID || firstPage.NextCursor == "" {
		t.Fatalf("first page payload = %#v", firstPage)
	}
	next := performRequest(handler, http.MethodGet, "/api/tasks?limit=1&cursor="+firstPage.NextCursor, "", "controller-token")
	var secondPage struct {
		Items      []domain.TaskSummary `json:"items"`
		NextCursor string               `json:"next_cursor"`
	}
	if next.Code != http.StatusOK || json.Unmarshal(next.Body.Bytes(), &secondPage) != nil || len(secondPage.Items) != 1 || secondPage.Items[0].ID != second.ID {
		t.Fatalf("second page = %d %s", next.Code, next.Body.String())
	}
	if invalid := performRequest(handler, http.MethodGet, "/api/tasks?cursor=broken", "", "controller-token"); invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor = %d %s", invalid.Code, invalid.Body.String())
	}
	filtered := performRequest(handler, http.MethodGet, "/api/tasks?q=second&status=queued&bot_id="+conversation.BotID, "", "controller-token")
	if filtered.Code != http.StatusOK || !strings.Contains(filtered.Body.String(), second.ID) || strings.Contains(filtered.Body.String(), first.ID) {
		t.Fatalf("filtered tasks = %d %s", filtered.Code, filtered.Body.String())
	}
	detail := performRequest(handler, http.MethodGet, "/api/tasks/"+second.ID, "", "controller-token")
	var detailPayload struct {
		Task      domain.TaskSummary `json:"task"`
		Result    string             `json:"result"`
		Artifacts []domain.Artifact  `json:"artifacts"`
	}
	if detail.Code != http.StatusOK || json.Unmarshal(detail.Body.Bytes(), &detailPayload) != nil || detailPayload.Result != exactResult || len(detailPayload.Artifacts) != 1 || detailPayload.Artifacts[0].ID != artifact.ID {
		t.Fatalf("task detail = %d %s", detail.Code, detail.Body.String())
	}
	if strings.Contains(detail.Body.String(), "PRIVATE_SYSTEM_PROMPT") {
		t.Fatalf("task detail leaked provider prompt: %s", detail.Body.String())
	}
	failedDetail := performRequest(handler, http.MethodGet, "/api/tasks/"+first.ID, "", "controller-token")
	var failedPayload struct {
		Task domain.TaskSummary `json:"task"`
	}
	if failedDetail.Code != http.StatusOK || json.Unmarshal(failedDetail.Body.Bytes(), &failedPayload) != nil || failedPayload.Task.Error != "provider exploded" || failedPayload.Task.Status != "failed" {
		t.Fatalf("failed task detail = %d %s", failedDetail.Code, failedDetail.Body.String())
	}
	content := performRequest(handler, http.MethodGet, "/api/tasks/"+second.ID+"/artifacts/"+artifact.ID+"/content", "", "controller-token")
	if content.Code != http.StatusOK || content.Body.String() != "snapshot" || !strings.HasPrefix(content.Header().Get("Content-Disposition"), "inline") || content.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("artifact content = %d headers=%v body=%q", content.Code, content.Header(), content.Body.String())
	}
	crossRun := performRequest(handler, http.MethodGet, "/api/tasks/"+first.ID+"/artifacts/"+artifact.ID+"/download", "", "controller-token")
	if crossRun.Code != http.StatusNotFound {
		t.Fatalf("cross-run artifact = %d %s", crossRun.Code, crossRun.Body.String())
	}
	traversal := performRequest(handler, http.MethodGet, "/api/tasks/"+second.ID+"/artifacts/../"+artifact.ID+"/content", "", "controller-token")
	if traversal.Code != http.StatusNotFound {
		t.Fatalf("artifact traversal = %d %s", traversal.Code, traversal.Body.String())
	}
}

func TestTaskStatusUsesPendingApprovalWithoutChangingRun(t *testing.T) {
	instance, conversation, server := openTasksAPI(t, "")
	run := createAPITask(t, instance, conversation, "Needs approval", "safe prompt")
	if err := instance.UpdateRun(t.Context(), run.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.CreateApproval(t.Context(), run.ID, "grok", "terminal", `{}`); err != nil {
		t.Fatal(err)
	}
	response := performRequest(server.Handler(), http.MethodGet, "/api/tasks?status=waiting_approval", "", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), run.ID) || !strings.Contains(response.Body.String(), `"status":"waiting_approval"`) {
		t.Fatalf("needs-me response = %d %s", response.Code, response.Body.String())
	}
	stored, err := instance.GetRun(t.Context(), run.ID)
	if err != nil || stored.Status != "running" {
		t.Fatalf("run lifecycle changed = %#v, %v", stored, err)
	}
}

func TestCaptureTaskArtifactsRejectsTraversalAndSymlinksAndKeepsSnapshots(t *testing.T) {
	instance, conversation, server := openTasksAPI(t, "")
	run := createAPITask(t, instance, conversation, "Capture files", "capture")
	outputs := filepath.Join(server.HarnessWorkdir, "outputs")
	for _, directory := range []string{filepath.Join(outputs, "a"), filepath.Join(outputs, "b")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	firstPath := filepath.Join(outputs, "a", "report.md")
	secondPath := filepath.Join(outputs, "b", "report.md")
	if err := os.WriteFile(firstPath, []byte("first snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("second snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(server.HarnessWorkdir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("do not capture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretPath, filepath.Join(outputs, "leak.txt")); err != nil {
		t.Fatal(err)
	}
	answer := strings.Join([]string{
		"[first](outputs/a/report.md)",
		"[duplicate](/workspace/outputs/a/report.md)",
		"[second](/workspace/outputs/b/report.md)",
		"[traversal](outputs/../secret.txt)",
		"[symlink](outputs/leak.txt)",
	}, "\n")
	server.captureTaskArtifacts(run, answer)
	artifacts, err := instance.ListTaskArtifacts(t.Context(), run.ID)
	if err != nil || len(artifacts) != 2 {
		t.Fatalf("captured artifacts = %#v, %v", artifacts, err)
	}
	names := map[string]bool{}
	contents := map[string]string{}
	for _, artifact := range artifacts {
		names[artifact.Name] = true
		_, data, err := instance.GetTaskArtifact(t.Context(), run.ID, artifact.ID)
		if err != nil {
			t.Fatal(err)
		}
		contents[artifact.Name] = string(data)
	}
	if !names["report.md"] || !names["report (2).md"] || contents["report.md"] != "first snapshot" || contents["report (2).md"] != "second snapshot" {
		t.Fatalf("deduplicated snapshots = names=%v contents=%v", names, contents)
	}
	if err := os.WriteFile(firstPath, []byte("mutated later"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, immutable, err := instance.GetTaskArtifact(t.Context(), run.ID, artifacts[0].ID)
	if err != nil || string(immutable) != "first snapshot" {
		t.Fatalf("immutable artifact = %q, %v", immutable, err)
	}
}

func TestTaskDeliverablesAndCaptureUseRunWorkdir(t *testing.T) {
	instance, conversation, server := openTasksAPI(t, "")
	run := createAPITask(t, instance, conversation, "Scoped output", "capture")
	runWorkdir := t.TempDir()
	if err := instance.SetRunWorkdir(t.Context(), run.ID, runWorkdir); err != nil {
		t.Fatal(err)
	}
	server.prepareTaskDeliverables(run.ID)
	runOutputDir := filepath.Join(runWorkdir, "outputs", run.ID)
	if info, err := os.Stat(runOutputDir); err != nil || !info.IsDir() {
		t.Fatalf("run output directory = %v, %v", info, err)
	}
	report := filepath.Join(runOutputDir, "report.txt")
	if err := os.WriteFile(report, []byte("scoped"), 0o600); err != nil {
		t.Fatal(err)
	}
	server.captureTaskArtifacts(run, "[report]("+report+")")
	artifacts, err := instance.ListTaskArtifacts(t.Context(), run.ID)
	if err != nil || len(artifacts) != 1 || artifacts[0].Name != "report.txt" {
		t.Fatalf("scoped artifacts = %#v, %v", artifacts, err)
	}
}
