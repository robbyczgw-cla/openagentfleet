package collaborationmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServeInitializesListsToolsAndSkipsInitializedNotification(t *testing.T) {
	server := newTestServer(t, DefaultAPIURL, "token", "run-1", "run-token", http.DefaultClient)
	input := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":"tools","method":"tools/list","params":{}}`,
	}, "\n") + "\n")
	var output bytes.Buffer
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}

	responses := decodeResponses(t, output.Bytes())
	if len(responses) != 2 {
		t.Fatalf("response count = %d, want 2; output = %s", len(responses), output.String())
	}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools map[string]any `json:"tools"`
		} `json:"capabilities"`
		ServerInfo struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(responses[0].Result, &initialized); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	if initialized.ProtocolVersion != "2025-06-18" {
		t.Errorf("protocol version = %q, want negotiated version", initialized.ProtocolVersion)
	}
	if _, ok := initialized.Capabilities.Tools["listChanged"]; !ok {
		t.Errorf("initialize result is missing tools capability: %#v", initialized.Capabilities.Tools)
	}
	if initialized.ServerInfo.Name != MCPServerName {
		t.Errorf("server name = %q, want %q", initialized.ServerInfo.Name, MCPServerName)
	}

	var listed struct {
		Tools []toolDefinition `json:"tools"`
	}
	if err := json.Unmarshal(responses[1].Result, &listed); err != nil {
		t.Fatalf("decode tools/list result: %v", err)
	}
	got := make(map[string]bool, len(listed.Tools))
	for _, tool := range listed.Tools {
		got[tool.Name] = true
	}
	for _, name := range []string{
		"list_agents", "message_agent", "delegate_to_agent", "get_agent_task_status",
	} {
		if !got[name] {
			t.Errorf("tools/list is missing %q", name)
		}
	}
	if len(listed.Tools) != 4 {
		t.Errorf("tools/list count = %d, want 4", len(listed.Tools))
	}
}

func TestListAgentsHitsCollaborationHTTPPath(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/collaboration/agents" {
			t.Fatalf("request = %s %s, want GET /api/collaboration/agents", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get(RunIDHeader); got != "run-123" {
			t.Errorf("run id header = %q, want run-123", got)
		}
		if got := r.Header.Get(RunTokenHeader); got != "capability-123" {
			t.Errorf("run capability header = %q, want capability-123", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"agents":[{"id":"rev-1","name":"Reviewer"},{"id":"wrt-2","name":"Writer"}]}`)
	}))
	defer api.Close()

	server := newTestServer(t, api.URL, "test-token", "run-123", "capability-123", api.Client())
	response := call(t, server, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_agents","arguments":{}}}`)
	var result toolResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if result.IsError {
		t.Fatalf("list_agents returned tool error: %#v", result)
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatalf("unexpected tool result: %#v", result)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "Reviewer (rev-1)") || !strings.Contains(text, "Writer (wrt-2)") {
		t.Errorf("list_agents text = %q", text)
	}
	if strings.Contains(text, "test-token") || strings.Contains(text, "capability-123") || strings.Contains(text, `"agents"`) {
		t.Errorf("tool result leaked token or raw JSON: %q", text)
	}
}

func TestDelegateToAgentPostsAndFormatsHumanText(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/collaboration/delegate" {
			t.Fatalf("request = %s %s, want POST /api/collaboration/delegate", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q", got)
		}
		var body struct {
			AgentID string `json:"agent_id"`
			Task    string `json:"task"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.AgentID != "rev-1" || body.Task != "inspect the API" {
			t.Errorf("body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"task_id":"abc","agent_name":"Reviewer","task":"inspect the API","status":"queued","token":"must-not-appear"}`)
	}))
	defer api.Close()

	server := newTestServer(t, api.URL, "test-token", "run-123", "capability-123", api.Client())
	response := call(t, server, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"delegate_to_agent","arguments":{"agent_id":"rev-1","task":"inspect the API"}}}`)
	var result toolResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("unexpected tool result: %#v", result)
	}
	if got := result.Content[0].Text; got != "Asked Reviewer to inspect the API. Task abc is queued." {
		t.Errorf("delegate text = %q", got)
	}
	if strings.Contains(result.Content[0].Text, "must-not-appear") || strings.Contains(result.Content[0].Text, "test-token") {
		t.Errorf("tool result leaked a token: %q", result.Content[0].Text)
	}
}

func TestNewRejectsMissingTokenOrRun(t *testing.T) {
	if _, err := New(Config{APIURL: DefaultAPIURL, RunID: "run", RunToken: "cap"}); err == nil || !strings.Contains(err.Error(), APITokenEnv) {
		t.Fatalf("missing token error = %v", err)
	}
	if _, err := New(Config{APIURL: DefaultAPIURL, APIToken: "token", RunToken: "cap"}); err == nil || !strings.Contains(err.Error(), RunIDEnv) {
		t.Fatalf("missing run id error = %v", err)
	}
	if _, err := New(Config{APIURL: DefaultAPIURL, APIToken: "token", RunID: "run"}); err == nil || !strings.Contains(err.Error(), RunTokenEnv) {
		t.Fatalf("missing run token error = %v", err)
	}
}

func TestNewRejectsNonLoopbackAPIURL(t *testing.T) {
	_, err := New(Config{
		APIURL:   "http://example.com:4317",
		APIToken: "token",
		RunID:    "run",
		RunToken: "cap",
	})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback URL error = %v", err)
	}
}

type testRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

func decodeResponses(t *testing.T, output []byte) []testRPCResponse {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(output), []byte("\n"))
	responses := make([]testRPCResponse, 0, len(lines))
	for _, line := range lines {
		var response testRPCResponse
		if err := json.Unmarshal(line, &response); err != nil {
			t.Fatalf("decode response %q: %v", line, err)
		}
		if response.JSONRPC != "2.0" {
			t.Errorf("jsonrpc = %q, want 2.0", response.JSONRPC)
		}
		if response.Error != nil {
			t.Errorf("unexpected JSON-RPC error: %#v", response.Error)
		}
		responses = append(responses, response)
	}
	return responses
}

func call(t *testing.T, server *Server, request string) testRPCResponse {
	t.Helper()
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(request+"\n"), &output); err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
	responses := decodeResponses(t, output.Bytes())
	if len(responses) != 1 {
		t.Fatalf("response count = %d, want 1", len(responses))
	}
	return responses[0]
}

func newTestServer(t *testing.T, apiURL, token, runID, runToken string, client *http.Client) *Server {
	t.Helper()
	server, err := New(Config{APIURL: apiURL, APIToken: token, RunID: runID, RunToken: runToken, HTTPClient: client})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return server
}
