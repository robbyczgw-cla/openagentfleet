package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	grokMCPHelperEnv     = "OPENAGENTFLEET_GROK_MCP_TEST_HELPER"
	grokMCPParamsPathEnv = "OPENAGENTFLEET_GROK_MCP_PARAMS_PATH"
)

func TestMCPServerValidationAndDefensiveCopy(t *testing.T) {
	original := []MCPServerSpec{{
		Name:    "hound-search",
		Command: "hound-mcp",
		Args:    []string{"--stdio"},
		Env:     map[string]string{"MODE": "free"},
	}}
	servers, err := normalizeMCPServers(original)
	if err != nil {
		t.Fatal(err)
	}
	original[0].Name = "changed"
	original[0].Args[0] = "changed"
	original[0].Env["MODE"] = "changed"
	if servers[0].Name != "hound-search" || !slices.Equal(servers[0].Args, []string{"--stdio"}) || servers[0].Env["MODE"] != "free" {
		t.Fatalf("normalized MCP servers alias caller data: %#v", servers)
	}
}

func TestMCPServerValidationRejectsInvalidSpecs(t *testing.T) {
	tooManyServers := make([]MCPServerSpec, maxMCPServers+1)
	for index := range tooManyServers {
		tooManyServers[index] = MCPServerSpec{Name: "server-" + string(rune('a'+index)), Command: "mcp"}
	}
	tooManyArgs := make([]string, maxMCPArguments+1)
	tooManyEnv := make(map[string]string, maxMCPEnvironment+1)
	for index := range maxMCPEnvironment + 1 {
		tooManyEnv["KEY_"+string(rune('a'+index))] = "value"
	}
	tests := []struct {
		name    string
		servers []MCPServerSpec
		want    string
	}{
		{name: "too many servers", servers: tooManyServers, want: "count"},
		{name: "empty name", servers: []MCPServerSpec{{Command: "mcp"}}, want: "name"},
		{name: "unsafe name", servers: []MCPServerSpec{{Name: "bad.name", Command: "mcp"}}, want: "name"},
		{name: "duplicate name", servers: []MCPServerSpec{{Name: "same", Command: "one"}, {Name: "same", Command: "two"}}, want: "duplicated"},
		{name: "empty command", servers: []MCPServerSpec{{Name: "one", Command: " \t"}}, want: "command"},
		{name: "command control", servers: []MCPServerSpec{{Name: "one", Command: "mcp\nserver"}}, want: "control"},
		{name: "too many args", servers: []MCPServerSpec{{Name: "one", Command: "mcp", Args: tooManyArgs}}, want: "argument count"},
		{name: "argument NUL", servers: []MCPServerSpec{{Name: "one", Command: "mcp", Args: []string{"bad\x00arg"}}}, want: "NUL"},
		{name: "too many env", servers: []MCPServerSpec{{Name: "one", Command: "mcp", Env: tooManyEnv}}, want: "environment count"},
		{name: "unsafe env name", servers: []MCPServerSpec{{Name: "one", Command: "mcp", Env: map[string]string{"BAD=NAME": "value"}}}, want: "portable ASCII identifier"},
		{name: "empty env name", servers: []MCPServerSpec{{Name: "one", Command: "mcp", Env: map[string]string{"": "value"}}}, want: "portable ASCII identifier"},
		{name: "env name NUL", servers: []MCPServerSpec{{Name: "one", Command: "mcp", Env: map[string]string{"BAD\x00KEY": "value"}}}, want: "portable ASCII identifier"},
		{name: "env value NUL", servers: []MCPServerSpec{{Name: "one", Command: "mcp", Env: map[string]string{"KEY": "bad\x00value"}}}, want: "NUL"},
		{name: "argument too long", servers: []MCPServerSpec{{Name: "one", Command: "mcp", Args: []string{strings.Repeat("a", maxMCPArgumentBytes+1)}}}, want: "exceeds"},
		{name: "environment value too long", servers: []MCPServerSpec{{Name: "one", Command: "mcp", Env: map[string]string{"KEY": strings.Repeat("v", maxMCPEnvValueBytes+1)}}}, want: "exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeMCPServers(test.servers)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestMCPInjectionRejectsUnsupportedCLIWorkersBeforeLaunch(t *testing.T) {
	runner := &Runner{AllowExecution: true}
	servers := []MCPServerSpec{{Name: "hound", Command: "must-not-launch"}}
	for _, provider := range []string{"pi", "claude", "codex", "cursor"} {
		t.Run(provider, func(t *testing.T) {
			_, err := runner.RunWithOptions(t.Context(), provider, "test", t.TempDir(), RunOptions{MCPServers: servers})
			if err == nil || !strings.Contains(err.Error(), "MCP server injection is unsupported") {
				t.Fatalf("error = %v, want unsupported MCP injection", err)
			}
		})
	}
}

func TestGrokMCPInjectionRejectsYoloBeforeLaunch(t *testing.T) {
	_, err := OpenGrokSession(t.Context(), GrokSessionOptions{
		Binary:         filepath.Join(t.TempDir(), "must-not-launch"),
		Workdir:        t.TempDir(),
		PermissionMode: "yolo",
		MCPServers:     []MCPServerSpec{{Name: "hound", Command: "hound-mcp"}},
	})
	if err == nil || !strings.Contains(err.Error(), "yolo mode is disabled") {
		t.Fatalf("error = %v, want yolo rejection", err)
	}
}

func TestGrokMCPServersUseExactACPShapeOnStartAndLoad(t *testing.T) {
	servers := []MCPServerSpec{{
		Name:    "hound",
		Command: "/opt/hound-mcp",
		Args:    []string{"--stdio", "free"},
		Env:     map[string]string{"ZETA": "last", "ALPHA": "first"},
	}}
	for _, test := range []struct {
		name      string
		sessionID string
		method    string
	}{
		{name: "start", method: "session/new"},
		{name: "load", sessionID: "grok-resume", method: "session/load"},
	} {
		t.Run(test.name, func(t *testing.T) {
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			paramsPath := filepath.Join(directory, "params.json")
			argumentsPath := filepath.Join(directory, "arguments")
			wrapper := filepath.Join(directory, "grok")
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(argumentsPath) + "\nexport " + grokMCPHelperEnv + "=1\nexport " + grokMCPParamsPathEnv + "=" + shellQuote(paramsPath) + "\nexec " + shellQuote(binary) + " -test.run " + shellQuote("^TestGrokMCPHelper$") + "\n"
			if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}

			session, err := OpenGrokSession(t.Context(), GrokSessionOptions{
				Binary:     wrapper,
				Workdir:    directory,
				SessionID:  test.sessionID,
				MCPServers: servers,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}

			actual, err := os.ReadFile(paramsPath)
			if err != nil {
				t.Fatal(err)
			}
			expectedParams := map[string]any{
				"cwd":        directory,
				"mcpServers": json.RawMessage(`[{"name":"hound","command":"/opt/hound-mcp","args":["--stdio","free"],"env":[{"name":"ALPHA","value":"first"},{"name":"ZETA","value":"last"}]}]`),
			}
			if test.sessionID != "" {
				expectedParams["sessionId"] = test.sessionID
			}
			expected, err := json.Marshal(expectedParams)
			if err != nil {
				t.Fatal(err)
			}
			if string(actual) != string(expected) {
				t.Fatalf("%s params = %s, want %s", test.method, actual, expected)
			}
			arguments, err := os.ReadFile(argumentsPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(arguments), "yolo") {
				t.Fatalf("Grok arguments enabled yolo: %q", arguments)
			}
		})
	}
}

func TestGrokMCPHelper(t *testing.T) {
	if os.Getenv(grokMCPHelperEnv) != "1" {
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
		case "session/new", "session/load":
			if err := os.WriteFile(os.Getenv(grokMCPParamsPathEnv), request.Params, 0o600); err != nil {
				t.Fatal(err)
			}
			result = map[string]string{"sessionId": "grok-started"}
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

func TestCodexMCPServersUseExactConfigOnStartAndResume(t *testing.T) {
	servers := []MCPServerSpec{{
		Name:    "hound",
		Command: "hound-mcp",
		Args:    []string{"--stdio", "free"},
		Env:     map[string]string{"MODE": "keyless"},
	}}
	for _, test := range []struct {
		name      string
		sessionID string
	}{
		{name: "start"},
		{name: "resume", sessionID: "codex-resume"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			paramsPath := filepath.Join(directory, "params.json")
			server := newTestCodexAppServerWithExpectedConfigAndParams(t, "disabled", true, paramsPath)
			existingMCP := map[string]any{"existing-server": map[string]any{"command": "existing", "enabled": false}}
			config := map[string]any{
				"existing":    "kept",
				"web_search":  "caller-value",
				"mcp_servers": existingMCP,
			}
			session, err := server.OpenSession(t.Context(), CodexAppSessionOptions{
				Workdir:    directory,
				SessionID:  test.sessionID,
				WebSearch:  "disabled",
				Config:     config,
				MCPServers: servers,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}

			actual, err := os.ReadFile(paramsPath)
			if err != nil {
				t.Fatal(err)
			}
			expectedParams := map[string]any{
				"cwd":            directory,
				"approvalPolicy": "untrusted",
				"sandbox":        "read-only",
				"serviceName":    "atlas_openagentfleet",
				"config": map[string]any{
					"existing":   "kept",
					"web_search": "disabled",
					"mcp_servers": map[string]any{
						"existing-server": map[string]any{"command": "existing", "enabled": false},
						"hound":           json.RawMessage(`{"command":"hound-mcp","args":["--stdio","free"],"env":{"MODE":"keyless"},"enabled":true,"required":false,"default_tools_approval_mode":"prompt"}`),
					},
				},
			}
			if test.sessionID != "" {
				expectedParams["threadId"] = test.sessionID
			}
			expected, err := json.Marshal(expectedParams)
			if err != nil {
				t.Fatal(err)
			}
			if string(actual) != string(expected) {
				t.Fatalf("Codex params = %s, want %s", actual, expected)
			}
			if config["web_search"] != "caller-value" || len(existingMCP) != 1 {
				t.Fatalf("caller Codex config was mutated: %#v", config)
			}
		})
	}
}

func TestOpenCodeMCPConfigIsExactAndProcessLocal(t *testing.T) {
	directory := t.TempDir()
	contentPath := filepath.Join(directory, "content.json")
	argumentsPath := filepath.Join(directory, "arguments")
	wrapper := filepath.Join(directory, OpenCodeProvider)
	script := "#!/bin/sh\nif [ \"${OPENAI_API_KEY+x}\" = x ] || [ \"${ANTHROPIC_API_KEY+x}\" = x ] || [ \"${XAI_API_KEY+x}\" = x ]; then exit 23; fi\nprintf '%s' \"$OPENCODE_CONFIG_CONTENT\" > " + shellQuote(contentPath) + "\nprintf '%s\\n' \"$@\" > " + shellQuote(argumentsPath) + "\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENAI_API_KEY", "ambient-openai")
	t.Setenv("ANTHROPIC_API_KEY", "ambient-anthropic")
	t.Setenv("XAI_API_KEY", "ambient-xai")

	runner := &Runner{AllowExecution: true}
	servers := []MCPServerSpec{{
		Name:    "hound",
		Command: "hound-mcp",
		Args:    []string{"--stdio", "free"},
		Env:     map[string]string{"MODE": "keyless"},
	}}
	if _, err := runner.RunWithOptions(t.Context(), OpenCodeProvider, "search", directory, RunOptions{MCPServers: servers}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(contentPath)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"mcp":{"hound":{"type":"local","command":["hound-mcp","--stdio","free"],"environment":{"MODE":"keyless"},"enabled":true}}}`
	if string(content) != want {
		t.Fatalf("OPENCODE_CONFIG_CONTENT = %s, want %s", content, want)
	}
	arguments, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(arguments), "--auto") || strings.Contains(string(arguments), "--yolo") {
		t.Fatalf("OpenCode arguments enabled unsafe approval: %q", arguments)
	}
}

func TestOpenCodeNoMCPLeavesInlineConfigUnset(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, "state")
	wrapper := filepath.Join(directory, OpenCodeProvider)
	script := "#!/bin/sh\nif [ \"${OPENCODE_CONFIG_CONTENT+x}\" = x ]; then printf set > " + shellQuote(statePath) + "; else printf unset > " + shellQuote(statePath) + "; fi\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := (&Runner{AllowExecution: true}).RunWithOptions(t.Context(), OpenCodeProvider, "test", directory, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	state, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(state) != "unset" {
		t.Fatalf("OPENCODE_CONFIG_CONTENT state = %q, want unset", state)
	}
}

func TestOpenCodeDisabledWebSearchIsEnforcedInProcessLocalConfig(t *testing.T) {
	directory := t.TempDir()
	contentPath := filepath.Join(directory, "content.json")
	wrapper := filepath.Join(directory, OpenCodeProvider)
	script := "#!/bin/sh\nprintf '%s' \"$OPENCODE_CONFIG_CONTENT\" > " + shellQuote(contentPath) + "\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := (&Runner{AllowExecution: true}).RunWithOptions(t.Context(), OpenCodeProvider, "test", directory, RunOptions{WebSearch: "disabled"}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(contentPath)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"permission":{"webfetch":"deny","websearch":"deny"}}`
	if string(content) != want {
		t.Fatalf("OPENCODE_CONFIG_CONTENT = %s, want %s", content, want)
	}
}
