package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	RoutineNameMaxRunes           = 160
	RoutineDescriptionMaxBytes    = 4096
	RoutineCronExpressionMaxBytes = 256
	RoutineHeartbeatMinSeconds    = 30
	RoutineHeartbeatMaxSeconds    = 24 * 60 * 60
	RoutineRetryMaxAttempts       = 10
	RoutineRetryMaxBackoffSeconds = 24 * 60 * 60
)

type RoutineKind string

const (
	RoutineKindCron      RoutineKind = "cron"
	RoutineKindHeartbeat RoutineKind = "heartbeat"
)

type RoutineStatus string

const (
	RoutineStatusDisabled       RoutineStatus = "disabled"
	RoutineStatusEnabled        RoutineStatus = "enabled"
	RoutineStatusPaused         RoutineStatus = "paused"
	RoutineStatusNeedsAttention RoutineStatus = "needs_attention"
)

type RoutineApprovalPolicy string

const (
	RoutineApprovalNever  RoutineApprovalPolicy = "never"
	RoutineApprovalOnRisk RoutineApprovalPolicy = "on_risk"
	RoutineApprovalAlways RoutineApprovalPolicy = "always"
)

type RoutineLeadHarness string

const (
	RoutineLeadGrokBuild      RoutineLeadHarness = "grok_build"
	RoutineLeadCodexAppServer RoutineLeadHarness = "codex_app_server"
)

type RoutineWorker string

const (
	RoutineWorkerClaude   RoutineWorker = "claude"
	RoutineWorkerPi       RoutineWorker = "pi"
	RoutineWorkerCodex    RoutineWorker = "codex"
	RoutineWorkerGrok     RoutineWorker = "grok"
	RoutineWorkerOpenCode RoutineWorker = "opencode"
	RoutineWorkerCursor   RoutineWorker = "cursor"
)

// RoutineLedgerState intentionally has no queued or skipped state. A claimed
// occurrence is already owned by exactly one scheduler. Unknown is reserved
// for a lease that expired while its external side effects were indeterminate.
type RoutineLedgerState string

const (
	RoutineLedgerClaimed   RoutineLedgerState = "claimed"
	RoutineLedgerRunning   RoutineLedgerState = "running"
	RoutineLedgerCompleted RoutineLedgerState = "completed"
	RoutineLedgerFailed    RoutineLedgerState = "failed"
	RoutineLedgerUnknown   RoutineLedgerState = "unknown"
)

const (
	RoutineTriggerSchedule = "schedule"
	RoutineTriggerTest     = "test"
	RoutineTriggerWebhook  = "webhook"
)

// RoutineSkipsSchedule is true when a run must not consume next_run_at.
func RoutineSkipsSchedule(trigger string) bool {
	return trigger == RoutineTriggerTest || trigger == RoutineTriggerWebhook
}

// RoutineWebhook is the public view of a delivery credential. The secret is
// never stored here; Create/Rotate return it once on a different type.
type RoutineWebhook struct {
	RoutineID  string `json:"routine_id"`
	Configured bool   `json:"configured"`
	CreatedAt  string `json:"created_at,omitempty"`
	LastUsedAt string `json:"last_used_at,omitempty"`
}

type RoutineRetryPolicy struct {
	MaxAttempts    int `json:"max_attempts"`
	BackoffSeconds int `json:"backoff_seconds"`
}

// RoutineDraft is deliberately missing an enabled field. Every persisted
// routine starts disabled and must be resumed explicitly. Heartbeats require a
// separate opt-in bit in addition to that lifecycle transition.
type RoutineDraft struct {
	BotID                    string                `json:"bot_id"`
	Name                     string                `json:"name"`
	Description              string                `json:"description,omitempty"`
	Kind                     RoutineKind           `json:"kind"`
	CronExpression           string                `json:"cron_expression,omitempty"`
	TimeZone                 string                `json:"time_zone"`
	HeartbeatOptIn           bool                  `json:"heartbeat_opt_in"`
	HeartbeatIntervalSeconds int                   `json:"heartbeat_interval_seconds,omitempty"`
	LeadHarness              RoutineLeadHarness    `json:"lead_harness"`
	Worker                   RoutineWorker         `json:"worker"`
	ApprovalPolicy           RoutineApprovalPolicy `json:"approval_policy"`
	Retry                    RoutineRetryPolicy    `json:"retry"`
	NextRunAt                string                `json:"next_run_at,omitempty"`
}

type Routine struct {
	ID                       string                `json:"id"`
	BotID                    string                `json:"bot_id"`
	Name                     string                `json:"name"`
	Description              string                `json:"description,omitempty"`
	Kind                     RoutineKind           `json:"kind"`
	Status                   RoutineStatus         `json:"status"`
	CronExpression           string                `json:"cron_expression,omitempty"`
	TimeZone                 string                `json:"time_zone"`
	HeartbeatOptIn           bool                  `json:"heartbeat_opt_in"`
	HeartbeatIntervalSeconds int                   `json:"heartbeat_interval_seconds,omitempty"`
	LeadHarness              RoutineLeadHarness    `json:"lead_harness"`
	Worker                   RoutineWorker         `json:"worker"`
	ApprovalPolicy           RoutineApprovalPolicy `json:"approval_policy"`
	Retry                    RoutineRetryPolicy    `json:"retry"`
	NextRunAt                string                `json:"next_run_at,omitempty"`
	OccurrenceKey            string                `json:"occurrence_key,omitempty"`
	RetryNotBefore           string                `json:"retry_not_before,omitempty"`
	LastRunAt                string                `json:"last_run_at,omitempty"`
	AttentionReason          string                `json:"attention_reason,omitempty"`
	CreatedAt                string                `json:"created_at"`
	UpdatedAt                string                `json:"updated_at"`
}

type RoutineListFilter struct {
	BotID  string
	Kind   RoutineKind
	Status RoutineStatus
}

type RoutineRun struct {
	ID             string             `json:"id"`
	RoutineID      string             `json:"routine_id"`
	State          RoutineLedgerState `json:"state"`
	ScheduledFor   string             `json:"scheduled_for"`
	OccurrenceKey  string             `json:"occurrence_key"`
	Attempt        int                `json:"attempt"`
	LeaseOwner     string             `json:"lease_owner,omitempty"`
	LeaseToken     string             `json:"-"`
	LeaseExpiresAt string             `json:"lease_expires_at,omitempty"`
	ApprovalID     string             `json:"approval_id,omitempty"`
	IdempotencyKey string             `json:"idempotency_key,omitempty"`
	Trigger        string             `json:"trigger,omitempty"`
	ClaimedAt      string             `json:"claimed_at"`
	StartedAt      string             `json:"started_at,omitempty"`
	FinishedAt     string             `json:"finished_at,omitempty"`
	OutcomeReason  string             `json:"outcome_reason,omitempty"`
	CreatedAt      string             `json:"created_at"`
	UpdatedAt      string             `json:"updated_at"`
}

type RoutineClaim struct {
	RoutineID      string
	LeaseOwner     string
	LeaseDuration  time.Duration
	IdempotencyKey string
	ApprovalID     string
	Trigger        string
	OccurrenceKey  string
	Now            time.Time
}

type RoutineFinish struct {
	RunID      string
	LeaseOwner string
	LeaseToken string
	State      RoutineLedgerState
	Reason     string
	NextRunAt  time.Time
	Now        time.Time
}

type RoutineEvent struct {
	ID        string `json:"id"`
	RoutineID string `json:"routine_id"`
	RunID     string `json:"run_id,omitempty"`
	Type      string `json:"type"`
	Message   string `json:"message,omitempty"`
	CreatedAt string `json:"created_at"`
}

func (draft RoutineDraft) Validate() error {
	if strings.TrimSpace(draft.BotID) == "" {
		return errors.New("routine bot id is required")
	}
	name := strings.TrimSpace(draft.Name)
	if name == "" || len([]rune(name)) > RoutineNameMaxRunes || hasControl(name) {
		return fmt.Errorf("routine name must contain 1 to %d printable characters", RoutineNameMaxRunes)
	}
	if len([]byte(draft.Description)) > RoutineDescriptionMaxBytes || hasControlExceptWhitespace(draft.Description) {
		return fmt.Errorf("routine description must be at most %d bytes and contain no control characters", RoutineDescriptionMaxBytes)
	}
	zone := strings.TrimSpace(draft.TimeZone)
	if zone == "" {
		return errors.New("routine time zone is required")
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return fmt.Errorf("invalid routine time zone %q: %w", zone, err)
	}
	if !validRoutineLead(draft.LeadHarness) {
		return fmt.Errorf("unsupported routine lead harness %q", draft.LeadHarness)
	}
	if !validRoutineWorker(draft.Worker) {
		return fmt.Errorf("unsupported routine worker %q", draft.Worker)
	}
	if draft.ApprovalPolicy != "" && !validRoutineApproval(draft.ApprovalPolicy) {
		return fmt.Errorf("unsupported routine approval policy %q", draft.ApprovalPolicy)
	}
	if draft.Retry.MaxAttempts < 1 || draft.Retry.MaxAttempts > RoutineRetryMaxAttempts {
		return fmt.Errorf("routine retry attempts must be between 1 and %d", RoutineRetryMaxAttempts)
	}
	if draft.Retry.BackoffSeconds < 0 || draft.Retry.BackoffSeconds > RoutineRetryMaxBackoffSeconds {
		return fmt.Errorf("routine retry backoff must be between 0 and %d seconds", RoutineRetryMaxBackoffSeconds)
	}
	if draft.NextRunAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, draft.NextRunAt); err != nil {
			return fmt.Errorf("invalid routine next run timestamp: %w", err)
		}
	}
	switch draft.Kind {
	case RoutineKindCron:
		if !validStoredCronExpression(draft.CronExpression) {
			return errors.New("routine cron expression must contain five valid numeric fields")
		}
		if draft.HeartbeatOptIn || draft.HeartbeatIntervalSeconds != 0 {
			return errors.New("cron routines cannot enable heartbeat")
		}
	case RoutineKindHeartbeat:
		if strings.TrimSpace(draft.CronExpression) != "" {
			return errors.New("heartbeat routines cannot contain a cron expression")
		}
		if draft.HeartbeatIntervalSeconds < RoutineHeartbeatMinSeconds || draft.HeartbeatIntervalSeconds > RoutineHeartbeatMaxSeconds {
			return fmt.Errorf("heartbeat interval must be between %d and %d seconds", RoutineHeartbeatMinSeconds, RoutineHeartbeatMaxSeconds)
		}
	default:
		return fmt.Errorf("unsupported routine kind %q", draft.Kind)
	}
	return nil
}

func validRoutineLead(value RoutineLeadHarness) bool {
	return value == RoutineLeadGrokBuild || value == RoutineLeadCodexAppServer
}

func validRoutineWorker(value RoutineWorker) bool {
	switch value {
	case RoutineWorkerClaude, RoutineWorkerPi, RoutineWorkerCodex, RoutineWorkerGrok, RoutineWorkerOpenCode, RoutineWorkerCursor:
		return true
	default:
		return false
	}
}

func validRoutineApproval(value RoutineApprovalPolicy) bool {
	return value == RoutineApprovalNever || value == RoutineApprovalOnRisk || value == RoutineApprovalAlways
}

func validStoredCronExpression(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len([]byte(value)) > RoutineCronExpressionMaxBytes {
		return false
	}
	fields := strings.Fields(value)
	if len(fields) != 5 {
		return false
	}
	ranges := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for index, field := range fields {
		if !validCronField(field, ranges[index][0], ranges[index][1]) {
			return false
		}
	}
	return true
}

func validCronField(field string, minimum, maximum int) bool {
	for _, listItem := range strings.Split(field, ",") {
		if listItem == "" {
			return false
		}
		base := listItem
		if strings.Contains(listItem, "/") {
			parts := strings.Split(listItem, "/")
			if len(parts) != 2 || parts[0] == "" {
				return false
			}
			step, err := strconv.Atoi(parts[1])
			if err != nil || step < 1 || step > maximum-minimum+1 {
				return false
			}
			base = parts[0]
		}
		if base == "*" {
			continue
		}
		if strings.Contains(base, "-") {
			bounds := strings.Split(base, "-")
			if len(bounds) != 2 {
				return false
			}
			start, startErr := strconv.Atoi(bounds[0])
			end, endErr := strconv.Atoi(bounds[1])
			if startErr != nil || endErr != nil || start < minimum || end > maximum || start > end {
				return false
			}
			continue
		}
		number, err := strconv.Atoi(base)
		if err != nil || number < minimum || number > maximum {
			return false
		}
	}
	return true
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func hasControlExceptWhitespace(value string) bool {
	return strings.IndexFunc(value, func(character rune) bool {
		return unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t'
	}) >= 0
}
