package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/events"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

func TestPiLeadMessagePermissionEdges(t *testing.T) {
	t.Run("auto", func(t *testing.T) {
		_, handler, conversationID, _ := openPiLeadMessageFixture(t)
		auto := performRequest(handler, http.MethodPost, "/api/messages", `{"conversation_id":"`+conversationID+`","content":"auto","provider":"pi","permission_mode":"auto"}`, "")
		if auto.Code != http.StatusBadRequest || !strings.Contains(auto.Body.String(), "auto") {
			t.Fatalf("auto = %d, body = %s", auto.Code, auto.Body.String())
		}
	})

	wantPermission := map[string]string{
		"default":   "workspace",
		"plan":      "workspace",
		"":          "workspace",
		"workspace": "workspace",
		"read_only": "read_only",
		"ask":       "ask",
	}
	for permission, mapped := range wantPermission {
		name := permission
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			instance, handler, conversationID, executor := openPiLeadMessageFixture(t)
			body := `{"conversation_id":"` + conversationID + `","content":"task-` + name + `","provider":"pi"`
			if permission != "" {
				body += `,"permission_mode":"` + permission + `"`
			}
			body += `}`
			response := performRequest(handler, http.MethodPost, "/api/messages", body, "")
			if response.Code != http.StatusAccepted {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			run := waitForTerminalRun(t, instance, conversationID)
			if run.Provider != "pi" {
				t.Fatalf("run provider = %q", run.Provider)
			}
			calls := executor.snapshot()
			if len(calls) != 1 || calls[0].Provider != "pi" || calls[0].Options.Role != "lead" {
				t.Fatalf("harness call = %#v", calls)
			}
			if calls[0].Options.PermissionMode != mapped {
				t.Fatalf("permission = %q, want %q", calls[0].Options.PermissionMode, mapped)
			}
			if len(calls[0].Options.MCPServers) != 0 {
				t.Fatalf("MCP leaked onto Pi lead: %#v", calls[0].Options.MCPServers)
			}
		})
	}
}

func openPiLeadMessageFixture(t *testing.T) (*store.Store, http.Handler, string, *recordingHarnessExecutor) {
	t.Helper()
	instance, err := store.Open(t.TempDir() + "/botd.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversations, err := instance.ListConversations(t.Context(), "")
	if err != nil || len(conversations) == 0 {
		t.Fatalf("seeded conversations = %#v, err = %v", conversations, err)
	}
	executor := &recordingHarnessExecutor{}
	handler := (&Server{
		Store: instance, Broker: events.New(), HarnessWorkdir: t.TempDir(),
		AllowHarnessExecution: true, runExecutorOverride: executor,
	}).Handler()
	return instance, handler, conversations[0].ID, executor
}

func TestPiLeadAgentProfileAskIsPreservedAndProviderDefaultRejected(t *testing.T) {
	_, handler := openAgentsAPIServer(t)
	rejected := agentsAPIRequest(handler, http.MethodPost, "/api/agents", `{
		"name":"Bad Pi","title":"Invalid permission",
		"metadata":{"lead":{"harness":"pi","permission":"provider_default"}}
	}`)
	if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), "read_only, workspace, or ask") {
		t.Fatalf("provider_default = %d, body = %s", rejected.Code, rejected.Body.String())
	}

	created := agentsAPIRequest(handler, http.MethodPost, "/api/agents", `{
		"name":"Ask Pi","title":"Explicit ask",
		"metadata":{"lead":{"harness":"pi","model":"openai/gpt-4o","permission":"ask","web_search":"live"}}
	}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create ask = %d, body = %s", created.Code, created.Body.String())
	}
	var agent domain.Agent
	if err := json.NewDecoder(created.Body).Decode(&agent); err != nil {
		t.Fatal(err)
	}
	if agent.Metadata == nil || agent.Metadata.Lead == nil {
		t.Fatal("missing lead")
	}
	if agent.Metadata.Lead.Permission != "ask" {
		t.Fatalf("permission = %q, want ask", agent.Metadata.Lead.Permission)
	}
	if agent.Metadata.Lead.WebSearch != domain.AgentWebSearchDisabled {
		t.Fatalf("web_search = %q, want disabled", agent.Metadata.Lead.WebSearch)
	}
}

func TestPiLeadConfiguredAgentForwardsRoleAndMappedPermission(t *testing.T) {
	instance, err := store.Open(t.TempDir() + "/botd.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	metadata := domain.DefaultAgentMetadata()
	metadata.Lead = &domain.AgentExecutionProfile{
		Harness: "pi", Model: "openai/gpt-4o", Reasoning: "high",
		ServiceTier: "default", Permission: "workspace",
	}
	agent, err := instance.CreateAgentWithMetadata(t.Context(), domain.AgentDraft{Name: "Pi", Title: "Pi lead"}, metadata)
	if err != nil {
		t.Fatal(err)
	}
	executor := &recordingHarnessExecutor{}
	handler := (&Server{
		Store: instance, Broker: events.New(), HarnessWorkdir: t.TempDir(),
		AllowHarnessExecution: true, runExecutorOverride: executor,
	}).Handler()

	response := performRequest(handler, http.MethodPost, "/api/messages", `{"conversation_id":"`+agent.Conversation.ID+`","content":"review","provider":"grok","permission_mode":"auto"}`, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	run := waitForTerminalRun(t, instance, agent.Conversation.ID)
	if run.Provider != "pi" {
		t.Fatalf("run provider = %q, want configured Pi lead", run.Provider)
	}
	calls := executor.snapshot()
	if len(calls) != 1 || calls[0].Provider != "pi" || calls[0].Options.Role != "lead" || calls[0].Options.PermissionMode != "workspace" {
		t.Fatalf("harness call = %#v", calls)
	}
	if calls[0].Options.Model != "openai/gpt-4o" || calls[0].Options.ReasoningEffort != "high" {
		t.Fatalf("model controls = %#v", calls[0].Options)
	}
}
