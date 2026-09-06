package domain

const (
	EventRunQueued             = "run.queued"
	EventRunStarted            = "run.started"
	EventRunCompleted          = "run.completed"
	EventRunFailed             = "run.failed"
	EventRunStopped            = "run.stopped"
	EventRunBlocked            = "run.blocked"
	EventRunWaitingForApproval = "run.waiting_for_approval"
	EventProviderOutput        = "provider.output"
	EventSessionOpened         = "session.opened"
	EventApprovalRequested     = "approval.requested"
	EventApprovalResolved      = "approval.resolved"

	EventAgentTurnStarted      = "agent.turn.started"
	EventAgentThinking         = "agent.thinking"
	EventAgentMessageDelta     = "agent.message.delta"
	EventAgentMessageCompleted = "agent.message.completed"
	EventAgentToolStarted      = "agent.tool.started"
	EventAgentToolCompleted    = "agent.tool.completed"
	EventAgentToolFailed       = "agent.tool.failed"
	EventAgentTurnCompleted    = "agent.turn.completed"
	EventAgentTurnFailed       = "agent.turn.failed"
	EventAgentTurnCancelled    = "agent.turn.cancelled"

	// Delegation and computer events are reserved names. They have no
	// runtime behavior in this layer.
	EventAgentDelegationCreated   = "agent.delegation.created"
	EventAgentDelegationStarted   = "agent.delegation.started"
	EventAgentDelegationCompleted = "agent.delegation.completed"
	EventAgentDelegationFailed    = "agent.delegation.failed"
	EventComputerStarted          = "computer.started"
	EventComputerStopped          = "computer.stopped"
	EventComputerHealthChanged    = "computer.health_changed"
)

// CanonicalEventType maps historical run.* product events onto agent.turn.*
// names used by engines, tools, and the coordinator. Unknown types pass through.
func CanonicalEventType(existing string) string {
	switch existing {
	case EventRunStarted:
		return EventAgentTurnStarted
	case EventRunCompleted:
		return EventAgentTurnCompleted
	case EventRunFailed:
		return EventAgentTurnFailed
	case EventRunStopped:
		return EventAgentTurnCancelled
	default:
		return existing
	}
}
