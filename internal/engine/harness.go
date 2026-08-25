package engine

import (
	"context"
	"errors"
	"log/slog"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
)

// TurnRunner is the existing harness execution surface. The adapter does not
// talk to CLIs itself.
type TurnRunner interface {
	RunWithOptions(ctx context.Context, provider, prompt, workdir string, options harness.RunOptions) (string, error)
}

// AuthProbe reports CLI availability without exposing credentials.
type AuthProbe func(ctx context.Context) (AuthState, error)

// HarnessAdapter wraps the current provider runners behind Adapter.
type HarnessAdapter struct {
	id     ID
	runner TurnRunner
	auth   AuthProbe
	caps   Capabilities
}

func NewHarnessAdapter(id ID, runner TurnRunner) *HarnessAdapter {
	return &HarnessAdapter{id: id, runner: runner, caps: CapabilitiesFor(id)}
}

func (a *HarnessAdapter) WithAuthProbe(probe AuthProbe) *HarnessAdapter {
	if a != nil {
		a.auth = probe
	}
	return a
}

func (a *HarnessAdapter) ID() ID {
	if a == nil {
		return ""
	}
	return a.id
}

func (a *HarnessAdapter) GetCapabilities() Capabilities {
	if a == nil {
		return Capabilities{}
	}
	return a.caps
}

func (a *HarnessAdapter) GetAuthState(ctx context.Context) (AuthState, error) {
	if a == nil {
		return AuthState{}, errors.New("engine adapter is nil")
	}
	if err := ctx.Err(); err != nil {
		return AuthState{EngineID: a.id}, err
	}
	if a.auth != nil {
		state, err := a.auth(ctx)
		state.EngineID = a.id
		state.Detail = harness.Redact(state.Detail)
		return state, err
	}
	// A runner is not proof of login. Authentication stays false until a
	// probe reports it; adapters never invent a connected state.
	return AuthState{
		EngineID:      a.id,
		Available:     a.runner != nil,
		Authenticated: false,
		LoginRequired: a.runner != nil,
		Detail:        "sign-in status is owned by the provider CLI",
	}, nil
}

func (a *HarnessAdapter) RunTurn(ctx context.Context, turn TurnContext, emit func(Event)) (string, error) {
	if a == nil {
		return "", errors.New("engine adapter is nil")
	}
	if a.runner == nil {
		err := errors.New("engine runner unavailable")
		a.failTurn(turn, emit, err)
		return "", err
	}
	slog.Info("engine turn started", turn.logAttrs(a.id)...)
	if emit != nil {
		emit(startedEvent(turn, a.id))
	}
	if len(turn.MCPServers) > 0 && !a.caps.MCP {
		err := errors.New("MCP server injection is unsupported for this engine")
		a.failTurn(turn, emit, err)
		return "", err
	}
	options := harness.RunOptions{
		SessionID:       turn.SessionID,
		SystemPrompt:    turn.SystemPrompt,
		Model:           turn.Model,
		ReasoningEffort: turn.Reasoning,
		ServiceTier:     turn.ServiceTier,
		PermissionMode:  turn.Permission,
		WebSearch:       turn.WebSearch,
		Role:            turn.Role,
		MCPServers:      turn.MCPServers,
		OnSession:       turn.OnSession,
		OnPermission:    turn.OnPermission,
		OnLine: func(line harness.OutputLine) {
			if emit == nil {
				return
			}
			event := NormalizeOutputLine(a.id, turn, line)
			if event.ToolName != "" {
				slog.Info("engine tool event", turn.logAttrs(a.id, "toolName", harness.Redact(event.ToolName), "type", event.Type)...)
			}
			emit(event)
		},
	}
	output, err := a.runner.RunWithOptions(ctx, HarnessProvider(a.id), turn.Prompt, turn.Workdir, options)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			slog.Info("engine turn cancelled", turn.logAttrs(a.id)...)
			emitTurn(emit, turn, a.id, domain.EventAgentTurnCancelled, map[string]string{"status": "cancelled"})
			return output, err
		}
		a.failTurn(turn, emit, err)
		return output, err
	}
	if output != "" {
		emitTurn(emit, turn, a.id, domain.EventAgentMessageCompleted, map[string]string{"status": "completed"})
	}
	emitTurn(emit, turn, a.id, domain.EventAgentTurnCompleted, map[string]string{"status": "completed"})
	slog.Info("engine turn completed", turn.logAttrs(a.id)...)
	return output, nil
}

func (a *HarnessAdapter) failTurn(turn TurnContext, emit func(Event), err error) {
	detail := harness.Redact(err.Error())
	slog.Info("engine turn failed", turn.logAttrs(a.id, "error", detail)...)
	emitTurn(emit, turn, a.id, domain.EventAgentTurnFailed, map[string]string{"error": detail, "status": "failed"})
}

// DefaultRegistry wraps one runner for every shipped provider id.
func DefaultRegistry(runner TurnRunner) *Registry {
	registry := NewRegistry()
	for _, id := range []ID{Grok, GrokBuild, Claude, Codex, CodexAppServer, OpenCode, Pi, Cursor} {
		_ = registry.Register(NewHarnessAdapter(id, runner))
	}
	return registry
}
