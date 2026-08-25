package coordinator

import (
	"errors"
	"log/slog"

	"github.com/robbyczgw-cla/openagentfleet/internal/orchestration"
)

var (
	ErrDelegationSourceRequired = errors.New("delegation source agent is required")
	ErrDelegationTargetRequired = errors.New("delegation target agent is required")
	ErrDelegationSameAgent      = errors.New("delegation target must be a different agent")
	ErrDelegationDepth          = errors.New("delegation depth is invalid")
)

// Delegation is a coordinator-owned hop: source Agent queues work onto the
// target Agent's turn queue. It is not a hidden worker and not a new Agent.
// Policy (collaboration enable, allowlist, depth, active-peer cap) stays in
// orchestration.ValidateAgentTask. The durable row remains a domain.Handoff.
type Delegation struct {
	SourceAgentID   string
	TargetAgentID   string
	SourceTurnID    string
	OriginTurnID    string
	ParentHandoffID string
	Depth           int
	TimeoutSeconds  int
}

// PlanDelegation accepts an already-validated AgentTask. The coordinator does
// not re-check collaboration policy; it only refuses a hop that could not be
// scheduled onto a different Agent.
func PlanDelegation(task orchestration.AgentTask) (Delegation, error) {
	if task.SourceBotID == "" {
		return Delegation{}, ErrDelegationSourceRequired
	}
	if task.TargetBotID == "" {
		return Delegation{}, ErrDelegationTargetRequired
	}
	if task.SourceBotID == task.TargetBotID {
		return Delegation{}, ErrDelegationSameAgent
	}
	if task.Depth < 1 {
		return Delegation{}, ErrDelegationDepth
	}
	origin := task.OriginRunID
	if origin == "" {
		origin = task.SourceRunID
	}
	return Delegation{
		SourceAgentID:   task.SourceBotID,
		TargetAgentID:   task.TargetBotID,
		SourceTurnID:    task.SourceRunID,
		OriginTurnID:    origin,
		ParentHandoffID: task.ParentHandoffID,
		Depth:           task.Depth,
		TimeoutSeconds:  task.TimeoutSeconds,
	}, nil
}

func (c *Coordinator) PlanDelegation(task orchestration.AgentTask) (Delegation, error) {
	delegation, err := PlanDelegation(task)
	if err != nil {
		return Delegation{}, err
	}
	if c != nil {
		slog.Info("delegation planned",
			"agentId", delegation.SourceAgentID,
			"turnId", delegation.SourceTurnID,
			"targetAgentId", delegation.TargetAgentID,
			"depth", delegation.Depth,
		)
	}
	return delegation, nil
}
