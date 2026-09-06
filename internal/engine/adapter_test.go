package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
)

type fakeRunner struct {
	lines   []harness.OutputLine
	err     error
	output  string
	gotProv string
}

func (f *fakeRunner) RunWithOptions(_ context.Context, provider, _ string, _ string, options harness.RunOptions) (string, error) {
	f.gotProv = provider
	for _, line := range f.lines {
		if options.OnLine != nil {
			options.OnLine(line)
		}
	}
	return f.output, f.err
}

func TestCapabilitiesForKnownEngines(t *testing.T) {
	wantMCP := map[ID]bool{
		Grok: true, GrokBuild: true, CodexAppServer: true, OpenCode: true,
		Claude: false, Codex: false, Pi: false, Cursor: false,
	}
	for _, id := range []ID{Grok, GrokBuild, Claude, Codex, CodexAppServer, OpenCode, Pi, Cursor} {
		caps := CapabilitiesFor(id)
		if caps.MCP != wantMCP[id] {
			t.Fatalf("%s MCP = %v, want %v", id, caps.MCP, wantMCP[id])
		}
		if !caps.Tools || !caps.Streaming {
			t.Fatalf("%s missing tools/streaming: %+v", id, caps)
		}
		if id == Pi && caps.ComputerMCP {
			t.Fatalf("pi computer MCP = true")
		}
		if id == Cursor && caps.ComputerMCP {
			t.Fatalf("cursor computer MCP = true")
		}
	}
	unknown := CapabilitiesFor("nope")
	if unknown.MCP || unknown.Tools || unknown.ComputerMCP || !unknown.Streaming {
		t.Fatalf("unknown caps = %+v", unknown)
	}
}

func TestHarnessAdapterNormalizesEventsAndCompletes(t *testing.T) {
	runner := &fakeRunner{
		lines: []harness.OutputLine{
			{Stream: "stdout", Type: "thought", Text: "planning"},
			{Stream: "stdout", Type: "text", Text: "hello"},
			{Stream: "stdout", Type: "tool_call", Text: `{"name":"browser_status"}`},
		},
		output: "hello",
	}
	adapter := NewHarnessAdapter(GrokBuild, runner)
	if adapter.GetCapabilities().MCP != true {
		t.Fatal("expected grok_build MCP capability")
	}
	state, err := adapter.GetAuthState(context.Background())
	if err != nil || state.Authenticated || !state.Available {
		t.Fatalf("auth = %+v, err=%v", state, err)
	}
	var events []Event
	output, err := adapter.RunTurn(context.Background(), TurnContext{AgentID: "bot-1", TurnID: "run-1", ComputerID: "workspace", Prompt: "hi"}, func(event Event) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if output != "hello" {
		t.Fatalf("output = %q", output)
	}
	if runner.gotProv != "grok" {
		t.Fatalf("provider = %q", runner.gotProv)
	}
	types := eventTypes(events)
	want := []string{
		domain.EventAgentTurnStarted,
		domain.EventAgentThinking,
		domain.EventAgentMessageDelta,
		domain.EventAgentToolStarted,
		domain.EventAgentMessageCompleted,
		domain.EventAgentTurnCompleted,
	}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", types, want)
	}
	if events[3].ToolName != "browser_status" {
		t.Fatalf("tool name = %q", events[3].ToolName)
	}
	var payload map[string]string
	if err := json.Unmarshal(events[2].Data, &payload); err != nil || payload["text"] != "hello" {
		t.Fatalf("delta payload = %s", events[2].Data)
	}
}

func TestHarnessAdapterFailureEmitsFailed(t *testing.T) {
	runner := &fakeRunner{err: errors.New("cli missing")}
	adapter := NewHarnessAdapter(Claude, runner)
	var events []Event
	_, err := adapter.RunTurn(context.Background(), TurnContext{AgentID: "bot-1", TurnID: "run-2"}, func(event Event) {
		events = append(events, event)
	})
	if err == nil {
		t.Fatal("expected failure")
	}
	types := eventTypes(events)
	if types[0] != domain.EventAgentTurnStarted || types[len(types)-1] != domain.EventAgentTurnFailed {
		t.Fatalf("events = %v", types)
	}

	_, err = NewHarnessAdapter(Claude, &fakeRunner{err: errors.New("cli missing")}).RunTurn(
		context.Background(), TurnContext{Prompt: "hi"}, nil)
	if err == nil {
		t.Fatal("expected failure with nil emit")
	}
}

func TestHarnessAdapterCancelEmitsCancelled(t *testing.T) {
	runner := &fakeRunner{err: context.Canceled}
	adapter := NewHarnessAdapter(OpenCode, runner)
	var events []Event
	_, err := adapter.RunTurn(context.Background(), TurnContext{TurnID: "run-3"}, func(event Event) {
		events = append(events, event)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if eventTypes(events)[len(events)-1] != domain.EventAgentTurnCancelled {
		t.Fatalf("events = %v", eventTypes(events))
	}
}

func TestRegistryGetAndList(t *testing.T) {
	registry := DefaultRegistry(&fakeRunner{})
	adapter, ok := registry.Get(Pi)
	if !ok || adapter.ID() != Pi {
		t.Fatalf("missing pi adapter")
	}
	if adapter.GetCapabilities().MCP {
		t.Fatal("pi must not advertise MCP")
	}
	listed := registry.List()
	if len(listed) != 8 {
		t.Fatalf("list = %d", len(listed))
	}
	replacement := NewHarnessAdapter(Pi, &fakeRunner{output: "replaced"})
	if err := registry.Register(replacement); err != nil {
		t.Fatal(err)
	}
	got, ok := registry.Get(Pi)
	if !ok || got != replacement {
		t.Fatal("same id must overwrite")
	}
}

func eventTypes(events []Event) []string {
	types := make([]string, len(events))
	for i, event := range events {
		types[i] = event.Type
	}
	return types
}
