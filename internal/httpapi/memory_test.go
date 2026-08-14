package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

func TestMemoryAPIIsReviewableAndSnapshotsOnlyActiveContext(t *testing.T) {
	instance, conversation, handler := openMemoryAPIServer(t)
	ctx := t.Context()

	create := func(body string) domain.BotMemory {
		t.Helper()
		response := memoryAPIRequest(handler, http.MethodPost, "/api/memories", body)
		if response.Code != http.StatusCreated {
			t.Fatalf("create memory status = %d, body = %s", response.Code, response.Body.String())
		}
		var memory domain.BotMemory
		if err := json.NewDecoder(response.Body).Decode(&memory); err != nil {
			t.Fatal(err)
		}
		return memory
	}

	active := create(`{"bot_id":"` + conversation.BotID + `","category":"preference","content":"Keep release notes concise.","priority":5,"expires_at":""}`)
	if active.Source != domain.MemorySourceUser || active.Status != domain.MemoryStatusApproved {
		t.Fatalf("created memory provenance = %#v", active)
	}
	if response := memoryAPIRequest(handler, http.MethodPost, "/api/memories", `{"bot_id":"`+conversation.BotID+`","category":"fact","content":"password is hunter2","priority":2}`); response.Code != http.StatusBadRequest {
		t.Fatalf("secret-shaped memory status = %d, body = %s", response.Code, response.Body.String())
	}

	archived, err := instance.CreateBotMemory(ctx, conversation.BotID, domain.BotMemoryDraft{
		Category: domain.MemoryCategoryFact, Status: domain.MemoryStatusApproved, Source: domain.MemorySourceUser,
		Content: "This archived note must not reach a run.", Priority: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.ArchiveBotMemory(ctx, conversation.BotID, archived.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.CreateBotMemory(ctx, conversation.BotID, domain.BotMemoryDraft{
		Category: domain.MemoryCategoryFact, Status: domain.MemoryStatusApproved, Source: domain.MemorySourceUser,
		Content: "This expired note must not reach a run.", Priority: 5,
		ExpiresAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}

	listResponse := memoryAPIRequest(handler, http.MethodGet, "/api/memories?bot_id="+conversation.BotID, "")
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list memory status = %d, body = %s", listResponse.Code, listResponse.Body.String())
	}
	var listed struct {
		Memories []domain.BotMemory `json:"memories"`
	}
	if err := json.NewDecoder(listResponse.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Memories) != 3 {
		t.Fatalf("reviewable memory list = %#v", listed.Memories)
	}

	patchResponse := memoryAPIRequest(handler, http.MethodPatch, "/api/memories/"+active.ID+"?bot_id="+conversation.BotID, `{"content":"Use short release notes.","priority":4,"expires_at":null}`)
	if patchResponse.Code != http.StatusOK {
		t.Fatalf("patch memory status = %d, body = %s", patchResponse.Code, patchResponse.Body.String())
	}
	var patched domain.BotMemory
	if err := json.NewDecoder(patchResponse.Body).Decode(&patched); err != nil {
		t.Fatal(err)
	}
	if patched.Content != "Use short release notes." || patched.Priority != 4 || patched.ExpiresAt != "" {
		t.Fatalf("patched memory = %#v", patched)
	}

	messageResponse := memoryAPIRequest(handler, http.MethodPost, "/api/messages", `{"conversation_id":"`+conversation.ID+`","content":"Prepare the release notes.","provider":"grok"}`)
	if messageResponse.Code != http.StatusAccepted {
		t.Fatalf("create message status = %d, body = %s", messageResponse.Code, messageResponse.Body.String())
	}
	var envelope struct {
		Run domain.Run `json:"run"`
	}
	if err := json.NewDecoder(messageResponse.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"This archived note", "This expired note"} {
		if strings.Contains(envelope.Run.Prompt, forbidden) {
			t.Fatalf("inactive memory reached run prompt: %s", forbidden)
		}
	}
	if !strings.Contains(envelope.Run.Prompt, "Use short release notes.") || !strings.Contains(envelope.Run.Prompt, "Current user task:\nPrepare the release notes.") {
		t.Fatalf("run did not include a bounded memory snapshot: %q", envelope.Run.Prompt)
	}
	messages, err := instance.ListMessages(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := messages[len(messages)-1].Content; got != "Prepare the release notes." {
		t.Fatalf("stored user message unexpectedly contains memory context: %q", got)
	}

	bootstrapResult := memoryAPIRequest(handler, http.MethodGet, "/api/bootstrap?conversation_id="+conversation.ID, "")
	if bootstrapResult.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d, body = %s", bootstrapResult.Code, bootstrapResult.Body.String())
	}
	var bootstrap bootstrapResponse
	if err := json.NewDecoder(bootstrapResult.Body).Decode(&bootstrap); err != nil {
		t.Fatal(err)
	}
	if len(bootstrap.Memories) != 3 {
		t.Fatalf("bootstrap memories = %#v", bootstrap.Memories)
	}
}

func TestMemoryAPIDeleteIsExplicitAndScoped(t *testing.T) {
	_, conversation, handler := openMemoryAPIServer(t)
	createdResponse := memoryAPIRequest(handler, http.MethodPost, "/api/memories", `{"bot_id":"`+conversation.BotID+`","category":"fact","content":"Delete this reviewed note.","priority":2}`)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created domain.BotMemory
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	if response := memoryAPIRequest(handler, http.MethodDelete, "/api/memories/"+created.ID, ""); response.Code != http.StatusBadRequest {
		t.Fatalf("delete without bot scope = %d, body = %s", response.Code, response.Body.String())
	}
	if response := memoryAPIRequest(handler, http.MethodDelete, "/api/memories/"+created.ID+"?bot_id="+conversation.BotID, "unexpected"); response.Code != http.StatusBadRequest {
		t.Fatalf("delete with body = %d, body = %s", response.Code, response.Body.String())
	}
	if response := memoryAPIRequest(handler, http.MethodDelete, "/api/memories/"+created.ID+"?bot_id="+conversation.BotID, ""); response.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", response.Code, response.Body.String())
	}
	if response := memoryAPIRequest(handler, http.MethodPatch, "/api/memories/"+created.ID+"?bot_id="+conversation.BotID, `{"status":"archived"}`); response.Code != http.StatusNotFound {
		t.Fatalf("deleted memory patch status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestPromptWithBotMemoryPreservesCurrentTaskWithoutContext(t *testing.T) {
	if got := promptWithBotMemory("current task", nil); got != "current task" {
		t.Fatalf("prompt without memory = %q", got)
	}
}

func openMemoryAPIServer(t *testing.T) (*store.Store, domain.Conversation, http.Handler) {
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
	return instance, conversation, (&Server{Store: instance, HarnessWorkdir: t.TempDir()}).Handler()
}

func memoryAPIRequest(handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
