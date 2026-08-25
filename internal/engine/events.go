package engine

import (
	"encoding/json"
	"strings"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
)

// NormalizeOutputLine maps a provider stream line onto an OAF fleet event.
// The original text stays in Data so persistence does not lose the payload.
func NormalizeOutputLine(engineID ID, turn TurnContext, line harness.OutputLine) Event {
	eventType := domain.EventAgentMessageDelta
	toolName := ""
	switch strings.ToLower(strings.TrimSpace(line.Type)) {
	case "thought", "thinking", "reason", "reasoning":
		eventType = domain.EventAgentThinking
	case "tool", "tool_call", "toolcall", "function_call", "mcp":
		eventType = domain.EventAgentToolStarted
		toolName = toolNameFromText(line.Text)
	case "tool_result", "tool_completed":
		eventType = domain.EventAgentToolCompleted
		toolName = toolNameFromText(line.Text)
	case "tool_error", "tool_failed":
		eventType = domain.EventAgentToolFailed
		toolName = toolNameFromText(line.Text)
	case "text", "":
		eventType = domain.EventAgentMessageDelta
	default:
		if looksLikeTool(line.Type) {
			eventType = domain.EventAgentToolStarted
			toolName = toolNameFromText(line.Text)
		}
	}
	payload, err := json.Marshal(map[string]string{
		"stream": line.Stream,
		"text":   harness.Redact(line.Text),
		"type":   line.Type,
	})
	if err != nil {
		payload = []byte(`{}`)
	}
	return Event{
		Type:       eventType,
		AgentID:    turn.AgentID,
		TurnID:     turn.TurnID,
		EngineID:   engineID,
		ComputerID: turn.ComputerID,
		ToolName:   harness.Redact(toolName),
		Data:       payload,
	}
}

func looksLikeTool(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "tool") || strings.Contains(lower, "mcp") || strings.Contains(lower, "function")
}

func toolNameFromText(text string) string {
	var payload map[string]any
	if json.Unmarshal([]byte(text), &payload) != nil {
		return ""
	}
	for _, key := range []string{"name", "tool", "tool_name", "toolName"} {
		if item, ok := payload[key].(string); ok && strings.TrimSpace(item) != "" {
			return item
		}
	}
	if nested, ok := payload["tool_call"].(map[string]any); ok {
		if item, ok := nested["name"].(string); ok {
			return item
		}
	}
	return ""
}
