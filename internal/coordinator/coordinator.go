// Package coordinator owns turn scheduling and normalized event routing.
// Future agent.delegation.* scheduling belongs here, not inside an engine
// adapter or a computer backend. This package does not implement delegation.
package coordinator

import (
	"log/slog"

	"github.com/robbyczgw-cla/openagentfleet/internal/computer"
	"github.com/robbyczgw-cla/openagentfleet/internal/engine"
	"github.com/robbyczgw-cla/openagentfleet/internal/tools"
)

// Coordinator is the fleet-side runtime boundary: Agent lifecycle, per-Agent
// turn serialization, engine lookup, and tool/computer routing. It does not
// talk to provider CLIs or Docker itself.
//
// Delegation is planned here and executed as a turn on the target Agent.
// Policy stays in orchestration.ValidateAgentTask; the durable job is a handoff.
type Coordinator struct {
	Turns     *TurnQueue
	Engines   *engine.Registry
	Tools     *tools.Registry
	Computers *computer.Registry
}

func New(engines *engine.Registry, toolset *tools.Registry, computers *computer.Registry) *Coordinator {
	return &Coordinator{
		Turns:     NewTurnQueue(),
		Engines:   engines,
		Tools:     toolset,
		Computers: computers,
	}
}

func (c *Coordinator) Engine(id engine.ID) (engine.Adapter, bool) {
	if c == nil || c.Engines == nil {
		return nil, false
	}
	return c.Engines.Get(id)
}

func (c *Coordinator) Computer(id string) (computer.Backend, bool) {
	if c == nil || c.Computers == nil {
		return nil, false
	}
	return c.Computers.Get(id)
}

func (c *Coordinator) LogAttrs(agentID, turnID, engineID, computerID, toolName string) []any {
	attrs := []any{"agentId", agentID, "turnId", turnID, "engineId", engineID, "computerId", computerID}
	if toolName != "" {
		attrs = append(attrs, "toolName", toolName)
	}
	return attrs
}

func (c *Coordinator) Info(msg, agentID, turnID, engineID, computerID, toolName string) {
	slog.Info(msg, c.LogAttrs(agentID, turnID, engineID, computerID, toolName)...)
}
