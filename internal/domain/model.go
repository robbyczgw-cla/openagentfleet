package domain

type Bot struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type Conversation struct {
	ID        string `json:"id"`
	BotID     string `json:"bot_id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type SearchHit struct {
	Kind           string `json:"kind"`
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id,omitempty"`
	Title          string `json:"title,omitempty"`
	Snippet        string `json:"snippet"`
	CreatedAt      string `json:"created_at"`
}

type Message struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	Role           string `json:"role"`
	Content        string `json:"content"`
	CreatedAt      string `json:"created_at"`
}

// Attachment is stored inside the managed workspace so both host workers and
// the mounted Agent Computer can access the same user-selected file. The
// physical path never leaves the local API.
type Attachment struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id,omitempty"`
	Name           string `json:"name"`
	MediaType      string `json:"media_type"`
	Size           int64  `json:"size"`
	StoragePath    string `json:"-"`
	CreatedAt      string `json:"created_at"`
}

type Run struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	BotID          string `json:"bot_id"`
	Provider       string `json:"provider"`
	SessionID      string `json:"session_id,omitempty"`
	Status         string `json:"status"`
	Prompt         string `json:"prompt"`
	Error          string `json:"error,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type HarnessSession struct {
	ID              string `json:"id"`
	ConversationID  string `json:"conversation_id"`
	Provider        string `json:"provider"`
	NativeSessionID string `json:"native_session_id"`
	Workdir         string `json:"workdir"`
	Title           string `json:"title"`
	Status          string `json:"status"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

type RunEvent struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	Type      string `json:"type"`
	Data      string `json:"data"`
	CreatedAt string `json:"created_at"`
}

// StreamEvent is the client-facing envelope used by the live event channel.
// Data remains provider-specific JSON so the daemon can normalize lifecycle
// events without losing useful harness details.
type StreamEvent struct {
	ID             string `json:"id"`
	RunID          string `json:"run_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	Type           string `json:"type"`
	Data           string `json:"data"`
	CreatedAt      string `json:"created_at"`
}

type ApprovalRequest struct {
	ID               string `json:"id"`
	RunID            string `json:"run_id"`
	Provider         string `json:"provider"`
	Action           string `json:"action"`
	Payload          string `json:"payload"`
	Status           string `json:"status"`
	SelectedOptionID string `json:"selected_option_id,omitempty"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
	ResolvedAt       string `json:"resolved_at,omitempty"`
}

// ApprovalOption is the safe, typed subset of a provider permission option
// that can be rendered after a reload. Provider payloads and tool-call JSON
// are intentionally not carried into the transcript read model.
type ApprovalOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// TranscriptBlock is an additive read model for durable approval history. It
// is derived from approval_requests and deliberately excludes raw payloads so
// a resolved prompt cannot leak provider/session data into the transcript.
type TranscriptBlock struct {
	ID               string           `json:"id"`
	Kind             string           `json:"kind"`
	ConversationID   string           `json:"conversation_id"`
	RunID            string           `json:"run_id"`
	ApprovalID       string           `json:"approval_id"`
	Provider         string           `json:"provider"`
	Action           string           `json:"action"`
	Status           string           `json:"status"`
	Options          []ApprovalOption `json:"options,omitempty"`
	SelectedOptionID string           `json:"selected_option_id,omitempty"`
	CreatedAt        string           `json:"created_at"`
	UpdatedAt        string           `json:"updated_at"`
	ResolvedAt       string           `json:"resolved_at,omitempty"`
}

type Capability struct {
	Name      string `json:"name"`
	Command   string `json:"command"`
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type Skill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Path        string `json:"-"`
	Source      string `json:"source"`
	Eligible    bool   `json:"eligible"`
	Detail      string `json:"detail,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}
