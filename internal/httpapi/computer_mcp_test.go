package httpapi

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/browsermcp"
	"github.com/robbyczgw-cla/openagentfleet/internal/compute"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
	"github.com/robbyczgw-cla/openagentfleet/internal/testexe"
)

func TestLeadMCPServerSpecsInjectsControllerComputerOnlyWithAgentControl(t *testing.T) {
	command := executableFixture(t)
	server := &Server{
		Docker:                  &compute.Docker{},
		RemoteToken:             "controller-secret",
		AgentComputerMCPCommand: command,
		AgentComputerMCPAPIURL:  "http://127.0.0.1:4317",
	}

	withoutControl, err := server.leadMCPServerSpecs(t.Context(), nil)
	if err != nil {
		t.Fatalf("disabled MCP resolution = %v", err)
	}
	if len(withoutControl) != 0 {
		t.Fatalf("disabled Agent Control injected MCPs = %#v", withoutControl)
	}

	server.computerAgentControl = true
	withControl, err := server.leadMCPServerSpecs(t.Context(), nil)
	if err != nil {
		t.Fatalf("enabled MCP resolution = %v", err)
	}
	if len(withControl) != 1 {
		t.Fatalf("enabled Agent Control MCPs = %#v", withControl)
	}
	computer := withControl[0]
	if computer.Name != "openagentfleet-browser-mcp" || computer.Command != command {
		t.Fatalf("computer MCP identity = %#v", computer)
	}
	if computer.Env["OPENAGENTFLEET_API_URL"] != "http://127.0.0.1:4317" || computer.Env["OPENAGENTFLEET_API_TOKEN"] != "controller-secret" {
		t.Fatalf("computer MCP environment = %#v", computer.Env)
	}
	if computer.Env[browsermcp.RunTokenEnv] == "" || computer.Env[browsermcp.RunIDEnv] != "" {
		t.Fatalf("computer MCP capability environment before run binding = %#v", computer.Env)
	}

	server.computerTakeover = true
	withTakeover, err := server.leadMCPServerSpecs(t.Context(), nil)
	if err != nil {
		t.Fatalf("takeover MCP resolution = %v", err)
	}
	if len(withTakeover) != 0 {
		t.Fatalf("takeover retained Agent Control MCP = %#v", withTakeover)
	}
}

func TestLeadMCPServerSpecsRequiresAuthenticatedController(t *testing.T) {
	server := &Server{
		Docker:                  &compute.Docker{},
		AgentComputerMCPCommand: executableFixture(t),
		AgentComputerMCPAPIURL:  "http://127.0.0.1:4317",
		computerAgentControl:    true,
	}

	_, err := server.leadMCPServerSpecs(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), "bearer authentication") {
		t.Fatalf("missing bearer token error = %v", err)
	}
}

func TestCreateMessageForwardsControllerComputerMCPToLeadOnly(t *testing.T) {
	instance, agent := openOrchestrationStore(t, true, true)
	executor := &recordingHarnessExecutor{}
	serverValue := &Server{
		Store:                   instance,
		Docker:                  &compute.Docker{},
		RemoteToken:             "controller-secret",
		AgentComputerMCPCommand: executableFixture(t),
		HarnessWorkdir:          t.TempDir(),
		AllowHarnessExecution:   true,
		runExecutorOverride:     executor,
		computerAgentControl:    true,
	}

	response := performRequest(serverValue.Handler(), http.MethodPost, "/api/messages", `{"conversation_id":"`+agent.Conversation.ID+`","content":"use the computer"}`, "controller-secret")
	if response.Code != http.StatusAccepted {
		t.Fatalf("create message = %d, body = %s", response.Code, response.Body.String())
	}
	run := waitForTerminalRun(t, instance, agent.Conversation.ID)
	if run.Status != "completed" {
		t.Fatalf("run = %#v", run)
	}

	calls := executor.snapshot()
	if len(calls) != 3 {
		t.Fatalf("harness calls = %#v, want lead, bounded worker, and synthesis", calls)
	}
	if len(calls[0].Options.MCPServers) != 1 || calls[0].Options.MCPServers[0].Name != "openagentfleet-browser-mcp" {
		t.Fatalf("lead MCP servers = %#v", calls[0].Options.MCPServers)
	}
	if calls[0].Options.MCPServers[0].Env[browsermcp.RunIDEnv] != run.ID || calls[0].Options.MCPServers[0].Env[browsermcp.RunTokenEnv] == "" {
		t.Fatalf("lead MCP capability binding = %#v", calls[0].Options.MCPServers[0].Env)
	}
	for _, want := range []string{
		"OpenAgentFleet computer boundary (mandatory)",
		"use only the injected openagentfleet-browser-mcp tools",
		"Never fall back to the host computer",
	} {
		if !strings.Contains(calls[0].Options.SystemPrompt, want) {
			t.Fatalf("lead system prompt missing computer boundary %q: %s", want, calls[0].Options.SystemPrompt)
		}
	}
	if len(calls[1].Options.MCPServers) != 0 {
		t.Fatalf("worker inherited controller MCP = %#v", calls[1].Options.MCPServers)
	}
	if len(calls[2].Options.MCPServers) != 1 || calls[2].Options.MCPServers[0].Name != "openagentfleet-browser-mcp" {
		t.Fatalf("lead synthesis MCP servers = %#v", calls[2].Options.MCPServers)
	}
	serverValue.computerCapabilityMu.RLock()
	remainingCapabilities := len(serverValue.computerCapabilities)
	serverValue.computerCapabilityMu.RUnlock()
	if remainingCapabilities != 0 {
		t.Fatalf("run capability remained after terminal run: %d", remainingCapabilities)
	}
}

func TestComputerCapabilityExpiresAndRejectsTerminalRun(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := instance.CreateRunWithQueuedEvent(t.Context(), conversation.ID, conversation.BotID, "grok", "computer")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: instance}
	server.bindComputerCapability("short-lived", run.ID, time.Minute)
	request := httptest.NewRequest(http.MethodPost, "/api/computer/action", nil)
	request.Header.Set(browsermcp.RunTokenHeader, "short-lived")
	request.Header.Set(browsermcp.RunIDHeader, run.ID)
	if !server.computerCapabilityValid(request) {
		t.Fatal("active queued run capability was rejected")
	}
	if _, err := instance.UpdateRunWithLifecycleEvent(t.Context(), run.ID, "stopped", "", "run.stopped", `{"status":"stopped"}`); err != nil {
		t.Fatal(err)
	}
	if server.computerCapabilityValid(request) {
		t.Fatal("terminal run capability remained valid")
	}
	server.bindComputerCapability("expires", run.ID, 5*time.Millisecond)
	expires := httptest.NewRequest(http.MethodPost, "/api/computer/action", nil)
	expires.Header.Set(browsermcp.RunTokenHeader, "expires")
	expires.Header.Set(browsermcp.RunIDHeader, run.ID)
	time.Sleep(10 * time.Millisecond)
	if server.computerCapabilityValid(expires) {
		t.Fatal("expired capability remained valid")
	}
}

func executableFixture(t *testing.T) string {
	t.Helper()
	return testexe.WriteEcho(t, t.TempDir(), "openagentfleet-browser-mcp", "ok")
}
