package harness

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOpenCodeUsesExplicitBundledBinary(t *testing.T) {
	t.Setenv("OPENAGENTFLEET_OPENCODE_BINARY", "/Applications/OpenAgentFleet.app/Contents/MacOS/opencode")
	command, err := BuildCommand(OpenCodeProvider, "test", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if command.Program != "/Applications/OpenAgentFleet.app/Contents/MacOS/opencode" {
		t.Fatalf("program = %q", command.Program)
	}
}

func TestBuildCommandKeepsProviderArgumentsStructured(t *testing.T) {
	for _, provider := range []string{"pi", "claude", "codex", "grok", "opencode", "cursor"} {
		options := CommandOptions{}
		if provider == "pi" {
			options.PermissionMode = "read_only"
		}
		command, err := BuildCommandWithOptions(provider, "hello; do not shell expand", "/tmp/work", options)
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		if command.Program == "" || command.Dir == "" {
			t.Fatalf("%s: incomplete command: %#v", provider, command)
		}
		joined := strings.Join(command.Args, " ")
		if provider == "pi" {
			if strings.Contains(joined, "hello; do not shell expand") {
				t.Fatalf("Pi RPC must not put the prompt on the command line: %#v", command.Args)
			}
			continue
		}
		if !strings.Contains(joined, "hello; do not shell expand") {
			t.Fatalf("%s: prompt was not preserved: %#v", provider, command.Args)
		}
	}
}

func TestCursorCommandDoesNotEnableBroadAutoApproval(t *testing.T) {
	command, err := BuildCommandWithOptions("cursor", "inspect safely", "/tmp/work", CommandOptions{SessionID: "chat-1", Model: "sonnet"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(command.Args, " ")
	for _, unsafeFlag := range []string{"--force", "--trust", "--auto-review", "--yolo"} {
		if strings.Contains(joined, unsafeFlag) {
			t.Fatalf("cursor command must not include %s: %s", unsafeFlag, joined)
		}
	}
	if !strings.Contains(joined, "--output-format stream-json") || !strings.Contains(joined, "--resume chat-1") {
		t.Fatalf("cursor command missing headless/session arguments: %s", joined)
	}
}

func TestOpenCodeCommandUsesPureJSONAndSafeExplicitOptions(t *testing.T) {
	workdir, err := filepath.Abs("/tmp/work")
	if err != nil {
		t.Fatal(err)
	}
	command, err := BuildCommandWithOptions(OpenCodeProvider, "inspect safely", "/tmp/work", CommandOptions{
		SessionID:       "ses_123",
		Model:           "openai/gpt-5",
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"run", "--pure", "--format", "json", "--dir", workdir,
		"--session", "ses_123", "--model", "openai/gpt-5", "--variant", "high",
		"inspect safely",
	}
	if !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("OpenCode args = %#v, want %#v", command.Args, want)
	}
	if strings.Contains(strings.Join(command.Args, " "), "--auto") {
		t.Fatalf("OpenCode command enabled dangerous auto approval: %#v", command.Args)
	}
}

func TestOpenCodeCommandOmitsDefaultVariant(t *testing.T) {
	command, err := BuildCommandWithOptions(OpenCodeProvider, "inspect safely", "/tmp/work", CommandOptions{ReasoningEffort: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(command.Args, " "), "--variant") {
		t.Fatalf("default reasoning must use the model's default variant: %#v", command.Args)
	}
}

func TestValidateOpenCodeOptionsRejectsUnmappableControls(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		reasoning  string
		tier       string
		permission string
		want       string
	}{
		{name: "model", model: "gpt-5", want: "provider/model"},
		{name: "reasoning", model: "openai/gpt-5", reasoning: "extreme", want: "variant"},
		{name: "tier", model: "openai/gpt-5", tier: "priority", want: "service-tier"},
		{name: "read only", model: "openai/gpt-5", permission: "read_only", want: "unsupported"},
		{name: "auto", model: "openai/gpt-5", permission: "auto", want: "dangerous"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateOpenCodeOptions(test.model, test.reasoning, test.tier, test.permission)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateOpenCodeOptions() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestBuildCommandRejectsUnknownProvider(t *testing.T) {
	if _, err := BuildCommand("unknown", "prompt", "."); err == nil {
		t.Fatal("expected unknown provider error")
	}
}
