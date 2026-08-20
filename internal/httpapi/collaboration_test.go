package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/collaborationmcp"
	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/events"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

func TestCollaborationToolsRequireRunCapabilityAndStayOnTargetAgent(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	sourceConv, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	source, _, err := (&Server{Store: instance}).agentForBot(t.Context(), sourceConv.BotID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.PatchAgent(t.Context(), source.Bot.ID, domain.AgentProfileUpdate{}, func(existing domain.AgentMetadata) (domain.AgentMetadata, error) {
		existing.Collaboration = &domain.AgentCollaboration{Enabled: true}
		return domain.NormalizeAgentMetadata(existing)
	}); err != nil {
		t.Fatal(err)
	}
	reviewer, err := instance.CreateAgent(t.Context(), domain.AgentDraft{
		Name: "Reviewer", Title: "Reviews handed-off work", Description: "Second visible agent.",
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceRun, _, err := instance.CreateRunWithQueuedEvent(t.Context(), sourceConv.ID, sourceConv.BotID, "grok", "coordinate")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: instance, Broker: events.New(), HarnessWorkdir: t.TempDir(), RemoteToken: "controller"}
	handler := server.Handler()

	unauth := performRequest(handler, http.MethodGet, "/api/collaboration/agents", "", "controller")
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("missing collab capability = %d %s", unauth.Code, unauth.Body.String())
	}

	server.bindCollabCapability("collab-token", sourceRun.ID)
	listed := performCollabRequest(handler, http.MethodGet, "/api/collaboration/agents", "", "controller", sourceRun.ID, "collab-token")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), reviewer.Bot.ID) || strings.Contains(listed.Body.String(), sourceConv.BotID) {
		t.Fatalf("list agents = %d %s", listed.Code, listed.Body.String())
	}

	disabled := performCollabRequest(handler, http.MethodPost, "/api/collaboration/delegate", `{"agent_id":"`+reviewer.Bot.ID+`","task":"inspect the API"}`, "controller", sourceRun.ID, "collab-token")
	if disabled.Code != http.StatusAccepted {
		t.Fatalf("delegate = %d %s", disabled.Code, disabled.Body.String())
	}
	var payload struct {
		TaskID  string         `json:"task_id"`
		Run     domain.Run     `json:"run"`
		Handoff domain.Handoff `json:"handoff"`
	}
	if err := json.Unmarshal(disabled.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Run.BotID != reviewer.Bot.ID || payload.Handoff.SourceBotID != sourceConv.BotID {
		t.Fatalf("delegate isolation = %#v", payload)
	}
	if !strings.Contains(payload.Run.Prompt, "Do not assume the sender's computer") {
		t.Fatalf("target prompt missing isolation: %s", payload.Run.Prompt)
	}
	if payload.Handoff.Mode != domain.HandoffModeDelegate || payload.Handoff.Depth != 1 {
		t.Fatalf("handoff bounds = %#v", payload.Handoff)
	}

	ping := performCollabRequest(handler, http.MethodPost, "/api/collaboration/delegate", `{"agent_id":"`+sourceConv.BotID+`","task":"bounce back"}`, "controller", payload.Run.ID, "collab-token")
	if ping.Code != http.StatusUnauthorized {
		t.Fatalf("target run must not reuse source collab token = %d %s", ping.Code, ping.Body.String())
	}

	reviewerRun, err := instance.GetRun(t.Context(), payload.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	server.bindCollabCapability("reviewer-token", reviewerRun.ID)
	loop := performCollabRequest(handler, http.MethodPost, "/api/collaboration/delegate", `{"agent_id":"`+sourceConv.BotID+`","task":"bounce back"}`, "controller", reviewerRun.ID, "reviewer-token")
	if loop.Code != http.StatusBadRequest || !strings.Contains(loop.Body.String(), "collaboration is not enabled") {
		t.Fatalf("reviewer without collaboration = %d %s", loop.Code, loop.Body.String())
	}

	if _, err := instance.PatchAgent(t.Context(), reviewer.Bot.ID, domain.AgentProfileUpdate{}, func(existing domain.AgentMetadata) (domain.AgentMetadata, error) {
		existing.Collaboration = &domain.AgentCollaboration{Enabled: true}
		return domain.NormalizeAgentMetadata(existing)
	}); err != nil {
		t.Fatal(err)
	}
	loop = performCollabRequest(handler, http.MethodPost, "/api/collaboration/delegate", `{"agent_id":"`+sourceConv.BotID+`","task":"bounce back"}`, "controller", reviewerRun.ID, "reviewer-token")
	if loop.Code != http.StatusBadRequest || !(strings.Contains(loop.Body.String(), "ping-pong") || strings.Contains(loop.Body.String(), "cycle")) {
		t.Fatalf("ping-pong = %d %s", loop.Code, loop.Body.String())
	}

	if _, err := instance.CreateMessageForActiveRun(t.Context(), reviewerRun.ID, reviewerRun.ConversationID, "assistant", "API breaks in /v2/widgets."); err != nil {
		t.Fatal(err)
	}
	server.finishCollaborationHandoff(reviewerRun, "completed", "")
	sourceMessages, err := instance.ListMessages(t.Context(), sourceConv.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundResult := false
	for _, message := range sourceMessages {
		if message.Kind == domain.MessageKindHandoffResult && strings.Contains(message.Content, "API breaks") && message.AuthorBotID == reviewer.Bot.ID {
			foundResult = true
		}
	}
	if !foundResult {
		t.Fatalf("source result missing: %#v", sourceMessages)
	}

	status := performCollabRequest(handler, http.MethodGet, "/api/collaboration/tasks/"+payload.TaskID, "", "controller", sourceRun.ID, "collab-token")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), "completed") {
		t.Fatalf("task status = %d %s", status.Code, status.Body.String())
	}
}

func TestCollaborationAllowlistAndActiveCap(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	sourceConv, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Engineer", Title: "Code", Description: "Repo work."})
	if err != nil {
		t.Fatal(err)
	}
	denied, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Researcher", Title: "Research", Description: "Notes."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.PatchAgent(t.Context(), sourceConv.BotID, domain.AgentProfileUpdate{}, func(existing domain.AgentMetadata) (domain.AgentMetadata, error) {
		existing.Collaboration = &domain.AgentCollaboration{
			Enabled:            true,
			AllowAgentIDs:      []string{allowed.Bot.ID},
			MaxActivePeerTasks: 1,
		}
		return domain.NormalizeAgentMetadata(existing)
	}); err != nil {
		t.Fatal(err)
	}
	sourceRun, _, err := instance.CreateRunWithQueuedEvent(t.Context(), sourceConv.ID, sourceConv.BotID, "grok", "coordinate")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: instance, HarnessWorkdir: t.TempDir(), RemoteToken: "controller"}
	handler := server.Handler()
	server.bindCollabCapability("collab-token", sourceRun.ID)

	blocked := performCollabRequest(handler, http.MethodPost, "/api/collaboration/message", `{"agent_id":"`+denied.Bot.ID+`","content":"hello"}`, "controller", sourceRun.ID, "collab-token")
	if blocked.Code != http.StatusBadRequest || !strings.Contains(blocked.Body.String(), "not allowed") {
		t.Fatalf("allowlist = %d %s", blocked.Code, blocked.Body.String())
	}
	first := performCollabRequest(handler, http.MethodPost, "/api/collaboration/delegate", `{"agent_id":"`+allowed.Bot.ID+`","task":"inspect repo"}`, "controller", sourceRun.ID, "collab-token")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first delegate = %d %s", first.Code, first.Body.String())
	}
	second := performCollabRequest(handler, http.MethodPost, "/api/collaboration/delegate", `{"agent_id":"`+allowed.Bot.ID+`","task":"again"}`, "controller", sourceRun.ID, "collab-token")
	if second.Code != http.StatusBadRequest || !strings.Contains(second.Body.String(), "too many active peer tasks") {
		t.Fatalf("active cap = %d %s", second.Code, second.Body.String())
	}
}

func performCollabRequest(handler http.Handler, method, path, body, token, runID, runToken string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set(collaborationmcp.RunIDHeader, runID)
	request.Header.Set(collaborationmcp.RunTokenHeader, runToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
