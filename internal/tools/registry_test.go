package tools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestLookupCanonicalAndAlias(t *testing.T) {
	registry := NewDefaultRegistry()
	canonical, ok := registry.Lookup("browser.navigate")
	if !ok || canonical.Name != "browser.navigate" {
		t.Fatalf("canonical lookup = %+v, ok=%v", canonical, ok)
	}
	alias, ok := registry.Lookup("browser_navigate")
	if !ok || alias.Name != "browser.navigate" {
		t.Fatalf("alias lookup = %+v, ok=%v", alias, ok)
	}
	if canonical.Description != alias.Description {
		t.Fatalf("alias resolved to a different tool")
	}
	if _, ok := registry.Lookup("not.a.tool"); ok {
		t.Fatal("unknown tool was found")
	}
}

func TestDefaultCatalogCoversExistingMCPTools(t *testing.T) {
	registry := NewDefaultRegistry()
	want := []struct {
		canonical string
		alias     string
	}{
		{"browser.status", "browser_status"},
		{"browser.start", "browser_start"},
		{"browser.navigate", "browser_navigate"},
		{"browser.snapshot", "browser_snapshot"},
		{"browser.click", "browser_click"},
		{"browser.type", "browser_type"},
		{"browser.press", "browser_press"},
		{"browser.scroll", "browser_scroll"},
		{"browser.screenshot", "browser_screenshot"},
		{"computer.snapshot", "computer_snapshot"},
		{"computer.screenshot", "computer_screenshot"},
		{"computer.click", "computer_click"},
		{"computer.type", "computer_type"},
		{"computer.press", "computer_press"},
		{"computer.scroll", "computer_scroll"},
		{"agent.list", "list_agents"},
		{"agent.message", "message_agent"},
		{"agent.delegate", "delegate_to_agent"},
		{"agent.task_status", "get_agent_task_status"},
	}
	listed := registry.List()
	if len(listed) != len(want) {
		t.Fatalf("List() count = %d, want %d", len(listed), len(want))
	}
	for i, item := range want {
		canonical, ok := registry.Lookup(item.canonical)
		if !ok {
			t.Fatalf("missing canonical %q", item.canonical)
		}
		alias, ok := registry.Lookup(item.alias)
		if !ok || alias.Name != item.canonical {
			t.Fatalf("alias %q = %+v ok=%v", item.alias, alias, ok)
		}
		if listed[i].Name != item.canonical {
			t.Errorf("List()[%d] = %q, want %q", i, listed[i].Name, item.canonical)
		}
		if canonical.Execute != nil {
			t.Errorf("%s Execute is bound; catalog tools are definition-only", item.canonical)
		}
	}
}

func TestExecuteRejectsMissingCapability(t *testing.T) {
	registry := NewDefaultRegistry()
	_, err := registry.Execute(ExecutionContext{AgentID: "bot-1", TurnID: "run-1"}, "browser.navigate", json.RawMessage(`{"url":"https://example.com"}`))
	if !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("err = %v", err)
	}
	_, err = registry.Execute(ExecutionContext{GrantedCapabilities: []string{CapComputerView}}, "browser_navigate", json.RawMessage(`{"url":"https://example.com"}`))
	if !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("partial grant err = %v", err)
	}
}

func TestExecuteRejectsMissingRequiredField(t *testing.T) {
	registry := NewDefaultRegistry()
	ctx := ExecutionContext{GrantedCapabilities: []string{CapComputerControl}}
	_, err := registry.Execute(ctx, "browser.navigate", json.RawMessage(`{}`))
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v", err)
	}
	_, err = registry.Execute(ctx, "browser.navigate", json.RawMessage(`{"href":"https://example.com"}`))
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("wrong key err = %v", err)
	}
	_, err = registry.Execute(ExecutionContext{GrantedCapabilities: []string{CapAgentCollaborate}}, "agent.message", json.RawMessage(`{"agent_id":"bot-2"}`))
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "content") {
		t.Fatalf("collab err = %v", err)
	}
}

func TestExecuteUnboundAfterValidation(t *testing.T) {
	registry := NewDefaultRegistry()
	ctx := ExecutionContext{GrantedCapabilities: []string{CapComputerControl}, AgentID: "bot-1", TurnID: "run-1", ComputerID: "workspace"}
	_, err := registry.Execute(ctx, "browser_navigate", json.RawMessage(`{"url":"https://example.com"}`))
	if !errors.Is(err, ErrExecuteUnbound) {
		t.Fatalf("err = %v", err)
	}
}

func TestBoundExecuteRuns(t *testing.T) {
	registry := NewRegistry()
	called := false
	if err := registry.Register(Tool{
		Name:        "files.read",
		InputSchema: objectSchema(map[string]any{"path": map[string]any{"type": "string"}}, "path"),
		Execute: func(_ ExecutionContext, input json.RawMessage) (Result, error) {
			called = true
			return Result{Content: string(input)}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := registry.Execute(ExecutionContext{}, "files.read", json.RawMessage(`{"path":"/workspace/a"}`))
	if err != nil || !called || result.Content == "" {
		t.Fatalf("result = %+v err = %v", result, err)
	}
}

func TestMCPDescriptorsTranslateCanonicalTools(t *testing.T) {
	registry := NewDefaultRegistry()
	tool, ok := registry.Lookup("agent.delegate")
	if !ok {
		t.Fatal("agent.delegate missing")
	}
	descriptors := MCPDescriptors([]Tool{tool})
	if len(descriptors) != 2 {
		t.Fatalf("descriptor count = %d", len(descriptors))
	}
	names := map[string]bool{}
	for _, item := range descriptors {
		name, _ := item["name"].(string)
		description, _ := item["description"].(string)
		schema, ok := item["inputSchema"]
		if name == "" || description == "" || !ok {
			t.Fatalf("incomplete descriptor: %#v", item)
		}
		if _, exists := item["requiredCapabilities"]; exists {
			t.Fatalf("MCP descriptor leaked capabilities: %#v", item)
		}
		encoded, err := json.Marshal(schema)
		if err != nil || len(encoded) == 0 || string(encoded) == "null" {
			t.Fatalf("missing schema for %s: %v", name, err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(encoded, &parsed); err != nil {
			t.Fatalf("schema for %s: %v", name, err)
		}
		required, _ := parsed["required"].([]any)
		if len(required) != 2 {
			t.Fatalf("required = %#v", parsed["required"])
		}
		names[name] = true
	}
	if !names["agent.delegate"] || !names["delegate_to_agent"] {
		t.Fatalf("names = %v", names)
	}
}

func TestRegisterRejectsDuplicatesAndEmptyNames(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(Tool{Name: ""}); !errors.Is(err, ErrInvalidTool) {
		t.Fatalf("empty name err = %v", err)
	}
	if err := registry.Register(Tool{Name: "browser.status", Aliases: []string{"browser_status"}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(Tool{Name: "browser.status"}); !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("duplicate err = %v", err)
	}
	if err := registry.Register(Tool{Name: "other.tool", Aliases: []string{"browser_status"}}); !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("alias collision err = %v", err)
	}
}
