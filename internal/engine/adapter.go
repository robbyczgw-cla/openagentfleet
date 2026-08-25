// Package engine is the interchangeable provider boundary. An Agent binds an
// engine; switching engines must not change Agent identity or computer state.
package engine

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
)

// ID is a stable engine identifier. It is not an Agent id and not a model id.
type ID string

const (
	GrokBuild      ID = "grok_build"
	Grok           ID = "grok"
	Claude         ID = "claude"
	Codex          ID = "codex"
	CodexAppServer ID = "codex_app_server"
	OpenCode       ID = "opencode"
	Pi             ID = "pi"
	Cursor         ID = "cursor"
)

// Capabilities are declared by an adapter, never inferred by callers.
type Capabilities struct {
	Tools            bool `json:"tools"`
	MCP              bool `json:"mcp"`
	Reasoning        bool `json:"reasoning"`
	ImageInput       bool `json:"image_input"`
	SessionResume    bool `json:"session_resume"`
	Streaming        bool `json:"streaming"`
	ComputerMCP      bool `json:"computer_mcp"`
	RemoteExecution  bool `json:"remote_execution,omitempty"`
	MaxContextTokens int  `json:"max_context_tokens,omitempty"`
}

// AuthState is metadata-only. Adapters must not return tokens or session secrets.
type AuthState struct {
	EngineID      ID     `json:"engine_id"`
	Available     bool   `json:"available"`
	Authenticated bool   `json:"authenticated"`
	LoginRequired bool   `json:"login_required"`
	Detail        string `json:"detail,omitempty"`
}

// TurnContext is the adapter-neutral snapshot for one serialized Agent turn.
type TurnContext struct {
	AgentID        string
	TurnID         string
	ConversationID string
	RunID          string
	ComputerID     string
	Prompt         string
	SystemPrompt   string
	Model          string
	Reasoning      string
	ServiceTier    string
	Permission     string
	WebSearch      string
	Workdir        string
	SessionID      string
	Role           string
	MCPServers     []harness.MCPServerSpec
	OnSession      func(nativeSessionID string)
	OnPermission   func(context.Context, harness.PermissionRequest) (harness.PermissionDecision, error)
}

// Event is a normalized fleet event produced by an adapter before UI/persistence.
type Event struct {
	Type       string          `json:"type"`
	AgentID    string          `json:"agent_id,omitempty"`
	TurnID     string          `json:"turn_id,omitempty"`
	EngineID   ID              `json:"engine_id,omitempty"`
	ComputerID string          `json:"computer_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
}

// Adapter is the single engine contract. Provider-specific protocol lives inside
// the adapter, not in the Agent domain or the HTTP API.
type Adapter interface {
	ID() ID
	GetAuthState(ctx context.Context) (AuthState, error)
	GetCapabilities() Capabilities
	RunTurn(ctx context.Context, turn TurnContext, emit func(Event)) (string, error)
}

func (turn TurnContext) logAttrs(engineID ID, extra ...any) []any {
	attrs := []any{
		"agentId", turn.AgentID,
		"turnId", turn.TurnID,
		"engineId", string(engineID),
		"computerId", turn.ComputerID,
	}
	return append(attrs, extra...)
}

func emitTurn(emit func(Event), turn TurnContext, engineID ID, eventType string, data any) {
	if emit == nil {
		return
	}
	payload, err := json.Marshal(data)
	if err != nil {
		slog.Warn("engine event marshal failed", turn.logAttrs(engineID, "error", err.Error())...)
		payload = []byte(`{}`)
	}
	emit(Event{
		Type:       eventType,
		AgentID:    turn.AgentID,
		TurnID:     turn.TurnID,
		EngineID:   engineID,
		ComputerID: turn.ComputerID,
		Data:       payload,
	})
}

func startedEvent(turn TurnContext, engineID ID) Event {
	return Event{
		Type:       domain.EventAgentTurnStarted,
		AgentID:    turn.AgentID,
		TurnID:     turn.TurnID,
		EngineID:   engineID,
		ComputerID: turn.ComputerID,
		Data:       json.RawMessage(`{"status":"running"}`),
	}
}
