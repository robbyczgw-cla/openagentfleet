package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

type groupHarnessExecutor struct {
	mu    sync.Mutex
	calls int
}

func (executor *groupHarnessExecutor) RunWithOptions(_ context.Context, _, _, _ string, _ harness.RunOptions) (string, error) {
	executor.mu.Lock()
	executor.calls++
	executor.mu.Unlock()
	return "Engineer checked the repo.", nil
}

func (executor *groupHarnessExecutor) callCount() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.calls
}

func TestGroupAPIRoutesMentionsWithoutCanonicalMemory(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	source, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	engineer, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Engineer", Title: "Code", Description: "Repo."})
	if err != nil {
		t.Fatal(err)
	}
	researcher, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Researcher", Title: "Research", Description: "Notes."})
	if err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: instance}).Handler()

	tooSmall := performRequest(handler, http.MethodPost, "/api/groups", `{"title":"Launch","agent_ids":["`+engineer.Bot.ID+`"]}`, "")
	if tooSmall.Code != http.StatusBadRequest {
		t.Fatalf("one agent = %d %s", tooSmall.Code, tooSmall.Body.String())
	}

	created := performRequest(handler, http.MethodPost, "/api/groups", `{"title":"Product Launch","agent_ids":["`+engineer.Bot.ID+`","`+researcher.Bot.ID+`"]}`, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create group = %d %s", created.Code, created.Body.String())
	}
	var groupPayload struct {
		Group domain.Group `json:"group"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &groupPayload); err != nil {
		t.Fatal(err)
	}

	posted := performRequest(handler, http.MethodPost, "/api/groups/"+groupPayload.Group.ID+"/messages",
		`{"content":"@Researcher investigate the API and @Engineer check the repo.","mention_bot_ids":["`+researcher.Bot.ID+`","`+engineer.Bot.ID+`"]}`, "")
	if posted.Code != http.StatusAccepted {
		t.Fatalf("group message = %d %s", posted.Code, posted.Body.String())
	}
	var messagePayload struct {
		Message domain.GroupMessage `json:"message"`
		Runs    []domain.GroupRun   `json:"runs"`
	}
	if err := json.Unmarshal(posted.Body.Bytes(), &messagePayload); err != nil {
		t.Fatal(err)
	}
	if messagePayload.Message.AuthorBotID != "" || len(messagePayload.Runs) != 2 {
		t.Fatalf("mention routing = %#v", messagePayload)
	}
	bots := map[string]bool{}
	for _, run := range messagePayload.Runs {
		bots[run.BotID] = true
		if run.BotID == source.BotID {
			t.Fatal("unmentioned seed agent received a group run")
		}
	}
	if !bots[engineer.Bot.ID] || !bots[researcher.Bot.ID] {
		t.Fatalf("expected both mentioned agents, got %#v", messagePayload.Runs)
	}

	canonical, err := instance.ListMessages(t.Context(), engineer.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != 0 {
		t.Fatalf("group message leaked into canonical chat: %#v", canonical)
	}
	reply, err := instance.CreateGroupAgentReply(t.Context(), groupPayload.Group.ID, engineer.Bot.ID, "These files need modifications.", nil)
	if err != nil || reply.AuthorBotID != engineer.Bot.ID {
		t.Fatalf("agent reply = %#v, %v", reply, err)
	}
}

func TestGroupAPIExecutesMentionedRunsWithoutCanonicalMemory(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	engineer, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Engineer", Title: "Code", Description: "Repo."})
	if err != nil {
		t.Fatal(err)
	}
	researcher, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Researcher", Title: "Research", Description: "Notes."})
	if err != nil {
		t.Fatal(err)
	}
	executor := &groupHarnessExecutor{}
	handler := (&Server{
		Store: instance, AllowHarnessExecution: true, runExecutorOverride: executor, HarnessWorkdir: t.TempDir(),
	}).Handler()

	created := performRequest(handler, http.MethodPost, "/api/groups", `{"title":"Product Launch","agent_ids":["`+engineer.Bot.ID+`","`+researcher.Bot.ID+`"]}`, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create group = %d %s", created.Code, created.Body.String())
	}
	var groupPayload struct {
		Group domain.Group `json:"group"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &groupPayload); err != nil {
		t.Fatal(err)
	}

	posted := performRequest(handler, http.MethodPost, "/api/groups/"+groupPayload.Group.ID+"/messages",
		`{"content":"@Researcher investigate the API and @Engineer check the repo.","mention_bot_ids":["`+researcher.Bot.ID+`","`+engineer.Bot.ID+`"]}`, "")
	if posted.Code != http.StatusAccepted {
		t.Fatalf("group message = %d %s", posted.Code, posted.Body.String())
	}

	deadline := time.Now().Add(3 * time.Second)
	var listed struct {
		Messages []domain.GroupMessage `json:"messages"`
		Runs     []domain.GroupRun     `json:"runs"`
	}
	for time.Now().Before(deadline) {
		response := performRequest(handler, http.MethodGet, "/api/groups/"+groupPayload.Group.ID+"/messages", "", "")
		if response.Code != http.StatusOK {
			t.Fatalf("list group messages = %d %s", response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
			t.Fatal(err)
		}
		completed := 0
		for _, run := range listed.Runs {
			if run.Status == domain.GroupRunStatusCompleted {
				completed++
			}
		}
		assistants := 0
		authors := map[string]bool{}
		for _, message := range listed.Messages {
			if message.Role == "assistant" && message.AuthorBotID != "" {
				assistants++
				authors[message.AuthorBotID] = true
			}
		}
		if completed == 2 && assistants == 2 && authors[engineer.Bot.ID] && authors[researcher.Bot.ID] {
			break
		}
		time.Sleep(10 * time.Millisecond)
		listed = struct {
			Messages []domain.GroupMessage `json:"messages"`
			Runs     []domain.GroupRun     `json:"runs"`
		}{}
	}
	if executor.callCount() != 2 {
		t.Fatalf("harness calls = %d, want 2", executor.callCount())
	}
	completed := 0
	for _, run := range listed.Runs {
		if run.Status != domain.GroupRunStatusCompleted {
			t.Fatalf("group run not completed: %#v", listed.Runs)
		}
		completed++
	}
	if completed != 2 {
		t.Fatalf("completed runs = %#v", listed.Runs)
	}
	authors := map[string]bool{}
	assistants := 0
	for _, message := range listed.Messages {
		if message.Role != "assistant" {
			continue
		}
		assistants++
		authors[message.AuthorBotID] = true
	}
	if assistants != 2 || !authors[engineer.Bot.ID] || !authors[researcher.Bot.ID] {
		t.Fatalf("assistant replies = %#v", listed.Messages)
	}

	for _, conversationID := range []string{engineer.Conversation.ID, researcher.Conversation.ID} {
		canonical, err := instance.ListMessages(t.Context(), conversationID)
		if err != nil {
			t.Fatal(err)
		}
		if len(canonical) != 0 {
			t.Fatalf("group execution leaked into canonical chat %s: %#v", conversationID, canonical)
		}
	}
}
