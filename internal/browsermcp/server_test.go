package browsermcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServeReturnsCancellationBeforeDispatch(t *testing.T) {
	server, err := New(Config{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	err = server.Serve(ctx, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"), &output)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve() error = %v, want context.Canceled", err)
	}
	if output.Len() != 0 {
		t.Fatalf("cancelled server wrote a response: %s", output.String())
	}
}

func TestServeUnblocksOnCancellationWhileWaitingForInput(t *testing.T) {
	server, err := New(Config{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- server.Serve(ctx, reader, &bytes.Buffer{})
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not unblock after cancellation")
	}
}

func TestServeInitializesListsToolsAndSkipsInitializedNotification(t *testing.T) {
	server, err := New(Config{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
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
		"browser_status", "browser_start", "browser_navigate", "browser_snapshot", "browser_click", "browser_type", "browser_press", "browser_scroll", "browser_screenshot",
		"computer_snapshot", "computer_screenshot", "computer_click", "computer_type", "computer_press", "computer_scroll",
	} {
		if !got[name] {
			t.Errorf("tools/list is missing %q", name)
		}
	}
}

func TestBrowserNavigateForwardsAgentHeaderAndToken(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/computer/action" {
			t.Fatalf("request = %s %s, want POST /api/computer/action", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-OpenAgentFleet-Computer-Use"); got != "agent" {
			t.Errorf("agent header = %q, want agent", got)
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
		var action struct {
			Action string `json:"action"`
			URL    string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&action); err != nil {
			t.Fatalf("decode action: %v", err)
		}
		if action.Action != "navigate" || action.URL != "https://example.com/path" {
			t.Errorf("action = %#v", action)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"running":true,"browser_ready":true,"url":"https://example.com/path"}`)
	}))
	defer api.Close()

	server := newTestServerWithCapability(t, api.URL, "test-token", "run-123", "capability-123", api.Client())
	response := call(t, server, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"browser_navigate","arguments":{"url":"https://example.com/path"}}}`)
	var result toolResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if result.IsError {
		t.Fatalf("browser_navigate returned tool error: %#v", result)
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" || !strings.Contains(result.Content[0].Text, "browser_ready") {
		t.Errorf("unexpected tool result: %#v", result)
	}
}

func TestBrowserScreenshotReturnsMCPImageContent(t *testing.T) {
	frame := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/computer/frame" {
			t.Fatalf("request = %s %s, want GET /api/computer/frame", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-OpenAgentFleet-Computer-Use"); got != "agent" {
			t.Errorf("agent header = %q, want agent", got)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(frame)
	}))
	defer api.Close()

	server := newTestServer(t, api.URL, "", api.Client())
	response := call(t, server, `{"jsonrpc":"2.0","id":"frame","method":"tools/call","params":{"name":"browser_screenshot","arguments":{}}}`)
	var result toolResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("unexpected screenshot result: %#v", result)
	}
	content := result.Content[0]
	if content.Type != "image" || content.MIMEType != "image/png" {
		t.Errorf("image content = %#v", content)
	}
	if got := base64.StdEncoding.EncodeToString(frame); content.Data != got {
		t.Errorf("base64 image data = %q, want %q", content.Data, got)
	}
}

func TestComputerClickMapsToDesktopActionRoute(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/computer/desktop/action" {
			t.Fatalf("request = %s %s, want POST /api/computer/desktop/action", r.Method, r.URL.Path)
		}
		var action struct {
			Action string  `json:"action"`
			X      float64 `json:"x"`
			Y      float64 `json:"y"`
		}
		if err := json.NewDecoder(r.Body).Decode(&action); err != nil {
			t.Fatalf("decode action: %v", err)
		}
		if action.Action != "click" || action.X != 240 || action.Y != 120 {
			t.Errorf("action = %#v", action)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"running":true,"desktop_ready":true}`)
	}))
	defer api.Close()

	server := newTestServer(t, api.URL, "", api.Client())
	response := call(t, server, `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"computer_click","arguments":{"x":240,"y":120}}}`)
	var result toolResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if result.IsError {
		t.Fatalf("computer_click returned tool error: %#v", result)
	}
}

func TestAPIActionDenialIsAnMCPToolError(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusLocked)
		_, _ = io.WriteString(w, `{"error":"enable agent control before browser actions"}`)
	}))
	defer api.Close()

	server := newTestServer(t, api.URL, "", api.Client())
	response := call(t, server, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"browser_press","arguments":{"key":"Enter"}}}`)
	var result toolResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if !result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "enable agent control") {
		t.Errorf("tool error = %#v", result)
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

func newTestServer(t *testing.T, apiURL, token string, client *http.Client) *Server {
	t.Helper()
	server, err := New(Config{APIURL: apiURL, APIToken: token, HTTPClient: client})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return server
}

func newTestServerWithCapability(t *testing.T, apiURL, token, runID, runToken string, client *http.Client) *Server {
	t.Helper()
	server, err := New(Config{APIURL: apiURL, APIToken: token, RunID: runID, RunToken: runToken, HTTPClient: client})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return server
}
