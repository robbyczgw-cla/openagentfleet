package domain

type TaskSummary struct {
	ID             string `json:"id"`
	BotID          string `json:"bot_id"`
	BotName        string `json:"bot_name"`
	ConversationID string `json:"conversation_id"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	Error          string `json:"error"`
	Provider       string `json:"provider"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	ResultPreview  string `json:"result_preview"`
	ArtifactCount  int    `json:"artifact_count"`
	ParentTaskID   string `json:"parent_task_id,omitempty"`
	RootTaskID     string `json:"root_task_id"`
	Attempt        int    `json:"attempt"`
	FollowupKind   string `json:"followup_kind,omitempty"`
	CanRetry       bool   `json:"can_retry"`
	CanRevise      bool   `json:"can_revise"`
}

type TaskInput struct {
	Brief       string       `json:"brief"`
	AgentID     string       `json:"agent_id"`
	Attachments []Attachment `json:"attachments"`
}

type Artifact struct {
	ID          string `json:"id"`
	RunID       string `json:"run_id"`
	Name        string `json:"name"`
	MediaType   string `json:"media_type"`
	Size        int64  `json:"size"`
	CreatedAt   string `json:"created_at"`
	PreviewKind string `json:"preview_kind"`
}
