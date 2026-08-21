package domain

import "encoding/json"

const (
	RemotePlatformIOS     = "ios"
	RemotePlatformAndroid = "android"
	RemotePlatformDesktop = "desktop"

	RemoteScopeObserver   = "observer"
	RemoteScopeController = "controller"
	RemoteScopeOwner      = "owner"

	RemoteDeviceActive  = "active"
	RemoteDeviceRevoked = "revoked"

	RemotePairingPending = "pending"
	RemotePairingClaimed = "claimed"
	RemotePairingLocked  = "locked"
	RemotePairingExpired = "expired"

	// RemoteAuthVersionBearer is hashed-bearer alpha auth. Bump auth_version
	// before refresh-token rotation; DPoP is not implemented on this version.
	RemoteAuthVersionBearer = 1
)

// ValidRemotePlatform reports whether platform is a paired-client identity
// (phones or a desktop client). The fleet host itself is not a remote device.
func ValidRemotePlatform(platform string) bool {
	switch platform {
	case RemotePlatformIOS, RemotePlatformAndroid, RemotePlatformDesktop:
		return true
	default:
		return false
	}
}

// RemoteDevice is the public, durable identity of a paired mobile client.
// Credentials are stored separately and are never included in device views.
type RemoteDevice struct {
	ID           string `json:"id"`
	DisplayName  string `json:"display_name"`
	Platform     string `json:"platform"`
	ScopeProfile string `json:"scope_profile"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
	RevokedAt    string `json:"revoked_at,omitempty"`
	LastUsedAt   string `json:"last_used_at,omitempty"`
}

// RemoteCredential is the persisted representation of a bearer credential.
// TokenHash is deliberately excluded from JSON; raw bearer values must never
// be placed in this type or written to durable storage.
type RemoteCredential struct {
	TokenHash   [32]byte `json:"-"`
	DeviceID    string   `json:"device_id"`
	AuthVersion int      `json:"auth_version"`
	ExpiresAt   string   `json:"expires_at"`
	Revoked     bool     `json:"revoked"`
}

// RemotePairingGrant is the public view of a short-lived, one-time pairing
// grant. Pairing secrets and their hashes deliberately have no representation
// in this type.
type RemotePairingGrant struct {
	ID           string `json:"id"`
	ScopeProfile string `json:"scope_profile"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
	ExpiresAt    string `json:"expires_at"`
}

// RemoteSession is the authenticated alpha credential view. It contains no
// bearer value or credential hash and can safely be used by the HTTP layer.
type RemoteSession struct {
	Device      RemoteDevice `json:"device"`
	AuthVersion int          `json:"auth_version"`
	ExpiresAt   string       `json:"expires_at"`
}

// MobileRun deliberately excludes prompts, provider errors, bot/session IDs,
// model options, and native harness metadata.
type MobileRun struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	Status         string `json:"status"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// MobileApproval is the phone-visible gate. It never carries provider, payload,
// or tool_call JSON; options are optionId/name/kind only.
type MobileApproval struct {
	ID             string           `json:"id"`
	RunID          string           `json:"run_id"`
	ConversationID string           `json:"conversation_id"`
	BotID          string           `json:"bot_id"`
	Action         string           `json:"action"`
	Status         string           `json:"status"`
	CreatedAt      string           `json:"created_at"`
	Options        []ApprovalOption `json:"options,omitempty"`
}

// MobileRoutine is the phone-visible schedule. Skill internals, workers,
// prompts, and approval policy stay off this DTO.
type MobileRoutine struct {
	ID              string        `json:"id"`
	BotID           string        `json:"bot_id"`
	Name            string        `json:"name"`
	Status          RoutineStatus `json:"status"`
	Kind            RoutineKind   `json:"kind"`
	NextRunAt       string        `json:"next_run_at,omitempty"`
	LastRunAt       string        `json:"last_run_at,omitempty"`
	AttentionReason string        `json:"attention_reason,omitempty"`
}

type MobileMessageResponse struct {
	Message Message   `json:"message"`
	Run     MobileRun `json:"run"`
}

// MobileSnapshot is read under one SQLite transaction so EventCursor is a
// trustworthy high-water mark for the returned conversation state.
type MobileSnapshot struct {
	Conversations []Conversation `json:"conversations"`
	Conversation  Conversation   `json:"conversation"`
	Messages      []Message      `json:"messages"`
	Runs          []MobileRun    `json:"runs"`
	EventCursor   uint64         `json:"event_cursor"`
}

// MobileEventRecord is intentionally data-free. The HTTP layer constructs a
// small event payload from Type rather than forwarding provider-controlled or
// session-bearing run-event JSON.
type MobileEventRecord struct {
	Cursor         uint64 `json:"cursor"`
	Type           string `json:"type"`
	RunID          string `json:"run_id"`
	ConversationID string `json:"conversation_id"`
	CreatedAt      string `json:"created_at"`
}

type MobileEventEnvelope struct {
	Cursor         uint64          `json:"cursor"`
	Type           string          `json:"type"`
	RunID          string          `json:"run_id"`
	ConversationID string          `json:"conversation_id"`
	Data           json.RawMessage `json:"data"`
}
