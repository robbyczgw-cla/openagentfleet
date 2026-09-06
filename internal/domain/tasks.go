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
