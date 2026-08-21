package httpapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

func TestReviewQueueEmpty(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "review-empty.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: instance}).Handler()
	response := performRequest(handler, http.MethodGet, "/api/review", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/review = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Items []reviewItem `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Items == nil {
		t.Fatal("items should be an empty list, not omitted")
	}
	if len(payload.Items) != 0 {
		t.Fatalf("empty review = %#v", payload.Items)
	}
}

func TestReviewQueuePendingApprovalAndCompletedRun(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "review-queue.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	reviewer, err := instance.CreateAgent(ctx, domain.AgentDraft{Name: "Cami", Title: "Reviewer", Description: ""})
	if err != nil {
		t.Fatal(err)
	}
	seedConversation, err := instance.GetConversation(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := instance.CreateRun(ctx, seedConversation.ID, seedConversation.BotID, "grok", "SECRET_PROMPT dump")
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.UpdateRun(ctx, waiting.ID, "waiting_for_approval", ""); err != nil {
		t.Fatal(err)
	}
	approval, err := instance.CreateApproval(ctx, waiting.ID, "grok", "Run host command", `{
		"options":[{"optionId":"allow_once","name":"Allow once","kind":"allow_once"}],
		"tool_call":{"command":"PRIVATE_COMMAND_SENTINEL","title":"hidden tool"}
	}`)
	if err != nil {
		t.Fatal(err)
	}

	finished, err := instance.CreateRun(ctx, reviewer.Conversation.ID, reviewer.Bot.ID, "grok", "SECRET_PROMPT dump")
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.UpdateRun(ctx, finished.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.AppendRunEvent(ctx, finished.ID, "session.opened", `{"native_session_id":"sess-secret","workdir":"/tmp/secret-work"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.AppendRunEvent(ctx, finished.ID, "provider.output", `{"stream":"stdout","type":"file","text":"{\"title\":\"Edit src/main.go\",\"command\":\"PRIVATE_COMMAND_SENTINEL\"}"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.AppendRunEvent(ctx, finished.ID, "provider.output", `{"stream":"stdout","type":"tool_call","text":"{\"title\":\"later tool\"}"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.CreateMessage(ctx, reviewer.Conversation.ID, "assistant", "I edited main.go after the file change."); err != nil {
		t.Fatal(err)
	}

	handler := (&Server{Store: instance}).Handler()
	response := performRequest(handler, http.MethodGet, "/api/review", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/review = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, leak := range []string{
		"SECRET_PROMPT",
		"PRIVATE_COMMAND_SENTINEL",
		"sess-secret",
		"/tmp/secret-work",
		"native_session_id",
		"hidden tool",
	} {
		if strings.Contains(body, leak) {
			t.Fatalf("review leaked %q: %s", leak, body)
		}
	}
	var payload struct {
		Items []reviewItem `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 2 {
		t.Fatalf("review items = %#v", payload.Items)
	}
	first, second := payload.Items[0], payload.Items[1]
	if first.Kind != "approval" || first.ID != approval.ID || first.Status != "pending" || first.Action != "Run host command" {
		t.Fatalf("approval item = %#v", first)
	}
	if first.BotID != seedConversation.BotID || first.RunID != waiting.ID || first.ConversationID != seedConversation.ID {
		t.Fatalf("approval wiring = %#v", first)
	}
	if len(first.Options) != 1 || first.Options[0].OptionID != "allow_once" {
		t.Fatalf("approval options = %#v", first.Options)
	}
	if second.Kind != "run" || second.Status != "completed" || second.RunID != finished.ID || second.BotName != "Cami" {
		t.Fatalf("run item = %#v", second)
	}
	if second.Summary != "Edit src/main.go" {
		t.Fatalf("run summary = %q, want file title", second.Summary)
	}
}

func TestReviewSummaryFallsBackToStatusAndAssistant(t *testing.T) {
	got := summarizeRunWork(nil, "failed", "Could not finish the notes.")
	if got != "failed · Could not finish the notes." {
		t.Fatalf("fallback summary = %q", got)
	}
	got = summarizeRunWork([]domain.RunEvent{{
		Type: "provider.output",
		Data: `{"type":"thought","text":"secret chain of thought"}`,
	}, {
		Type: "run.completed",
		Data: `{"status":"completed"}`,
	}}, "completed", "All done.")
	if got != "completed · All done." {
		t.Fatalf("ignored thought summary = %q", got)
	}
	got = summarizeRunWork([]domain.RunEvent{{
		Type: "provider.output",
		Data: `{"type":"tool_call","text":"{\"title\":\"later tool\"}"}`,
	}}, "completed", "ignored")
	if got != "tool_call" {
		t.Fatalf("work type summary = %q", got)
	}
}
