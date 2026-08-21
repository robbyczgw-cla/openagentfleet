package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestValidatePiOptionsAndLeadOptionsCoverPermissionEdges(t *testing.T) {
	t.Parallel()

	longModel := strings.Repeat("m", 129)
	workerCases := []struct {
		name, model, reasoning, tier, permission, want string
	}{
		{name: "ask", permission: "ask", want: "read_only or workspace"},
		{name: "provider_default", permission: "provider_default", want: "read_only or workspace"},
		{name: "default", permission: "default", want: "read_only or workspace"},
		{name: "empty", want: "read_only or workspace"},
		{name: "yolo", permission: "yolo", want: "unrestricted"},
		{name: "auto", permission: "auto", want: "unrestricted"},
		{name: "unknown", permission: "host", want: "unsupported"},
		{name: "priority", permission: "read_only", tier: "priority", want: "service-tier"},
		{name: "flex", permission: "workspace", tier: "flex", want: "service-tier"},
		{name: "whitespace", model: "openai / gpt", permission: "read_only", want: "whitespace"},
		{name: "tab", model: "openai/\tgpt", permission: "read_only", want: "whitespace"},
		{name: "newline", model: "openai/gpt\n", permission: "read_only", want: "whitespace"},
		{name: "too long", model: longModel, permission: "read_only", want: "at most 128"},
		{name: "reasoning", permission: "read_only", reasoning: "turbo", want: "reasoning"},
	}
	for _, test := range workerCases {
		t.Run("worker/"+test.name, func(t *testing.T) {
			err := ValidatePiOptions(test.model, test.reasoning, test.tier, test.permission)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}

	leadRejects := []struct {
		name, model, reasoning, tier, permission, want string
	}{
		{name: "auto", permission: "auto", want: "auto"},
		{name: "yolo", permission: "yolo", want: "unrestricted"},
		{name: "provider_default", permission: "provider_default", want: "read_only, workspace, or ask"},
		{name: "default", permission: "default", want: "read_only, workspace, or ask"},
		{name: "empty", want: "read_only, workspace, or ask"},
		{name: "tier", permission: "ask", tier: "priority", want: "service-tier"},
		{name: "whitespace", model: " openai/gpt", permission: "ask", want: "whitespace"},
		{name: "too long", model: longModel, permission: "workspace", want: "at most 128"},
		{name: "reasoning", permission: "ask", reasoning: "extreme", want: "reasoning"},
	}
	for _, test := range leadRejects {
		t.Run("lead/"+test.name, func(t *testing.T) {
			err := ValidatePiLeadOptions(test.model, test.reasoning, test.tier, test.permission)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}

	for _, permission := range []string{"read_only", "workspace"} {
		if err := ValidatePiOptions("openai/gpt-4o", "xhigh", "", permission); err != nil {
			t.Fatalf("worker %s xhigh: %v", permission, err)
		}
	}
	for _, permission := range []string{"read_only", "workspace", "ask"} {
		if err := ValidatePiLeadOptions("", "max", "default", permission); err != nil {
			t.Fatalf("lead %s empty model: %v", permission, err)
		}
	}
}

func TestBuildPiRPCCommandRejectsLeadAskWithoutValidRoleSandboxSwap(t *testing.T) {
	t.Setenv("OPENAGENTFLEET_PI_BINARY", "/opt/pi")
	if _, err := BuildCommandWithOptions("pi", "prompt", t.TempDir(), CommandOptions{
		PermissionMode: "ask",
	}); err == nil || !strings.Contains(err.Error(), "read_only or workspace") {
		t.Fatalf("worker ask leaked through: %v", err)
	}
	if _, err := BuildCommandWithOptions("pi", "prompt", t.TempDir(), CommandOptions{
		Role:           "LEAD",
		PermissionMode: "ask",
	}); err == nil {
		t.Fatal("case-shifted Role was treated as lead")
	}
}

func TestPiRPCRejectsMCPBeforeLaunchEvenForLeadAsk(t *testing.T) {
	runner := &Runner{AllowExecution: true}
	_, err := runner.RunWithOptions(t.Context(), "pi", "search", t.TempDir(), RunOptions{
		Role:           "lead",
		PermissionMode: "ask",
		MCPServers:     []MCPServerSpec{{Name: "hound", Command: "must-not-launch"}},
	})
	if err == nil || !strings.Contains(err.Error(), "MCP server injection is unsupported") {
		t.Fatalf("error = %v", err)
	}
}

func TestPiRPCDisabledExecutionNeverStartsHelper(t *testing.T) {
	helper := writePiRPCHelper(t, false)
	t.Setenv("OPENAGENTFLEET_PI_BINARY", helper)
	_, err := (&Runner{}).RunWithOptions(t.Context(), "pi", "nope", t.TempDir(), RunOptions{PermissionMode: "read_only"})
	if !errors.Is(err, ErrExecutionDisabled) {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(helper + ".args"); !os.IsNotExist(statErr) {
		t.Fatal("disabled runner still launched Pi")
	}
}

func TestPiRPCPromptRejectionIsSurfaced(t *testing.T) {
	t.Setenv("OPENAGENTFLEET_PI_BINARY", writePiRPCHelperPromptReject(t, "No API key found. Use /login."))
	_, err := (&Runner{AllowExecution: true}).RunWithOptions(t.Context(), "pi", "hello", t.TempDir(), RunOptions{PermissionMode: "read_only"})
	if err == nil || !strings.Contains(err.Error(), "Pi RPC rejected the prompt") || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("error = %v", err)
	}
}

func TestPiRPCRedactsSecretsInPromptRejection(t *testing.T) {
	t.Setenv("OPENAGENTFLEET_PI_BINARY", writePiRPCHelperPromptReject(t, `api_key="sk-secret-value-123456"`))
	_, err := (&Runner{AllowExecution: true}).RunWithOptions(t.Context(), "pi", "hello", t.TempDir(), RunOptions{PermissionMode: "read_only"})
	if err == nil || strings.Contains(err.Error(), "sk-secret-value-123456") {
		t.Fatalf("secret leaked: %v", err)
	}
}

func TestPiRPCExitsBeforeSettle(t *testing.T) {
	t.Setenv("OPENAGENTFLEET_PI_BINARY", writePiRPCHelperExit(t, 1))
	_, err := (&Runner{AllowExecution: true}).RunWithOptions(t.Context(), "pi", "hello", t.TempDir(), RunOptions{PermissionMode: "read_only"})
	if err == nil || !strings.Contains(err.Error(), "exited before the run settled") {
		t.Fatalf("error = %v", err)
	}
}

func TestPiRPCExtensionUIEdgesDoNotHang(t *testing.T) {
	t.Setenv("OPENAGENTFLEET_PI_BINARY", writePiRPCHelperUIKitchenSink(t))
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	output, err := (&Runner{AllowExecution: true}).RunWithOptions(ctx, "pi", "ui", t.TempDir(), RunOptions{
		Role:           "lead",
		PermissionMode: "ask",
		OnPermission: func(context.Context, PermissionRequest) (PermissionDecision, error) {
			return PermissionDecision{}, errors.New("approval store unavailable")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(AssistantText("pi", output), "RPC_OK") {
		t.Fatalf("output = %s", output)
	}
}

func TestPiRPCConfirmWithoutHandlerCancels(t *testing.T) {
	t.Setenv("OPENAGENTFLEET_PI_BINARY", writePiRPCHelperWithConfirm(t))
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	output, err := (&Runner{AllowExecution: true}).RunWithOptions(ctx, "pi", "bash", t.TempDir(), RunOptions{
		Role:           "lead",
		PermissionMode: "ask",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(AssistantText("pi", output), "RPC_OK") {
		t.Fatalf("output = %s", output)
	}
}

func TestScanPiRPCLineOnlySplitsOnLF(t *testing.T) {
	t.Parallel()

	lineSep := "\u2028"
	payload := `{"type":"message_update","text":"keep` + lineSep + `together"}` + "\r\n" + `{"type":"agent_settled"}` + "\n"
	advance, token, err := scanPiRPCLine([]byte(payload), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(token), "keep"+lineSep+"together") {
		t.Fatalf("token = %q", token)
	}
	if payload[advance-1] != '\n' {
		t.Fatalf("advance landed on %q", payload[advance-1])
	}
	if !utf8.Valid(token) {
		t.Fatal("token is not valid UTF-8")
	}
}

func TestMaterializePiLeadControllerIsIdempotent(t *testing.T) {
	first, err := materializePiLeadController()
	if err != nil {
		t.Fatal(err)
	}
	second, err := materializePiLeadController()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("paths %q vs %q", first, second)
	}
	source, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "Allow bash command?") {
		t.Fatalf("controller = %s", source)
	}
}

func writePiRPCHelperPromptReject(t *testing.T, message string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Pi RPC helpers are POSIX scripts")
	}
	path := filepath.Join(t.TempDir(), "pi")
	script := `#!/usr/bin/env python3
import json, pathlib, sys
pathlib.Path(sys.argv[0] + ".args").write_text(" ".join(sys.argv[1:]) + "\n")
sys.stdin.readline()
print(json.dumps({"id":"prompt-1","type":"response","command":"prompt","success":False,"error":sys.argv[1]}), flush=True)
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	// Re-write with the error baked in to avoid argv quoting issues.
	script = `#!/usr/bin/env python3
import json, pathlib, sys
pathlib.Path(sys.argv[0] + ".args").write_text(" ".join(sys.argv[1:]) + "\n")
sys.stdin.readline()
print(json.dumps({"id":"prompt-1","type":"response","command":"prompt","success":False,"error":` + jsonQuote(message) + `}), flush=True)
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func jsonQuote(value string) string {
	encoded, _ := jsonMarshalForTest(value)
	return string(encoded)
}

func jsonMarshalForTest(value string) ([]byte, error) {
	return []byte(`"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`), nil
}

func writePiRPCHelperExit(t *testing.T, code int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Pi RPC helpers are POSIX scripts")
	}
	path := filepath.Join(t.TempDir(), "pi")
	script := `#!/bin/sh
printf '%s\n' "$*" > "$0.args"
exit ` + strconv.Itoa(code) + `
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writePiRPCHelperUIKitchenSink(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Pi RPC helpers are POSIX scripts")
	}
	path := filepath.Join(t.TempDir(), "pi")
	script := `#!/usr/bin/env python3
import json, pathlib, sys
pathlib.Path(sys.argv[0] + ".args").write_text(" ".join(sys.argv[1:]) + "\n")
sys.stdin.readline()
print('{"id":"prompt-1","type":"response","command":"prompt","success":true}', flush=True)
print('{"type":"extension_ui_request","id":"n1","method":"notify","message":"ignore"}', flush=True)
print('{"type":"extension_ui_request","id":"s1","method":"select","options":["a","b"]}', flush=True)
print(sys.stdin.readline(), file=sys.stderr, flush=True)
print('{"type":"extension_ui_request","id":"i1","method":"input"}', flush=True)
print(sys.stdin.readline(), file=sys.stderr, flush=True)
print('{"type":"extension_ui_request","id":"c1","method":"confirm","title":"Allow bash command?","message":"rm -rf /"}', flush=True)
print(sys.stdin.readline(), file=sys.stderr, flush=True)
print('{"type":"agent_start"}', flush=True)
print('{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"RPC_OK"}}', flush=True)
print('{"type":"agent_settled"}', flush=True)
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
