package harness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	codexAppServerHelperEnv            = "OPENAGENTFLEET_CODEX_APP_SERVER_TEST_HELPER"
	codexAppServerExpectedWebSearchEnv = "OPENAGENTFLEET_CODEX_APP_SERVER_EXPECTED_WEB_SEARCH"
	codexAppServerExpectedPreservedEnv = "OPENAGENTFLEET_CODEX_APP_SERVER_EXPECTED_PRESERVED_CONFIG"
	codexAppServerParamsPathEnv        = "OPENAGENTFLEET_CODEX_APP_SERVER_PARAMS_PATH"
)

// TestCodexAppServerHelper is launched by a small wrapper as a controllable
// JSON-RPC App Server. The wrapper marks only its child process, so the
// harness's deliberately filtered environment remains unchanged.
func TestCodexAppServerHelper(t *testing.T) {
	if os.Getenv(codexAppServerHelperEnv) != "1" {
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for {
		var request rpcMessage
		if err := decoder.Decode(&request); err != nil {
			return
		}
		if len(request.ID) == 0 {
			continue
		}
		result := any(map[string]any{})
		var responseError *rpcError
		switch request.Method {
		case "account/read":
			result = map[string]any{"account": map[string]any{"type": "chatgpt"}}
		case "thread/start", "thread/resume":
			if path := os.Getenv(codexAppServerParamsPathEnv); path != "" {
				if err := os.WriteFile(path, request.Params, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var params struct {
				ThreadID string         `json:"threadId"`
				Config   map[string]any `json:"config"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			if expected := os.Getenv(codexAppServerExpectedWebSearchEnv); expected != "" && params.Config["web_search"] != expected {
				responseError = &rpcError{Code: -32000, Message: "unexpected web_search config"}
			}
			if os.Getenv(codexAppServerExpectedPreservedEnv) == "1" && params.Config["existing"] != "kept" {
				responseError = &rpcError{Code: -32000, Message: "existing config was not preserved"}
			}
			threadID := params.ThreadID
			if threadID == "" {
				threadID = "thread-started"
			}
			result = map[string]any{"thread": map[string]string{"id": threadID}}
		case "turn/start":
			result = map[string]any{"turn": map[string]string{"id": "turn-1", "status": "completed"}}
		}
		if err := encoder.Encode(struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  any             `json:"result"`
			Error   *rpcError       `json:"error,omitempty"`
		}{JSONRPC: "2.0", ID: request.ID, Result: result, Error: responseError}); err != nil {
			return
		}
	}
}

func TestCodexAppServerPassesWebSearchConfigOnStartAndResume(t *testing.T) {
	tests := []struct {
		name       string
		webSearch  string
		sessionID  string
		wantSearch string
	}{
		{name: "start omission defaults live", wantSearch: "live"},
		{name: "start explicit live", webSearch: "live", wantSearch: "live"},
		{name: "resume disabled", webSearch: "disabled", sessionID: "thread-resume", wantSearch: "disabled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newTestCodexAppServerWithExpectedConfig(t, test.wantSearch, true)
			config := map[string]any{"existing": "kept", "web_search": "caller-value"}
			session, err := server.OpenSession(t.Context(), CodexAppSessionOptions{
				Workdir:   t.TempDir(),
				SessionID: test.sessionID,
				WebSearch: test.webSearch,
				Config:    config,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			if config["existing"] != "kept" || config["web_search"] != "caller-value" {
				t.Fatalf("caller config was mutated: %#v", config)
			}
		})
	}
}

func TestRunOptionsThreadCodexWebSearchMode(t *testing.T) {
	server := newTestCodexAppServerWithExpectedConfig(t, "disabled", false)
	runner := &Runner{AllowExecution: true, CodexAppServer: server}
	if _, err := runner.RunWithOptions(t.Context(), CodexAppServerProvider, "test web search", t.TempDir(), RunOptions{
		SessionID: "thread-run-options",
		WebSearch: "disabled",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCodexAppServerRejectsUnsupportedWebSearchMode(t *testing.T) {
	server := newTestCodexAppServer(t)
	_, err := server.OpenSession(t.Context(), CodexAppSessionOptions{Workdir: t.TempDir(), WebSearch: "cached"})
	if err == nil || !strings.Contains(err.Error(), "use live or disabled") {
		t.Fatalf("error = %v, want web_search validation error", err)
	}
}

func TestCodexAppServerResumeSerializesThreadLifetimeAndRoutes(t *testing.T) {
	server := newTestCodexAppServer(t)
	firstNotifications := 0
	firstApprovals := 0
	first, err := server.OpenSession(context.Background(), CodexAppSessionOptions{
		Workdir:   t.TempDir(),
		SessionID: "thread-1",
		OnNotification: func(ACPNotification) {
			firstNotifications++
		},
		OnPermission: func(context.Context, PermissionRequest) (PermissionDecision, error) {
			firstApprovals++
			return PermissionDecision{Outcome: "selected", OptionID: "allow_once"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	other, err := server.OpenSession(context.Background(), CodexAppSessionOptions{Workdir: t.TempDir(), SessionID: "thread-2"})
	if err != nil {
		t.Fatalf("different thread should remain concurrent: %v", err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = server.OpenSession(waitCtx, CodexAppSessionOptions{Workdir: t.TempDir(), SessionID: first.ID})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same thread resume error = %v, want context deadline", err)
	}

	params := json.RawMessage(`{"threadId":"thread-1"}`)
	server.handleNotification(ACPNotification{Method: "item/agentMessage/delta", Params: params})
	if firstNotifications != 1 {
		t.Fatalf("first notification count = %d, want 1", firstNotifications)
	}
	approval, err := server.handleRequest(context.Background(), rpcMessage{
		Method: "item/commandExecution/requestApproval",
		Params: params,
	})
	if err != nil {
		t.Fatal(err)
	}
	if firstApprovals != 1 {
		t.Fatalf("first approval count = %d, want 1", firstApprovals)
	}
	if got := approval.(map[string]string)["decision"]; got != "accept" {
		t.Fatalf("approval decision = %q, want accept", got)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := server.OpenSession(context.Background(), CodexAppSessionOptions{Workdir: t.TempDir(), SessionID: "thread-1"})
	if err != nil {
		t.Fatalf("resume after Close: %v", err)
	}
	if err := resumed.Close(); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	gates := len(server.threadGates)
	server.mu.Unlock()
	if gates != 0 {
		t.Fatalf("thread gates = %d after all sessions closed, want 0", gates)
	}
}

func TestCodexAppSessionCloseKeepsNewerRoute(t *testing.T) {
	server := NewCodexAppServer("codex", t.TempDir())
	older := &CodexAppSession{server: server, ID: "thread-1"}
	newer := &CodexAppSession{server: server, ID: "thread-1"}
	server.sessions[older.ID] = newer

	if err := older.Close(); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	route := server.sessions[older.ID]
	server.mu.Unlock()
	if route != newer {
		t.Fatal("Close removed the newer session route")
	}
}

func newTestCodexAppServer(t *testing.T) *CodexAppServer {
	return newTestCodexAppServerWithExpectedConfig(t, "", false)
}

func newTestCodexAppServerWithExpectedConfig(t *testing.T, expectedWebSearch string, expectPreserved bool) *CodexAppServer {
	return newTestCodexAppServerWithExpectedConfigAndParams(t, expectedWebSearch, expectPreserved, "")
}

func newTestCodexAppServerWithExpectedConfigAndParams(t *testing.T, expectedWebSearch string, expectPreserved bool, paramsPath string) *CodexAppServer {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "fake-codex-app-server")
	preserved := "0"
	if expectPreserved {
		preserved = "1"
	}
	script := "#!/bin/sh\nexport " + codexAppServerHelperEnv + "=1\nexport " + codexAppServerExpectedWebSearchEnv + "=" + shellQuote(expectedWebSearch) + "\nexport " + codexAppServerExpectedPreservedEnv + "=" + preserved + "\nexport " + codexAppServerParamsPathEnv + "=" + shellQuote(paramsPath) + "\nexec " + shellQuote(binary) + " -test.run " + shellQuote("^TestCodexAppServerHelper$") + "\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	server := NewCodexAppServer(wrapper, t.TempDir())
	t.Cleanup(func() { _ = server.Close() })
	return server
}
