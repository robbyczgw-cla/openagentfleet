package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

type fakeGitHub struct {
	login        string
	repositories string
	responses    map[string]string
	calls        [][]string
	beforeRead   func()
}

func (f *fakeGitHub) run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	path := ""
	if len(args) >= 4 {
		path = args[3]
	}
	switch path {
	case "user":
		if f.beforeRead != nil {
			callback := f.beforeRead
			f.beforeRead = nil
			callback()
		}
		return []byte(f.login + "\n"), nil
	case "user/repos?per_page=100&sort=updated":
		return []byte(f.repositories), nil
	default:
		if response, ok := f.responses[path]; ok {
			return []byte(response), nil
		}
		return nil, fmt.Errorf("unexpected gh request %q", path)
	}
}

func TestGitHubConnectionConfigDiscoveryAndAllowlist(t *testing.T) {
	instance, agentID, _, server := githubTestServer(t)
	fake := &fakeGitHub{
		login:        "octocat",
		repositories: `[{"name":"Acme/Widgets","private":true}]`,
		responses:    map[string]string{"repos/acme/widgets/issues?state=open&per_page=30": `[{"number":7,"title":"Fix it"}]`},
	}
	server.GitHubRunner = fake.run
	handler := server.Handler()

	saved := performRequest(handler, http.MethodPut, "/api/connections/github", `{"enabled":true,"expected_login":"octocat","repositories":["Acme/Widgets","acme/widgets"],"agent_ids":["`+agentID+`","`+agentID+`"]}`, "controller")
	if saved.Code != http.StatusOK {
		t.Fatalf("save connection = %d %s", saved.Code, saved.Body.String())
	}
	config, err := instance.GetGitHubConnection(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if config.Login != "octocat" || len(config.Repositories) != 1 || config.Repositories[0] != "acme/widgets" || len(config.AgentIDs) != 1 {
		t.Fatalf("normalized config = %#v", config)
	}

	status := performRequest(handler, http.MethodGet, "/api/connections/github", "", "controller")
	if status.Code != http.StatusOK {
		t.Fatalf("status = %d %s", status.Code, status.Body.String())
	}
	var statusBody map[string]json.RawMessage
	if err := json.Unmarshal(status.Body.Bytes(), &statusBody); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"enabled", "repositories", "agent_ids", "installed", "authenticated", "login"} {
		if _, ok := statusBody[key]; !ok {
			t.Errorf("status missing %q: %s", key, status.Body.String())
		}
	}
	if len(statusBody) != 6 {
		t.Errorf("status has unexpected fields: %s", status.Body.String())
	}

	discovered := performRequest(handler, http.MethodGet, "/api/connections/github/repositories", "", "controller")
	if discovered.Code != http.StatusOK || !strings.Contains(discovered.Body.String(), "Acme/Widgets") {
		t.Fatalf("discover repositories = %d %s", discovered.Code, discovered.Body.String())
	}

	bad := performRequest(handler, http.MethodPut, "/api/connections/github", `{"enabled":true,"expected_login":"octocat","repositories":["acme/widgets;rm"],"agent_ids":["`+agentID+`"]}`, "controller")
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unsafe repository = %d %s", bad.Code, bad.Body.String())
	}
}

func TestGitHubSaveRejectsIdentityChangeUntilDisconnect(t *testing.T) {
	instance, agentID, _, server := githubTestServer(t)
	fake := &fakeGitHub{login: "alice"}
	server.GitHubRunner = fake.run
	handler := server.Handler()
	body := `{"enabled":true,"expected_login":"alice","repositories":["acme/widgets"],"agent_ids":["` + agentID + `"]}`
	if response := performRequest(handler, http.MethodPut, "/api/connections/github", body, "controller"); response.Code != http.StatusOK {
		t.Fatalf("connect Alice = %d %s", response.Code, response.Body.String())
	}

	fake.login = "bob"
	stale := performRequest(handler, http.MethodPut, "/api/connections/github", body, "controller")
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "disconnect and reconnect") {
		t.Fatalf("stale Alice save = %d %s", stale.Code, stale.Body.String())
	}
	connection, err := instance.GetGitHubConnection(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if connection.Login != "alice" {
		t.Fatalf("stale save changed pinned login: %#v", connection)
	}

	if response := performRequest(handler, http.MethodPut, "/api/connections/github", `{"enabled":false,"repositories":["stale invalid value"],"agent_ids":["stale-agent"]}`, "controller"); response.Code != http.StatusOK {
		t.Fatalf("disconnect Alice = %d %s", response.Code, response.Body.String())
	}
	bobBody := `{"enabled":true,"expected_login":"bob","repositories":["acme/widgets"],"agent_ids":["` + agentID + `"]}`
	if response := performRequest(handler, http.MethodPut, "/api/connections/github", bobBody, "controller"); response.Code != http.StatusOK {
		t.Fatalf("connect Bob after disconnect = %d %s", response.Code, response.Body.String())
	}
}

func TestGitHubGrantRejectsPiAndStalePiGrantDoesNotInjectGitHubMCP(t *testing.T) {
	instance, agentID, _, server := githubTestServer(t)
	fake := &fakeGitHub{login: "octocat"}
	server.GitHubRunner = fake.run
	if _, err := instance.PatchAgent(t.Context(), agentID, domain.AgentProfileUpdate{}, func(metadata domain.AgentMetadata) (domain.AgentMetadata, error) {
		metadata.Lead = &domain.AgentExecutionProfile{Harness: "pi", Model: "openai/gpt-5.5", Reasoning: "high", ServiceTier: "default", Permission: "workspace"}
		return domain.NormalizeAgentMetadata(metadata)
	}); err != nil {
		t.Fatal(err)
	}
	body := `{"enabled":true,"expected_login":"octocat","repositories":["acme/widgets"],"agent_ids":["` + agentID + `"]}`
	response := performRequest(server.Handler(), http.MethodPut, "/api/connections/github", body, "controller")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Pi lead") {
		t.Fatalf("Pi grant = %d %s", response.Code, response.Body.String())
	}
	if _, err := instance.PatchAgent(t.Context(), agentID, domain.AgentProfileUpdate{}, func(metadata domain.AgentMetadata) (domain.AgentMetadata, error) {
		metadata.Lead = nil
		metadata.LeadHarness = ""
		return domain.NormalizeAgentMetadata(metadata)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.PatchPreferences(t.Context(), []byte(`{"workspace":{"engine":"pi"}}`)); err != nil {
		t.Fatal(err)
	}
	response = performRequest(server.Handler(), http.MethodPut, "/api/connections/github", body, "controller")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Pi lead") {
		t.Fatalf("workspace-default Pi grant = %d %s", response.Code, response.Body.String())
	}

	if err := instance.SaveGitHubConnection(t.Context(), store.GitHubConnection{Enabled: true, Login: "octocat", Repositories: []string{"acme/widgets"}, AgentIDs: []string{agentID}}); err != nil {
		t.Fatal(err)
	}
	agents, err := instance.ListAgents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	agentIndex := -1
	for index := range agents {
		if agents[index].Bot.ID == agentID {
			agentIndex = index
			break
		}
	}
	if agentIndex < 0 {
		t.Fatal("updated Pi Agent not found")
	}
	server.RemoteToken = "controller"
	server.CollaborationMCPCommand = "/bin/true"
	existing := []harness.MCPServerSpec{{Name: "user-selected", Command: "user-mcp"}}
	token, specs, err := server.appendCollaborationMCP(t.Context(), existing, agents[agentIndex], true)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" || len(specs) != 1 || specs[0].Name != "user-selected" {
		t.Fatalf("stale Pi grant injected GitHub MCP: token=%q specs=%#v", token, specs)
	}
	if _, err := instance.PatchPreferences(t.Context(), []byte(`{"workspace":{"engine":"grok"}}`)); err != nil {
		t.Fatal(err)
	}
	token, specs, err = server.appendCollaborationMCP(t.Context(), existing, agents[agentIndex], true, "pi")
	if err != nil {
		t.Fatal(err)
	}
	if token != "" || len(specs) != 1 || specs[0].Name != "user-selected" {
		t.Fatalf("per-message Pi override injected GitHub MCP: token=%q specs=%#v", token, specs)
	}
}

func TestGitHubOnlyCapabilityCannotCallCollaborationRoutes(t *testing.T) {
	_, _, runID, server := githubTestServer(t)
	server.setCollabCapabilityScope("github-only-cap", collaborationCapabilityScopeGitHubOnly)
	server.bindCollabCapability("github-only-cap", runID)
	t.Cleanup(func() { server.revokeCollabCapability("github-only-cap") })
	for _, request := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/collaboration/agents"},
		{http.MethodGet, "/api/collaboration/tasks/missing"},
		{http.MethodPost, "/api/collaboration/tasks/missing/cancel"},
	} {
		response := performCollabRequest(server.Handler(), request.method, request.path, "", "controller", runID, "github-only-cap")
		if response.Code != http.StatusUnauthorized {
			t.Errorf("GitHub-only %s %s = %d %s", request.method, request.path, response.Code, response.Body.String())
		}
	}

	server.revokeCollabCapability("github-only-cap")
	server.bindCollabCapability("github-only-cap", runID)
	legacy := performCollabRequest(server.Handler(), http.MethodGet, "/api/collaboration/agents", "", "controller", runID, "github-only-cap")
	if legacy.Code != http.StatusOK {
		t.Fatalf("legacy collaboration helper scope = %d %s", legacy.Code, legacy.Body.String())
	}
}

func TestGitHubReadRequiresCapabilityGrantAndPinnedIdentity(t *testing.T) {
	instance, agentID, runID, server := githubTestServer(t)
	fake := &fakeGitHub{
		login:     "octocat",
		responses: map[string]string{"repos/acme/widgets/issues?state=open&per_page=30": `[{"number":7}]`},
	}
	server.GitHubRunner = fake.run
	if err := instance.SaveGitHubConnection(t.Context(), store.GitHubConnection{Enabled: true, Login: "octocat", Repositories: []string{"acme/widgets"}, AgentIDs: []string{agentID}}); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	body := `{"operation":"list_issues","repository":"acme/widgets"}`

	missing := performRequest(handler, http.MethodPost, "/api/connections/github/read", body, "controller")
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing capability = %d %s", missing.Code, missing.Body.String())
	}
	server.bindCollabCapability("github-cap", runID)
	allowed := performCollabRequest(handler, http.MethodPost, "/api/connections/github/read", body, "controller", runID, "github-cap")
	if allowed.Code != http.StatusOK || !strings.Contains(allowed.Body.String(), `"number":7`) {
		t.Fatalf("allowed read = %d %s", allowed.Code, allowed.Body.String())
	}

	denied := performCollabRequest(handler, http.MethodPost, "/api/connections/github/read", `{"operation":"list_issues","repository":"acme/other"}`, "controller", runID, "github-cap")
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "not allowed") {
		t.Fatalf("repository allowlist = %d %s", denied.Code, denied.Body.String())
	}

	fake.login = "mallory"
	mismatch := performCollabRequest(handler, http.MethodPost, "/api/connections/github/read", body, "controller", runID, "github-cap")
	if mismatch.Code != http.StatusForbidden || !strings.Contains(mismatch.Body.String(), "account changed") {
		t.Fatalf("identity mismatch = %d %s", mismatch.Code, mismatch.Body.String())
	}
	status := performRequest(handler, http.MethodGet, "/api/connections/github", "", "controller")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"authenticated":false`) || !strings.Contains(status.Body.String(), `"login":"octocat"`) {
		t.Fatalf("mismatched status = %d %s", status.Code, status.Body.String())
	}
}

func TestGitHubReadRechecksRevocationAfterIdentity(t *testing.T) {
	instance, agentID, runID, server := githubTestServer(t)
	fake := &fakeGitHub{login: "octocat", responses: map[string]string{"repos/acme/widgets/issues?state=open&per_page=30": `[]`}}
	server.GitHubRunner = fake.run
	connection := store.GitHubConnection{Enabled: true, Login: "octocat", Repositories: []string{"acme/widgets"}, AgentIDs: []string{agentID}}
	if err := instance.SaveGitHubConnection(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	server.bindCollabCapability("github-cap", runID)
	fake.beforeRead = func() {
		connection.AgentIDs = []string{}
		if err := instance.SaveGitHubConnection(context.Background(), connection); err != nil {
			t.Errorf("revoke Agent grant: %v", err)
		}
	}
	response := performCollabRequest(server.Handler(), http.MethodPost, "/api/connections/github/read", `{"operation":"list_issues","repository":"acme/widgets"}`, "controller", runID, "github-cap")
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "revoked") {
		t.Fatalf("revoked read = %d %s", response.Code, response.Body.String())
	}
	for _, call := range fake.calls {
		if len(call) >= 4 && strings.HasPrefix(call[3], "repos/") {
			t.Fatalf("revoked request reached repository API: %#v", fake.calls)
		}
	}
}

func githubTestServer(t *testing.T) (*store.Store, string, string, *Server) {
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
	run, _, err := instance.CreateRunWithQueuedEvent(t.Context(), conversation.ID, conversation.BotID, "grok", "read GitHub")
	if err != nil {
		t.Fatal(err)
	}
	return instance, conversation.BotID, run.ID, &Server{Store: instance, RemoteToken: "controller"}
}
