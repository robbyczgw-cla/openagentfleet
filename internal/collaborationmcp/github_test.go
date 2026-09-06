package collaborationmcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestGitHubOnlyBridgeListsAndCallsReadTools(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/connections/github/read" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get(RunIDHeader) != "run-1" || request.Header.Get(RunTokenHeader) != "run-cap" {
			t.Fatalf("missing run capability headers")
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["operation"] != "get_issue" || body["repository"] != "acme/widgets" || body["number"] != float64(17) {
			t.Fatalf("GitHub read body = %#v", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"number":17,"title":"Bounded"}`)),
		}, nil
	})}
	server, err := New(Config{
		APIURL: "http://127.0.0.1:4317", APIToken: "controller", RunID: "run-1", RunToken: "run-cap",
		GitHubEnabled: true, GitHubOnly: true, HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}

	listed := call(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	var toolsPayload struct {
		Tools []toolDefinition `json:"tools"`
	}
	if err := json.Unmarshal(listed.Result, &toolsPayload); err != nil {
		t.Fatal(err)
	}
	if len(toolsPayload.Tools) != 4 {
		t.Fatalf("GitHub-only tool count = %d", len(toolsPayload.Tools))
	}
	for _, tool := range toolsPayload.Tools {
		if !strings.HasPrefix(tool.Name, "github_") {
			t.Fatalf("GitHub-only bridge exposed %q", tool.Name)
		}
	}

	called := call(t, server, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"github_get_issue","arguments":{"repository":"acme/widgets","number":17}}}`)
	var result toolResult
	if err := json.Unmarshal(called.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, `"number":17`) {
		t.Fatalf("GitHub tool result = %#v", result)
	}

	blocked := server.callTool(context.Background(), "list_agents", json.RawMessage(`{}`))
	if !blocked.IsError || !strings.Contains(blocked.Content[0].Text, "collaboration is disabled") {
		t.Fatalf("collaboration tool on GitHub-only bridge = %#v", blocked)
	}
}
