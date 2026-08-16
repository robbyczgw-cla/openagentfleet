package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/browsermcp"
	"github.com/robbyczgw-cla/openagentfleet/internal/compute"
	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/events"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
	"github.com/robbyczgw-cla/openagentfleet/internal/orchestration"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

type recordedHarnessCall struct {
	Provider string
	Prompt   string
	Workdir  string
	Options  harness.RunOptions
}

type recordingHarnessExecutor struct {
	mu    sync.Mutex
	calls []recordedHarnessCall
}

func (executor *recordingHarnessExecutor) RunWithOptions(_ context.Context, provider, prompt, workdir string, options harness.RunOptions) (string, error) {
	executor.mu.Lock()
	executor.calls = append(executor.calls, recordedHarnessCall{Provider: provider, Prompt: prompt, Workdir: workdir, Options: options})
	callNumber := len(executor.calls)
	executor.mu.Unlock()
	if options.OnSession != nil && callNumber == 1 {
		options.OnSession("lead-session-1")
	}
	switch provider {
	case "grok":
		if strings.Contains(prompt, "You are a bounded one-hop worker") {
			return `{"type":"text","data":"worker evidence"}` + "\n", nil
		}
		if strings.Contains(prompt, "Produce the final user-facing answer") {
			return `{"type":"text","data":"final answer"}` + "\n", nil
		}
		return `{"type":"text","data":"lead draft"}` + "\n", nil
	default:
		return "unexpected provider", nil
	}
}

func (executor *recordingHarnessExecutor) snapshot() []recordedHarnessCall {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	result := make([]recordedHarnessCall, len(executor.calls))
	copy(result, executor.calls)
	return result
}

func TestConfiguredWorkersExecuteLeadWorkerLeadWhenFeatureEnabled(t *testing.T) {
	instance, agent := openOrchestrationStore(t, true, true)
	executor := &recordingHarnessExecutor{}
	server := (&Server{
		Store: instance, Broker: events.New(), HarnessWorkdir: t.TempDir(),
		AllowHarnessExecution: true, runExecutorOverride: executor,
	}).Handler()

	response := performRequest(server, "POST", "/api/messages", `{"conversation_id":"`+agent.Conversation.ID+`","content":"review the backend"}`, "")
	if response.Code != 202 {
		t.Fatalf("create message = %d, body = %s", response.Code, response.Body.String())
	}
	run := waitForTerminalRun(t, instance, agent.Conversation.ID)
	if run.Status != "completed" {
		t.Fatalf("run = %#v", run)
	}

	calls := executor.snapshot()
	if len(calls) != 3 || calls[0].Provider != "grok" || calls[1].Provider != "grok" || calls[2].Provider != "grok" {
		t.Fatalf("harness calls = %#v, want grok lead -> grok worker -> grok lead", calls)
	}
	worker := calls[1]
	if worker.Options.Model != "grok-worker" || worker.Options.ReasoningEffort != "high" || worker.Options.ServiceTier != "default" || worker.Options.PermissionMode != "plan" {
		t.Fatalf("worker profile was not forwarded exactly: %#v", worker.Options)
	}
	if worker.Options.SessionID != "" || worker.Options.WebSearch != domain.AgentWebSearchDisabled || len(worker.Options.MCPServers) != 0 {
		t.Fatalf("worker inherited lead authority: %#v", worker.Options)
	}
	for _, want := range []string{"Profile: reviewer", "Maximum controller turns: 3", "lead draft", "review the backend"} {
		if !strings.Contains(worker.Prompt, want) {
			t.Fatalf("worker prompt missing %q: %s", want, worker.Prompt)
		}
	}
	if calls[2].Options.SessionID != "lead-session-1" {
		t.Fatalf("synthesis did not resume lead session: %#v", calls[2].Options)
	}
	if !strings.Contains(calls[2].Prompt, "worker evidence") {
		t.Fatalf("synthesis prompt has no worker result: %s", calls[2].Prompt)
	}

	messages, err := instance.ListMessages(t.Context(), agent.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "assistant" || messages[1].Content != "final answer" {
		t.Fatalf("messages = %#v", messages)
	}
	eventItems, err := instance.ListRunEvents(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var eventTypes []string
	for _, event := range eventItems {
		eventTypes = append(eventTypes, event.Type)
	}
	for _, want := range []string{"lead.draft.started", "lead.draft.completed", "worker.started", "worker.completed", "lead.synthesis.started", "lead.synthesis.completed", "run.completed"} {
		if !containsString(eventTypes, want) {
			t.Fatalf("events %v do not contain %q", eventTypes, want)
		}
	}
}

func TestDirectLeadPathRemainsSingleInvocation(t *testing.T) {
	tests := []struct {
		name           string
		featureEnabled bool
		withWorker     bool
	}{
		{name: "no stored workers", featureEnabled: true, withWorker: false},
		{name: "runtime toggle disabled", featureEnabled: false, withWorker: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instance, agent := openOrchestrationStore(t, test.featureEnabled, test.withWorker)
			executor := &recordingHarnessExecutor{}
			handler := (&Server{
				Store: instance, Broker: events.New(), HarnessWorkdir: t.TempDir(),
				AllowHarnessExecution: true, runExecutorOverride: executor,
			}).Handler()
			response := performRequest(handler, "POST", "/api/messages", `{"conversation_id":"`+agent.Conversation.ID+`","content":"direct task"}`, "")
			if response.Code != 202 {
				t.Fatalf("create message = %d, body = %s", response.Code, response.Body.String())
			}
			run := waitForTerminalRun(t, instance, agent.Conversation.ID)
			if run.Status != "completed" {
				t.Fatalf("run = %#v", run)
			}
			if calls := executor.snapshot(); len(calls) != 1 || calls[0].Provider != "grok" {
				t.Fatalf("direct calls = %#v", calls)
			}
		})
	}
}

func TestLeadRunReceivesAgentComputerMCPOnlyWhileAgentControlIsEnabled(t *testing.T) {
	for _, test := range []struct {
		name          string
		agentControl  bool
		wantMCPServer bool
	}{
		{name: "disabled", agentControl: false, wantMCPServer: false},
		{name: "enabled", agentControl: true, wantMCPServer: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			bridgeCommand, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			instance, agent := openOrchestrationStore(t, false, false)
			executor := &recordingHarnessExecutor{}
			server := &Server{
				Store:                   instance,
				Docker:                  &compute.Docker{},
				Broker:                  events.New(),
				HarnessWorkdir:          t.TempDir(),
				AllowHarnessExecution:   true,
				RemoteToken:             "run-scoped-token",
				AgentComputerMCPCommand: bridgeCommand,
				AgentComputerMCPAPIURL:  "http://127.0.0.1:4317",
				runExecutorOverride:     executor,
			}
			server.computerAgentControl = test.agentControl

			response := performRequest(server.Handler(), "POST", "/api/messages", `{"conversation_id":"`+agent.Conversation.ID+`","content":"inspect the local computer"}`, "run-scoped-token")
			if response.Code != 202 {
				t.Fatalf("create message = %d, body = %s", response.Code, response.Body.String())
			}
			if run := waitForTerminalRun(t, instance, agent.Conversation.ID); run.Status != "completed" {
				t.Fatalf("run = %#v", run)
			}

			calls := executor.snapshot()
			if len(calls) != 1 {
				t.Fatalf("harness calls = %#v, want one lead call", calls)
			}
			if !test.wantMCPServer {
				if len(calls[0].Options.MCPServers) != 0 {
					t.Fatalf("disabled Agent Control injected MCP servers: %#v", calls[0].Options.MCPServers)
				}
				return
			}
			if len(calls[0].Options.MCPServers) != 1 {
				t.Fatalf("enabled Agent Control MCP servers = %#v, want one", calls[0].Options.MCPServers)
			}
			computer := calls[0].Options.MCPServers[0]
			if computer.Name != browsermcp.MCPServerName || computer.Command != bridgeCommand || len(computer.Args) != 0 {
				t.Fatalf("computer MCP spec = %#v", computer)
			}
			if computer.Env[browsermcp.APIURLEnv] != "http://127.0.0.1:4317" || computer.Env[browsermcp.APITokenEnv] != "run-scoped-token" {
				t.Fatalf("computer MCP environment = %#v", computer.Env)
			}
		})
	}
}

func TestAgentComputerActionRemainsFailClosedWhenAgentControlIsDisabled(t *testing.T) {
	server := &Server{RemoteToken: "run-scoped-token", Docker: &compute.Docker{}}
	request := httptest.NewRequest("POST", "/api/computer/action", strings.NewReader(`{"action":"click","x":1,"y":1}`))
	request.Header.Set("X-OpenAgentFleet-Computer-Use", "agent")
	request.Header.Set("Authorization", "Bearer run-scoped-token")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != 423 || !strings.Contains(response.Body.String(), "enable agent control") {
		t.Fatalf("disabled agent action = %d, body = %s; wanted locked fail-closed response", response.Code, response.Body.String())
	}
}

func TestAgentComputerCapabilityIsBoundToRunAndRevoked(t *testing.T) {
	server := &Server{computerAgentControl: true}
	server.bindComputerCapability("capability-a", "run-a")

	request := httptest.NewRequest(http.MethodGet, "/api/computer", nil)
	request.Header.Set("X-OpenAgentFleet-Computer-Use", "agent")
	request.Header.Set(browsermcp.RunIDHeader, "run-a")
	request.Header.Set(browsermcp.RunTokenHeader, "capability-a")
	if !server.computerAgentAuthorized(httptest.NewRecorder(), request, "status") {
		t.Fatal("valid run capability was rejected")
	}

	wrongRun := httptest.NewRequest(http.MethodGet, "/api/computer", nil)
	wrongRun.Header.Set("X-OpenAgentFleet-Computer-Use", "agent")
	wrongRun.Header.Set(browsermcp.RunIDHeader, "run-b")
	wrongRun.Header.Set(browsermcp.RunTokenHeader, "capability-a")
	wrongResponse := httptest.NewRecorder()
	if server.computerAgentAuthorized(wrongResponse, wrongRun, "status") {
		t.Fatal("capability was accepted for a different run")
	}
	if wrongResponse.Code != http.StatusLocked {
		t.Fatalf("wrong-run response = %d, want %d", wrongResponse.Code, http.StatusLocked)
	}

	server.revokeComputerCapability("capability-a")
	revokedResponse := httptest.NewRecorder()
	if server.computerAgentAuthorized(revokedResponse, request, "status") {
		t.Fatal("revoked capability was accepted")
	}
	if revokedResponse.Code != http.StatusLocked {
		t.Fatalf("revoked response = %d, want %d", revokedResponse.Code, http.StatusLocked)
	}
}

func TestConfiguredLeadHarnessAcceptsPiAndRejectsClaude(t *testing.T) {
	lead, err := configuredLeadHarness("pi")
	if err != nil || lead != orchestration.LeadPi {
		t.Fatalf("configuredLeadHarness(pi) = %q, %v", lead, err)
	}
	if _, err := configuredLeadHarness("claude"); err == nil || !strings.Contains(err.Error(), "unsupported configured lead harness") {
		t.Fatalf("configuredLeadHarness(claude) = %v, want unsupported lead error", err)
	}
}

func TestPiLeadRejectsMCPServersIncludingComputer(t *testing.T) {
	if err := rejectPiLeadMCP("pi", []harness.MCPServerSpec{{Name: "hound", Command: "hound-mcp"}}); err == nil || !strings.Contains(err.Error(), "does not support MCP") {
		t.Fatalf("search MCP error = %v", err)
	}
	if err := rejectPiLeadMCP("pi", []harness.MCPServerSpec{{Name: browsermcp.MCPServerName, Command: "openagentfleet-browser-mcp"}}); err == nil || !strings.Contains(err.Error(), "Agent Computer") {
		t.Fatalf("computer MCP error = %v", err)
	}
	if err := rejectPiLeadMCP("grok", []harness.MCPServerSpec{{Name: "hound", Command: "hound-mcp"}}); err != nil {
		t.Fatalf("Grok MCP unexpectedly rejected: %v", err)
	}

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
	server := &Server{
		Store:                   instance,
		Docker:                  &compute.Docker{},
		Broker:                  events.New(),
		HarnessWorkdir:          t.TempDir(),
		AllowHarnessExecution:   true,
		RemoteToken:             "run-scoped-token",
		AgentComputerMCPCommand: executableFixture(t),
		AgentComputerMCPAPIURL:  "http://127.0.0.1:4317",
		runExecutorOverride:     &recordingHarnessExecutor{},
		computerAgentControl:    true,
	}
	response := performRequest(server.Handler(), "POST", "/api/messages", `{"conversation_id":"`+conversations[0].ID+`","content":"inspect with computer","provider":"pi","permission_mode":"workspace"}`, "run-scoped-token")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "does not support MCP") {
		t.Fatalf("Pi lead with Computer MCP = %d, body = %s", response.Code, response.Body.String())
	}
	if calls := server.runExecutorOverride.(*recordingHarnessExecutor).snapshot(); len(calls) != 0 {
		t.Fatalf("Pi lead launched despite MCP: %#v", calls)
	}
}

func TestConfiguredBoundedWorkersRejectsAdapterWithoutPermissionEnforcement(t *testing.T) {
	_, err := configuredBoundedWorkers([]domain.AgentExecutionProfile{{
		ID: "unsafe-cli", Harness: "claude", Reasoning: "high", ServiceTier: "default",
		Permission: "read_only", MaxTurns: 3, TimeoutSeconds: 30,
	}})
	if err == nil || !strings.Contains(err.Error(), "cannot enforce stored permission") {
		t.Fatalf("error = %v, want fail-closed permission enforcement error", err)
	}
}

func TestConfiguredBoundedWorkersAcceptsPiReadOnlyAndWorkspace(t *testing.T) {
	for _, permission := range []string{"read_only", "workspace"} {
		workers, err := configuredBoundedWorkers([]domain.AgentExecutionProfile{{
			ID: "pi-reviewer", Harness: "pi", Model: "openai/gpt-4o", Reasoning: "high",
			ServiceTier: "default", Permission: permission, MaxTurns: 3, TimeoutSeconds: 30,
		}})
		if err != nil {
			t.Fatalf("permission %s: %v", permission, err)
		}
		if len(workers) != 1 || workers[0].Route.Worker != orchestration.WorkerPi {
			t.Fatalf("permission %s workers = %#v", permission, workers)
		}
	}
	if _, err := configuredBoundedWorkers([]domain.AgentExecutionProfile{{
		ID: "pi-ask", Harness: "pi", Reasoning: "high", ServiceTier: "default",
		Permission: "ask", MaxTurns: 3, TimeoutSeconds: 30,
	}}); err == nil || !strings.Contains(err.Error(), "read_only or workspace") {
		t.Fatalf("ask error = %v", err)
	}
}

func openOrchestrationStore(t *testing.T, featureEnabled, withWorker bool) (*store.Store, domain.Agent) {
	t.Helper()
	instance, err := store.Open(t.TempDir() + "/botd.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if featureEnabled {
		if _, err := instance.PatchPreferences(t.Context(), []byte(`{"features":{"lead_worker_runtime":true}}`)); err != nil {
			t.Fatal(err)
		}
	}
	metadata := domain.DefaultAgentMetadata()
	metadata.Lead = &domain.AgentExecutionProfile{
		Harness: "grok_build", Reasoning: "default", ServiceTier: "default", Permission: "ask",
		WebSearch: domain.AgentWebSearchLive, TimeoutSeconds: 120,
	}
	if withWorker {
		metadata.Workers = []domain.AgentExecutionProfile{{
			ID: "reviewer", Harness: "grok", Model: "grok-worker", Reasoning: "high",
			ServiceTier: "default", Permission: "read_only", MaxTurns: 3, TimeoutSeconds: 30,
		}}
	}
	agent, err := instance.CreateAgentWithMetadata(t.Context(), domain.AgentDraft{Name: "Builder", Title: "Backend builder"}, metadata)
	if err != nil {
		t.Fatal(err)
	}
	return instance, agent
}

func waitForTerminalRun(t *testing.T, instance *store.Store, conversationID string) domain.Run {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := instance.ListRuns(t.Context(), conversationID)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) == 1 && (runs[0].Status == "completed" || runs[0].Status == "failed" || runs[0].Status == "stopped") {
			return runs[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not reach a terminal state")
	return domain.Run{}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
