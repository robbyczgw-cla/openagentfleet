package harness

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPiToolsNeverGrantBash(t *testing.T) {
	t.Parallel()

	readOnly, err := PiToolsForPermission("read_only")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := PiToolsForPermission("workspace")
	if err != nil {
		t.Fatal(err)
	}
	if readOnly != "read,grep,find,ls" {
		t.Fatalf("read_only tools = %q", readOnly)
	}
	if workspace != "read,grep,find,ls,write,edit" {
		t.Fatalf("workspace tools = %q", workspace)
	}
	for _, tools := range []string{readOnly, workspace} {
		for _, name := range strings.Split(tools, ",") {
			if name == "bash" {
				t.Fatal("bash was granted")
			}
		}
	}
}

func TestValidatePiOptionsRejectsUnenforceableModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		model      string
		reasoning  string
		tier       string
		permission string
		want       string
	}{
		{name: "ask", permission: "ask", want: "read_only or workspace"},
		{name: "auto", permission: "auto", want: "unrestricted"},
		{name: "empty", want: "read_only or workspace"},
		{name: "tier", permission: "read_only", tier: "priority", want: "service-tier"},
		{name: "spaces", model: "openai / gpt", permission: "read_only", want: "whitespace"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidatePiOptions(test.model, test.reasoning, test.tier, test.permission)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestBuildPiRPCCommandUsesExactToolsAndNoPrompt(t *testing.T) {
	t.Setenv("OPENAGENTFLEET_PI_BINARY", "/opt/pi")
	command, err := BuildCommandWithOptions("pi", "must not appear", "/tmp/work", CommandOptions{
		Model:           "openai/gpt-4o",
		ReasoningEffort: "high",
		PermissionMode:  "workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--mode", "rpc", "--no-session", "--tools", "read,grep,find,ls,write,edit", "--model", "openai/gpt-4o:high"}
	if command.Program != "/opt/pi" || !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("command = %#v", command)
	}
	if strings.Contains(strings.Join(command.Args, " "), "must not appear") {
		t.Fatal("prompt leaked onto argv")
	}
}

func TestPiRPCRunCollectsAssistantTextAndHonorsToolAllowlist(t *testing.T) {
	helper := writePiRPCHelper(t, false)
	t.Setenv("OPENAGENTFLEET_PI_BINARY", helper)

	runner := &Runner{AllowExecution: true}
	output, err := runner.RunWithOptions(t.Context(), "pi", "Review the module", t.TempDir(), RunOptions{
		PermissionMode: "read_only",
		Model:          "openai/gpt-4o",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(AssistantText("pi", output), "RPC_OK") {
		t.Fatalf("assistant text missing: %s", output)
	}
	payload, err := os.ReadFile(helper + ".args")
	if err != nil {
		t.Fatal(err)
	}
	logged := string(payload)
	if !strings.Contains(logged, "--tools read,grep,find,ls") || strings.Contains(logged, "bash") {
		t.Fatalf("helper args = %q", logged)
	}
}

func TestPiRPCRunCancelsWithAbort(t *testing.T) {
	t.Setenv("OPENAGENTFLEET_PI_BINARY", writePiRPCHelper(t, true))

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	runner := &Runner{AllowExecution: true}
	_, err := runner.RunWithOptions(ctx, "pi", "hold", t.TempDir(), RunOptions{PermissionMode: "read_only"})
	if err == nil {
		t.Fatal("held helper returned success")
	}
}

func TestValidatePiLeadOptionsRejectsAuto(t *testing.T) {
	t.Parallel()

	err := ValidatePiLeadOptions("openai/gpt-4o", "", "", "auto")
	if err == nil || !strings.Contains(err.Error(), "auto") {
		t.Fatalf("error = %v, want auto rejection", err)
	}
	if err := ValidatePiLeadOptions("openai/gpt-4o", "", "", "ask"); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePiLeadOptions("openai / gpt", "", "", "ask"); err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("lead model whitespace = %v", err)
	}
}

func TestPiLeadToolsGrantBashOnlyForAsk(t *testing.T) {
	t.Parallel()

	readOnly, err := PiLeadToolsForPermission("read_only")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := PiLeadToolsForPermission("workspace")
	if err != nil {
		t.Fatal(err)
	}
	ask, err := PiLeadToolsForPermission("ask")
	if err != nil {
		t.Fatal(err)
	}
	if readOnly != "read,grep,find,ls" || strings.Contains(readOnly, "bash") {
		t.Fatalf("read_only tools = %q", readOnly)
	}
	if workspace != "read,grep,find,ls,write,edit" || strings.Contains(workspace, "bash") {
		t.Fatalf("workspace tools = %q", workspace)
	}
	if ask != "read,grep,find,ls,write,edit,bash" {
		t.Fatalf("ask tools = %q", ask)
	}
}

func TestBuildPiRPCCommandWorkerNeverLoadsExtensionOrBash(t *testing.T) {
	t.Setenv("OPENAGENTFLEET_PI_BINARY", "/opt/pi")
	for _, role := range []string{"", "worker"} {
		command, err := BuildCommandWithOptions("pi", "must not appear", "/tmp/work", CommandOptions{
			PermissionMode: "workspace",
			Role:           role,
		})
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(command.Args, " ")
		if strings.Contains(joined, "bash") || containsFlag(command.Args, "-e") || containsFlag(command.Args, "--extension") {
			t.Fatalf("worker role %q command = %#v", role, command.Args)
		}
		if strings.Contains(joined, "must not appear") {
			t.Fatal("prompt leaked onto argv")
		}
	}
}

func TestBuildPiRPCCommandLeadWorkspaceAndReadOnlyStaySandboxed(t *testing.T) {
	t.Setenv("OPENAGENTFLEET_PI_BINARY", "/opt/pi")
	for _, permission := range []string{"read_only", "workspace"} {
		command, err := BuildCommandWithOptions("pi", "must not appear", "/tmp/work", CommandOptions{
			PermissionMode: permission,
			Role:           "lead",
		})
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(command.Args, " ")
		if strings.Contains(joined, "bash") || containsFlag(command.Args, "-e") || containsFlag(command.Args, "--extension") {
			t.Fatalf("lead %s command = %#v", permission, command.Args)
		}
	}
}

func TestBuildPiRPCCommandLeadAskLoadsExtensionAndBash(t *testing.T) {
	t.Setenv("OPENAGENTFLEET_PI_BINARY", "/opt/pi")
	command, err := BuildCommandWithOptions("pi", "must not appear", "/tmp/work", CommandOptions{
		PermissionMode: "ask",
		Role:           "lead",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(command.Args, " ")
	if !strings.Contains(joined, "bash") || !containsFlag(command.Args, "-e") {
		t.Fatalf("lead ask command = %#v", command.Args)
	}
	if containsFlag(command.Args, "--extension") && !containsFlag(command.Args, "-e") {
		t.Fatal("expected -e for the bundled controller")
	}
	if strings.Contains(joined, "must not appear") {
		t.Fatal("prompt leaked onto argv")
	}
	extensionPath := flagValue(command.Args, "-e")
	if extensionPath == "" {
		t.Fatal("missing extension path")
	}
	source, err := os.ReadFile(extensionPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "ctx.ui.confirm") || strings.Contains(string(source), "registerTool") {
		t.Fatalf("controller source unexpected: %s", source)
	}
}

func TestPiRPCLeadConfirmDoesNotHang(t *testing.T) {
	t.Setenv("OPENAGENTFLEET_PI_BINARY", writePiRPCHelperWithConfirm(t))

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	runner := &Runner{AllowExecution: true}
	output, err := runner.RunWithOptions(ctx, "pi", "run bash", t.TempDir(), RunOptions{
		Role:           "lead",
		PermissionMode: "ask",
		OnPermission: func(context.Context, PermissionRequest) (PermissionDecision, error) {
			return PermissionDecision{Outcome: "selected", OptionID: "allow"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(AssistantText("pi", output), "RPC_OK") {
		t.Fatalf("assistant text missing: %s", output)
	}
}

func TestPiRPCLeadConfirmDenyStillSettles(t *testing.T) {
	t.Setenv("OPENAGENTFLEET_PI_BINARY", writePiRPCHelperWithConfirm(t))

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	runner := &Runner{AllowExecution: true}
	output, err := runner.RunWithOptions(ctx, "pi", "run bash", t.TempDir(), RunOptions{
		Role:           "lead",
		PermissionMode: "ask",
		OnPermission: func(context.Context, PermissionRequest) (PermissionDecision, error) {
			return PermissionDecision{Outcome: "cancelled"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(AssistantText("pi", output), "RPC_OK") {
		t.Fatalf("assistant text missing: %s", output)
	}
}

func writePiRPCHelper(t *testing.T, hold bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pi")
	script := `#!/bin/sh
printf '%s\n' "$*" > "$0.args"
`
	if hold {
		script += `while IFS= read -r line; do
  case "$line" in
    *'"abort"'*) exit 0 ;;
  esac
done
exit 0
`
	} else {
		script += `IFS= read -r _
printf '%s\n' '{"id":"prompt-1","type":"response","command":"prompt","success":true}'
printf '%s\n' '{"type":"agent_start"}'
printf '%s\n' '{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"RPC_OK"}}'
printf '%s\n' '{"type":"agent_settled"}'
`
	}
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writePiRPCHelperWithConfirm(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pi")
	script := `#!/usr/bin/env python3
import json
import pathlib
import sys

pathlib.Path(sys.argv[0] + ".args").write_text(" ".join(sys.argv[1:]) + "\n")
sys.stdin.readline()
print('{"id":"prompt-1","type":"response","command":"prompt","success":true}', flush=True)
print('{"type":"extension_ui_request","id":"ui-1","method":"confirm","title":"Allow bash command?","message":"ls"}', flush=True)
reply = sys.stdin.readline()
payload = json.loads(reply)
if payload.get("type") != "extension_ui_response":
    sys.exit(2)
print('{"type":"agent_start"}', flush=True)
print('{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"RPC_OK"}}', flush=True)
print('{"type":"agent_settled"}', flush=True)
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func containsFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}

func flagValue(args []string, flag string) string {
	for index, arg := range args {
		if arg == flag && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}
