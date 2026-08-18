package domain

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
}
