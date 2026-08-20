package domain

const (
	HandoffStatusQueued    = "queued"
	HandoffStatusRunning   = "running"
	HandoffStatusWaiting   = "waiting"
	HandoffStatusCompleted = "completed"
	HandoffStatusFailed    = "failed"
	HandoffStatusCancelled = "cancelled"
	HandoffStatusTimedOut  = "timed_out"

	HandoffModeUser     = "user"
	HandoffModeMessage  = "message"
	HandoffModeDelegate = "delegate"
)

// Handoff is one visible Agent-to-Agent transfer. It is not worker
// delegation: the target Agent keeps its own lead, MCP grants, and computer.
type Handoff struct {
	ID                   string `json:"id"`
	SourceBotID          string `json:"source_bot_id"`
	SourceConversationID string `json:"source_conversation_id"`
	SourceMessageID      string `json:"source_message_id"`
	TargetBotID          string `json:"target_bot_id"`
	TargetConversationID string `json:"target_conversation_id"`
	TargetMessageID      string `json:"target_message_id"`
	TargetRunID          string `json:"target_run_id"`
	Content              string `json:"content"`
	CreatedAt            string `json:"created_at"`
	Status               string `json:"status"`
	Mode                 string `json:"mode"`
	ParentHandoffID      string `json:"parent_handoff_id,omitempty"`
	Depth                int    `json:"depth"`
	OriginRunID          string `json:"origin_run_id,omitempty"`
	SourceRunID          string `json:"source_run_id,omitempty"`
	Result               string `json:"result,omitempty"`
	CompletedAt          string `json:"completed_at,omitempty"`
	TimeoutSeconds       int    `json:"timeout_seconds,omitempty"`
}
