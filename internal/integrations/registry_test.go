package integrations

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestAllowedCommandSpecsAreExactReadOnlyProbes(t *testing.T) {
	got := AllowedCommandSpecs()
	want := []CommandSpec{
		{Host: HostGrok, Kind: KindMCP, Program: "grok", Args: []string{"mcp", "list"}, Source: "grok:mcp:list"},
		{Host: HostGrok, Kind: KindPlugin, Program: "grok", Args: []string{"plugin", "list"}, Source: "grok:plugin:list"},
		{Host: HostCodex, Kind: KindMCP, Program: "codex", Args: []string{"mcp", "list"}, Source: "codex:mcp:list"},
		{Host: HostOpenCode, Kind: KindMCP, Program: "opencode", Args: []string{"mcp", "list"}, Source: "opencode:mcp:list"},
		{Host: HostCursor, Kind: KindMCP, Program: "cursor-agent", Args: []string{"mcp", "list"}, Source: "cursor:mcp:list"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("allowlist = %#v, want %#v", got, want)
	}
	for _, spec := range got {
		if strings.Contains(strings.Join(spec.Args, " "), "install") || strings.Contains(strings.Join(spec.Args, " "), "auth") {
			t.Fatalf("non-read-only argument in %#v", spec)
		}
	}
}

func TestAllowedCommandSpecsAreDefensiveCopies(t *testing.T) {
	got := AllowedCommandSpecs()
	got[0].Args[0] = "mutated"
	got = append(got, CommandSpec{Program: "sh"})

	again := AllowedCommandSpecs()
	if again[0].Args[0] != "mcp" {
		t.Fatalf("allowlist was mutated through Args: %#v", again[0])
	}
	if len(again) != 5 {
		t.Fatalf("allowlist length = %d, want 5", len(again))
	}
}

func TestInspectUsesOnlyAllowlistedSpecsInStableOrder(t *testing.T) {
	runner := &fakeRunner{responses: map[string]fakeResponse{
		"grok:mcp:list":     {output: CommandOutput{Stdout: `[{"name":"github","status":"connected"}]`}},
		"grok:plugin:list":  {output: CommandOutput{Stdout: "browser-tools: enabled\n"}},
		"codex:mcp:list":    {output: CommandOutput{Stdout: `{"servers":[{"name":"codex-mcp","status":"disabled"}]}`}},
		"opencode:mcp:list": {output: CommandOutput{Stdout: "openagentfleet connected\n"}},
		"cursor:mcp:list":   {output: CommandOutput{}},
	}}

	got := Inspect(context.Background(), runner)
	if !reflect.DeepEqual(runner.calls, AllowedCommandSpecs()) {
		t.Fatalf("calls = %#v, want exact allowlist %#v", runner.calls, AllowedCommandSpecs())
	}
	want := []Record{
		{Host: HostGrok, Kind: KindMCP, Name: "github", Status: StatusAvailable, Source: "grok:mcp:list", Detail: "connected"},
		{Host: HostGrok, Kind: KindPlugin, Name: "browser-tools", Status: StatusAvailable, Source: "grok:plugin:list", Detail: "enabled"},
		{Host: HostCodex, Kind: KindMCP, Name: "codex-mcp", Status: StatusUnavailable, Source: "codex:mcp:list", Detail: "disabled"},
		{Host: HostOpenCode, Kind: KindMCP, Name: "openagentfleet", Status: StatusAvailable, Source: "opencode:mcp:list", Detail: "connected"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("records = %#v, want %#v", got, want)
	}
}

func TestInspectTurnsProbeErrorsIntoRedactedUnavailableRecords(t *testing.T) {
	secret := "ghp_1234567890abcdef"
	runner := &fakeRunner{responses: map[string]fakeResponse{
		"grok:mcp:list": {
			output: CommandOutput{Stderr: "Authorization: Bearer " + secret + " api_key=top-secret"},
			err:    fmt.Errorf("exit status 1; token=%s", secret),
		},
	}}

	got := Inspect(context.Background(), runner)
	if len(got) != 1 {
		t.Fatalf("records = %#v, want one unavailable record for the failed probe", got)
	}
	first := got[0]
	if first.Status != StatusUnavailable || first.Name != string(HostGrok) || first.Source != "grok:mcp:list" {
		t.Fatalf("unavailable record = %#v", first)
	}
	for _, record := range got {
		if strings.Contains(record.Detail, secret) || strings.Contains(record.Detail, "top-secret") {
			t.Fatalf("credential leaked in detail: %#v", record)
		}
	}
	if !strings.Contains(first.Detail, "redacted") {
		t.Fatalf("redacted error detail = %q", first.Detail)
	}
}

func TestInspectWithNilRunnerIsUnavailablePerProbe(t *testing.T) {
	got := Inspect(context.Background(), nil)
	if len(got) != len(AllowedCommandSpecs()) {
		t.Fatalf("records = %#v", got)
	}
	for _, record := range got {
		if record.Status != StatusUnavailable || record.Detail == "" {
			t.Fatalf("nil-runner record = %#v", record)
		}
	}
}

func TestJSONParserIsConservativeAndRedactsDetails(t *testing.T) {
	spec := AllowedCommandSpecs()[0]
	text := `{"servers":[
    {"name":"github","state":"connected","detail":"Bearer sk-secret-value"},
    {"name":"downstream","enabled":false},
    {"name":"token=do-not-store","status":"connected"},
    {"description":"missing name"}
]}`

	got := parseRecords(spec, text)
	if len(got) != 2 {
		t.Fatalf("records = %#v, want two safe named records", got)
	}
	if got[0].Name != "github" || got[0].Status != StatusAvailable || !strings.Contains(got[0].Detail, "redacted") {
		t.Fatalf("connected record = %#v", got[0])
	}
	if got[1].Name != "downstream" || got[1].Status != StatusUnavailable {
		t.Fatalf("disabled record = %#v", got[1])
	}
	for _, record := range got {
		if strings.Contains(record.Detail, "sk-secret-value") || strings.Contains(record.Name, "do-not-store") {
			t.Fatalf("unsafe value survived parsing: %#v", record)
		}
	}
}

func TestTextParserSkipsHeadersAndNonRegistryLines(t *testing.T) {
	spec := AllowedCommandSpecs()[1]
	text := "Plugins\nName    Status\n- browser-tools connected\n\nnot a registry command --api-key=secret\nterminal output\n"
	got := parseRecords(spec, text)
	want := []Record{{
		Host: HostGrok, Kind: KindPlugin, Name: "browser-tools", Status: StatusAvailable,
		Source: "grok:plugin:list", Detail: "connected",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("records = %#v, want %#v", got, want)
	}
}

func TestExecRunnerRejectsAlteredCommandsAndShells(t *testing.T) {
	runner := ExecRunner{}
	allowed := AllowedCommandSpecs()[0]
	allowed.Args = []string{"mcp", "list", "--json"}
	if _, err := runner.Run(context.Background(), allowed); err == nil || !strings.Contains(err.Error(), "not allowlisted") {
		t.Fatalf("altered command error = %v", err)
	}
	if _, err := runner.Run(context.Background(), CommandSpec{
		Host: HostGrok, Kind: KindMCP, Program: "sh", Args: []string{"-c", "touch should-not-run"}, Source: "test",
	}); err == nil || !strings.Contains(err.Error(), "not allowlisted") {
		t.Fatalf("shell command error = %v", err)
	}
}

func TestSafeDetailRedactsCredentialShapes(t *testing.T) {
	value := "api_key=abc password: hunter2 Authorization: Bearer abcdefghijkl xai-1234567890abcdef eyJhbGciOiJub25l.sig.payload"
	got := safeDetail(value)
	for _, secret := range []string{"abc", "hunter2", "abcdefghijkl", "xai-1234567890abcdef", "eyJhbGciOiJub25l.sig.payload"} {
		if strings.Contains(got, secret) {
			t.Fatalf("%q survived redaction in %q", secret, got)
		}
	}
	if !strings.Contains(got, "redacted") {
		t.Fatalf("redaction marker missing from %q", got)
	}
}

type fakeResponse struct {
	output CommandOutput
	err    error
}

type fakeRunner struct {
	responses map[string]fakeResponse
	calls     []CommandSpec
}

func (runner *fakeRunner) Run(ctx context.Context, spec CommandSpec) (CommandOutput, error) {
	if ctx == nil {
		return CommandOutput{}, errors.New("nil context")
	}
	runner.calls = append(runner.calls, cloneSpec(spec))
	response, exists := runner.responses[spec.Source]
	if !exists {
		return CommandOutput{}, nil
	}
	return response.output, response.err
}
