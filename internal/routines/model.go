package routines

import (
	"strings"
	"time"
)

// ExecutionMode describes whether a routine is intended to run unattended or
// with a person present. It does not itself execute anything.
type ExecutionMode string

const (
	ModeBackground ExecutionMode = "background"
	ModeAttended   ExecutionMode = "attended"
)

// RoutineState is the manager-level lifecycle state. needs_attention is a
// deliberate stop state: a scheduler may inspect it, but must not start it.
type RoutineState string

const (
	StateEnabled        RoutineState = "enabled"
	StatePaused         RoutineState = "paused"
	StateNeedsAttention RoutineState = "needs_attention"
)

// SourceReference keeps a routine traceable to the conversation that created
// it and/or the reusable skill it came from. At least one ID is required.
type SourceReference struct {
	ConversationID string `json:"conversation_id,omitempty"`
	SkillID        string `json:"skill_id,omitempty"`
	SkillVersion   string `json:"skill_version,omitempty"`
}

func (s SourceReference) Valid() bool {
	return strings.TrimSpace(s.ConversationID) != "" || strings.TrimSpace(s.SkillID) != ""
}

// CreateInput is the persistence/API-neutral input for creating a routine.
type CreateInput struct {
	Name             string
	Schedule         Schedule
	Mode             ExecutionMode
	Source           SourceReference
	ApprovalRequired bool
	BrowserUse       bool
	DesktopUse       bool
}

// Routine is a safe schedule record. The manager derives ApprovalRequired as
// true for attended, browser, or desktop work even when a caller forgets to
// request approval explicitly.
type Routine struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Schedule         Schedule        `json:"schedule"`
	Mode             ExecutionMode   `json:"mode"`
	Source           SourceReference `json:"source"`
	ApprovalRequired bool            `json:"approval_required"`
	BrowserUse       bool            `json:"browser_use"`
	DesktopUse       bool            `json:"desktop_use"`
	State            RoutineState    `json:"state"`
	AttentionReason  string          `json:"attention_reason,omitempty"`
	NextRunAt        time.Time       `json:"next_run_at"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

func (r Routine) RequiresHumanApproval() bool {
	return r.ApprovalRequired || r.Mode == ModeAttended || r.BrowserUse || r.DesktopUse
}

type DueRoutine struct {
	Routine      Routine   `json:"routine"`
	ScheduledFor time.Time `json:"scheduled_for"`
}

type RunState string

const (
	RunAwaitingApproval RunState = "awaiting_approval"
	RunReady            RunState = "ready"
	RunRunning          RunState = "running"
	RunSucceeded        RunState = "succeeded"
	RunFailed           RunState = "failed"
	RunSkipped          RunState = "skipped"
)

// Run is a lifecycle lease/record for a single occurrence. It is intentionally
// only data: StartRun records authorization to start work, but does not invoke
// a command, model, browser, network, or desktop executor.
type Run struct {
	ID            string    `json:"id"`
	RoutineID     string    `json:"routine_id"`
	State         RunState  `json:"state"`
	ScheduledFor  time.Time `json:"scheduled_for"`
	RequestedAt   time.Time `json:"requested_at"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	FinishedAt    time.Time `json:"finished_at,omitempty"`
	ApprovalID    string    `json:"approval_id,omitempty"`
	OutcomeReason string    `json:"outcome_reason,omitempty"`
}

type RunOutcome string

const (
	OutcomeSucceeded RunOutcome = "succeeded"
	OutcomeFailed    RunOutcome = "failed"
	OutcomeSkipped   RunOutcome = "skipped"
)

type EventType string

const (
	EventCreated             EventType = "created"
	EventPaused              EventType = "paused"
	EventResumed             EventType = "resumed"
	EventNeedsAttention      EventType = "needs_attention"
	EventAttentionResolved   EventType = "attention_resolved"
	EventDue                 EventType = "run_due"
	EventRunReady            EventType = "run_ready"
	EventApprovalRequested   EventType = "approval_requested"
	EventApprovalGranted     EventType = "approval_granted"
	EventRunStarted          EventType = "run_started"
	EventRunSucceeded        EventType = "run_succeeded"
	EventRunFailed           EventType = "run_failed"
	EventRunSkipped          EventType = "run_skipped"
	EventPendingRunCancelled EventType = "pending_run_cancelled"
)

// Event is append-only manager history suitable for persistence later. The
// manager returns copies, including copied metadata, to prevent callers from
// mutating history behind its back.
type Event struct {
	ID         string            `json:"id"`
	RoutineID  string            `json:"routine_id"`
	RunID      string            `json:"run_id,omitempty"`
	Type       EventType         `json:"type"`
	At         time.Time         `json:"at"`
	Message    string            `json:"message,omitempty"`
	ApprovalID string            `json:"approval_id,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}
