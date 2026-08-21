package harness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/testexe"
)

const grokWebSearchHelperEnv = "OPENAGENTFLEET_GROK_WEB_SEARCH_TEST_HELPER"

func TestGrokPlanAndReadOnlyRejectAllTerminalRequests(t *testing.T) {
	methods := []struct {
		name   string
		method string
		params map[string]any
	}{
		{name: "create", method: "terminal/create", params: map[string]any{"command": os.Args[0]}},
		{name: "output", method: "terminal/output", params: map[string]any{"terminalId": "term-1"}},
		{name: "wait", method: "terminal/wait_for_exit", params: map[string]any{"terminalId": "term-1"}},
		{name: "kill", method: "terminal/kill", params: map[string]any{"terminalId": "term-1"}},
		{name: "release", method: "terminal/release", params: map[string]any{"terminalId": "term-1"}},
	}
	for _, mode := range []string{"plan", "read_only"} {
		for _, test := range methods {
			t.Run(mode+"/"+test.name, func(t *testing.T) {
				workdir := t.TempDir()
				session := &GrokSession{Workdir: workdir, terminals: newTerminalManager(workdir)}
				_, err := session.handleRequest(t.Context(), acpTestRequest(t, test.method, test.params), GrokSessionOptions{
					PermissionMode:          mode,
					AllowWorkspaceExecution: true,
				})
				if err == nil || !strings.Contains(err.Error(), "terminal execution is disabled") {
					t.Fatalf("%s with %s error = %v, want terminal policy rejection", test.method, mode, err)
				}
				if len(session.terminals.items) != 0 {
					t.Fatalf("%s with %s created a terminal despite read-only policy", test.method, mode)
				}
			})
		}
	}
}

func TestGrokFilesystemRejectsSymlinkEscapes(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(workspace, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		if runtime.GOOS == "windows" || errors.Is(err, os.ErrPermission) {
			t.Skipf("symlinks are unavailable on this platform: %v", err)
		}
		t.Fatal(err)
	}

	session := &GrokSession{Workdir: workspace, terminals: newTerminalManager(workspace)}
	options := GrokSessionOptions{AllowWorkspaceWrites: true}
	tests := []struct {
		name   string
		method string
		params map[string]any
	}{
		{name: "read existing target", method: "fs/read_text_file", params: map[string]any{"path": "escape/secret.txt"}},
		{name: "write existing target", method: "fs/write_text_file", params: map[string]any{"path": "escape/secret.txt", "content": "changed"}},
		{name: "create below symlink parent", method: "fs/write_text_file", params: map[string]any{"path": "escape/new/note.txt", "content": "created"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := session.handleRequest(t.Context(), acpTestRequest(t, test.method, test.params), options); err == nil || !strings.Contains(err.Error(), "outside the configured workspace") {
				t.Fatalf("%s error = %v, want symlink escape rejection", test.method, err)
			}
		})
	}

	content, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "unchanged" {
		t.Fatalf("outside file was modified: %q", content)
	}
	if _, err := os.Stat(filepath.Join(outside, "new", "note.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside create result = %v, want no file", err)
	}
}

func TestGrokFilesystemAllowsSymlinksContainedWithinWorkspace(t *testing.T) {
	workspace := t.TempDir()
	realDirectory := filepath.Join(workspace, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "inside")
	if err := os.Symlink(realDirectory, link); err != nil {
		if runtime.GOOS == "windows" || errors.Is(err, os.ErrPermission) {
			t.Skipf("symlinks are unavailable on this platform: %v", err)
		}
		t.Fatal(err)
	}

	session := &GrokSession{Workdir: workspace, terminals: newTerminalManager(workspace)}
	_, err := session.handleRequest(t.Context(), acpTestRequest(t, "fs/write_text_file", map[string]any{
		"path":    "inside/new/note.txt",
		"content": "safe",
	}), GrokSessionOptions{AllowWorkspaceWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(realDirectory, "new", "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "safe" {
		t.Fatalf("content = %q, want safe", content)
	}
}

func TestRPCClientCloseReapsProcessAndIsConcurrentSafe(t *testing.T) {
	executable, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	client, err := startRPC(context.Background(), "", executable, []string{
		"-test.run=^TestACPProcessHelper$", "--", "acp-process-helper",
	}, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	const callers = 8
	errorsByCaller := make(chan error, callers)
	var wait sync.WaitGroup
	wait.Add(callers)
	started := time.Now()
	for range callers {
		go func() {
			defer wait.Done()
			errorsByCaller <- client.close()
		}()
	}
	completed := make(chan struct{})
	go func() {
		wait.Wait()
		close(completed)
	}()
	select {
	case <-completed:
	case <-time.After(acpCloseTimeout + 2*time.Second):
		t.Fatal("concurrent ACP close calls deadlocked")
	}
	close(errorsByCaller)
	for closeErr := range errorsByCaller {
		if closeErr != nil {
			t.Fatalf("close error = %v", closeErr)
		}
	}
	if elapsed := time.Since(started); elapsed > acpCloseTimeout+time.Second {
		t.Fatalf("close took %v, want bounded termination", elapsed)
	}
	if client.process.ProcessState == nil {
		t.Fatalf("process state = %#v, want reaped child", client.process.ProcessState)
	}
	select {
	case <-client.done:
	default:
		t.Fatal("process was reaped without closing the client completion signal")
	}
}

func TestRunOptionsControlGrokNativeWebSearchFlag(t *testing.T) {
	tests := []struct {
		name        string
		webSearch   string
		wantDisable bool
	}{
		{name: "omitted defaults live"},
		{name: "explicit live", webSearch: "live"},
		{name: "disabled", webSearch: "disabled", wantDisable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			argumentsPath := filepath.Join(directory, "arguments")
			wrapper := testexe.WriteReexec(t, directory, "grok", "^TestGrokWebSearchHelper$", argumentsPath, map[string]string{
				grokWebSearchHelperEnv: "1",
			})
			_ = wrapper
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))

			runner := &Runner{AllowExecution: true}
			if _, err := runner.RunWithOptions(t.Context(), "grok", "test web search", directory, RunOptions{WebSearch: test.webSearch}); err != nil {
				t.Fatal(err)
			}
			arguments, err := os.ReadFile(argumentsPath)
			if err != nil {
				t.Fatal(err)
			}
			hasDisable := false
			for _, argument := range strings.Split(strings.ReplaceAll(strings.TrimSpace(string(arguments)), "\r\n", "\n"), "\n") {
				if strings.TrimSuffix(argument, "\r") == "--disable-web-search" {
					hasDisable = true
				}
			}
			if hasDisable != test.wantDisable {
				t.Fatalf("arguments = %q, disable flag = %v, want %v", arguments, hasDisable, test.wantDisable)
			}
		})
	}
}

func TestGrokWebSearchRejectsUnsupportedModeBeforeLaunch(t *testing.T) {
	_, err := OpenGrokSession(t.Context(), GrokSessionOptions{
		Binary:    filepath.Join(t.TempDir(), "must-not-launch"),
		Workdir:   t.TempDir(),
		WebSearch: "cached",
	})
	if err == nil || !strings.Contains(err.Error(), "use live or disabled") {
		t.Fatalf("error = %v, want web_search validation error", err)
	}
}

func TestGrokWebSearchHelper(t *testing.T) {
	if os.Getenv(grokWebSearchHelperEnv) != "1" {
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
		switch request.Method {
		case "initialize":
			result = map[string]any{"authMethods": []any{}}
		case "session/new":
			result = map[string]string{"sessionId": "grok-web-search-test"}
		case "session/prompt":
			result = map[string]string{"stopReason": "end_turn"}
		}
		if err := encoder.Encode(struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  any             `json:"result"`
		}{JSONRPC: "2.0", ID: request.ID, Result: result}); err != nil {
			return
		}
	}
}

func TestACPProcessHelper(t *testing.T) {
	for _, argument := range os.Args {
		if argument == "acp-process-helper" {
			for {
				time.Sleep(time.Hour)
			}
		}
	}
}

func acpTestRequest(t *testing.T, method string, params map[string]any) rpcMessage {
	t.Helper()
	payload, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return rpcMessage{Method: method, Params: payload}
}
