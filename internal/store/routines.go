package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
)

var (
	ErrRoutineNotFound            = errors.New("routine not found")
	ErrRoutineBotNotFound         = errors.New("routine bot not found")
	ErrRoutineRunNotFound         = errors.New("routine run not found")
	ErrRoutineDisabled            = errors.New("routine is disabled")
	ErrRoutinePaused              = errors.New("routine is paused")
	ErrRoutineNeedsAttention      = errors.New("routine needs attention")
	ErrRoutineNotDue              = errors.New("routine is not due")
	ErrRoutineRunActive           = errors.New("routine already has an active run")
	ErrRoutineLeaseLost           = errors.New("routine run lease is invalid or expired")
	ErrRoutineApprovalRequired    = errors.New("routine approval is required")
	ErrRoutineApprovalInvalid     = errors.New("routine approval is invalid")
	ErrRoutineIdempotencyConflict = errors.New("routine idempotency key conflicts with an earlier claim")
	ErrRoutineRetryExhausted      = errors.New("routine retry attempts are exhausted")
	ErrRoutineHeartbeatOptIn      = errors.New("routine heartbeat is not opted in")
	ErrRoutineNextRunRequired     = errors.New("next cron run is required")
)

const (
	minRoutineLease       = 5 * time.Second
	maxRoutineLease       = time.Hour
	maxRoutineOwnerBytes  = 256
	maxRoutineKeyBytes    = 256
	maxRoutineReasonBytes = 4096
	maxRoutineListLimit   = 1000
	routineTimeFormat     = "2006-01-02T15:04:05.000000000Z"
)

const routineSelectColumns = `id, bot_id, name, description, kind, status,
cron_expression, time_zone, heartbeat_opt_in, heartbeat_interval_seconds,
lead_harness, worker, approval_policy, retry_max_attempts, retry_backoff_seconds,
next_run_at, occurrence_key, retry_not_before, last_run_at, attention_reason, created_at, updated_at`

const routineRunSelectColumns = `id, routine_id, state, scheduled_for, attempt,
occurrence_key, lease_owner, lease_token, lease_expires_at, approval_id, idempotency_key,
claimed_at, started_at, finished_at, outcome_reason, trigger, created_at, updated_at`

// MigrateRoutines is additive and idempotent. Store.Open invokes it so the
// optional routine feature can be enabled without a separate database step;
// the migration still remains public for isolated persistence tests and
// future migration tooling.
func (s *Store) MigrateRoutines(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS routine_schedules (
  id TEXT PRIMARY KEY,
  bot_id TEXT NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 640),
  description TEXT NOT NULL DEFAULT '' CHECK(length(CAST(description AS BLOB)) <= 4096),
  kind TEXT NOT NULL CHECK(kind IN ('cron', 'heartbeat')),
  status TEXT NOT NULL DEFAULT 'disabled' CHECK(status IN ('disabled', 'enabled', 'paused', 'needs_attention')),
  cron_expression TEXT NOT NULL DEFAULT '',
  time_zone TEXT NOT NULL,
  heartbeat_opt_in INTEGER NOT NULL DEFAULT 0 CHECK(heartbeat_opt_in IN (0, 1)),
  heartbeat_interval_seconds INTEGER NOT NULL DEFAULT 0 CHECK(heartbeat_interval_seconds BETWEEN 0 AND 86400),
  lead_harness TEXT NOT NULL CHECK(lead_harness IN ('grok_build', 'codex_app_server')),
  worker TEXT NOT NULL CHECK(worker IN ('claude', 'pi', 'codex', 'grok', 'opencode', 'cursor')),
  approval_policy TEXT NOT NULL CHECK(approval_policy IN ('never', 'on_risk', 'always')),
  retry_max_attempts INTEGER NOT NULL CHECK(retry_max_attempts BETWEEN 1 AND 10),
  retry_backoff_seconds INTEGER NOT NULL CHECK(retry_backoff_seconds BETWEEN 0 AND 86400),
  next_run_at TEXT NOT NULL DEFAULT '',
  occurrence_key TEXT NOT NULL DEFAULT '',
  retry_not_before TEXT NOT NULL DEFAULT '',
  last_run_at TEXT NOT NULL DEFAULT '',
  attention_reason TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK(
    (kind = 'cron' AND cron_expression <> '' AND heartbeat_opt_in = 0 AND heartbeat_interval_seconds = 0)
    OR
    (kind = 'heartbeat' AND cron_expression = '' AND heartbeat_interval_seconds BETWEEN 30 AND 86400)
  ),
  CHECK(status <> 'enabled' OR (next_run_at <> '' AND occurrence_key <> '')),
  CHECK(next_run_at <> '' OR occurrence_key = ''),
  CHECK(kind <> 'heartbeat' OR status <> 'enabled' OR heartbeat_opt_in = 1)
);
CREATE INDEX IF NOT EXISTS routine_schedules_due_idx
  ON routine_schedules(status, next_run_at, retry_not_before, id)
  WHERE status = 'enabled';
CREATE INDEX IF NOT EXISTS routine_schedules_bot_idx
  ON routine_schedules(bot_id, updated_at DESC, id);

CREATE TABLE IF NOT EXISTS routine_run_ledger (
  id TEXT PRIMARY KEY,
  routine_id TEXT NOT NULL REFERENCES routine_schedules(id) ON DELETE CASCADE,
  state TEXT NOT NULL CHECK(state IN ('claimed', 'running', 'completed', 'failed', 'unknown')),
  scheduled_for TEXT NOT NULL,
  occurrence_key TEXT NOT NULL CHECK(occurrence_key <> ''),
  attempt INTEGER NOT NULL CHECK(attempt BETWEEN 1 AND 10),
  lease_owner TEXT NOT NULL DEFAULT '',
  lease_token TEXT NOT NULL DEFAULT '',
  lease_expires_at TEXT NOT NULL DEFAULT '',
  approval_id TEXT NOT NULL DEFAULT '',
  idempotency_key TEXT NOT NULL,
  request_hash BLOB NOT NULL CHECK(length(request_hash) = 32),
  claimed_at TEXT NOT NULL,
  started_at TEXT NOT NULL DEFAULT '',
  finished_at TEXT NOT NULL DEFAULT '',
  outcome_reason TEXT NOT NULL DEFAULT '',
  trigger TEXT DEFAULT 'schedule',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(routine_id, idempotency_key),
  UNIQUE(routine_id, occurrence_key, attempt)
);
CREATE UNIQUE INDEX IF NOT EXISTS routine_run_ledger_active_idx
  ON routine_run_ledger(routine_id)
  WHERE state IN ('claimed', 'running');
CREATE INDEX IF NOT EXISTS routine_run_ledger_stale_idx
  ON routine_run_ledger(state, lease_expires_at, id)
  WHERE state IN ('claimed', 'running');
CREATE INDEX IF NOT EXISTS routine_run_ledger_history_idx
  ON routine_run_ledger(routine_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS routine_history (
  id TEXT PRIMARY KEY,
  routine_id TEXT NOT NULL REFERENCES routine_schedules(id) ON DELETE CASCADE,
  run_id TEXT NOT NULL DEFAULT '',
  type TEXT NOT NULL,
  message TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS routine_history_routine_idx
  ON routine_history(routine_id, created_at, id);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate routines: %w", err)
	}
	if err := s.ensureRoutineLedgerTriggerColumn(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureRoutineLedgerTriggerColumn(ctx context.Context) error {
	found, err := hasSQLiteColumn(s.db, "routine_run_ledger", "trigger")
	if err != nil {
		return fmt.Errorf("migrate routines: %w", err)
	}
	if found {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE routine_run_ledger ADD COLUMN trigger TEXT DEFAULT 'schedule'`); err != nil {
		return fmt.Errorf("migrate routines: add trigger: %w", err)
	}
	return nil
}

func (s *Store) CreateRoutine(ctx context.Context, draft domain.RoutineDraft) (domain.Routine, error) {
	draft = normalizeRoutineDraft(draft)
	if err := draft.Validate(); err != nil {
		return domain.Routine{}, err
	}
	timestamp := routineTimestamp(time.Now())
	occurrenceKey := ""
	if draft.NextRunAt != "" {
		occurrenceKey = id.New("occurrence")
	}
	item := domain.Routine{
		ID:                       id.New("routine"),
		BotID:                    draft.BotID,
		Name:                     draft.Name,
		Description:              draft.Description,
		Kind:                     draft.Kind,
		Status:                   domain.RoutineStatusDisabled,
		CronExpression:           draft.CronExpression,
		TimeZone:                 draft.TimeZone,
		HeartbeatOptIn:           draft.HeartbeatOptIn,
		HeartbeatIntervalSeconds: draft.HeartbeatIntervalSeconds,
		LeadHarness:              draft.LeadHarness,
		Worker:                   draft.Worker,
		ApprovalPolicy:           draft.ApprovalPolicy,
		Retry:                    draft.Retry,
		NextRunAt:                draft.NextRunAt,
		OccurrenceKey:            occurrenceKey,
		CreatedAt:                timestamp,
		UpdatedAt:                timestamp,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Routine{}, fmt.Errorf("create routine: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO routine_schedules
(id, bot_id, name, description, kind, status, cron_expression, time_zone,
 heartbeat_opt_in, heartbeat_interval_seconds, lead_harness, worker,
 approval_policy, retry_max_attempts, retry_backoff_seconds, next_run_at, occurrence_key,
 retry_not_before, last_run_at, attention_reason, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '', ?, ?)`,
		item.ID, item.BotID, item.Name, item.Description, item.Kind, item.Status,
		item.CronExpression, item.TimeZone, item.HeartbeatOptIn, item.HeartbeatIntervalSeconds,
		item.LeadHarness, item.Worker, item.ApprovalPolicy, item.Retry.MaxAttempts,
		item.Retry.BackoffSeconds, item.NextRunAt, item.OccurrenceKey, item.CreatedAt, item.UpdatedAt); err != nil {
		if isSQLiteConstraintError(err) {
			var exists int
			if lookupErr := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM bots WHERE id = ?)", item.BotID).Scan(&exists); lookupErr == nil && exists == 0 {
				return domain.Routine{}, ErrRoutineBotNotFound
			}
		}
		return domain.Routine{}, fmt.Errorf("create routine: %w", err)
	}
	if err := appendRoutineEvent(ctx, tx, item.ID, "", "routine.created", "routine created disabled", timestamp); err != nil {
		return domain.Routine{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Routine{}, fmt.Errorf("create routine: commit: %w", err)
	}
	return item, nil
}

func (s *Store) GetRoutine(ctx context.Context, routineID string) (domain.Routine, error) {
	item, err := scanRoutine(s.db.QueryRowContext(ctx, "SELECT "+routineSelectColumns+" FROM routine_schedules WHERE id = ?", strings.TrimSpace(routineID)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Routine{}, ErrRoutineNotFound
	}
	if err != nil {
		return domain.Routine{}, fmt.Errorf("get routine: %w", err)
	}
	return item, nil
}

func (s *Store) ListRoutines(ctx context.Context, filter domain.RoutineListFilter) ([]domain.Routine, error) {
	query := "SELECT " + routineSelectColumns + " FROM routine_schedules WHERE 1 = 1"
	args := make([]any, 0, 3)
	if filter.BotID != "" {
		query += " AND bot_id = ?"
		args = append(args, strings.TrimSpace(filter.BotID))
	}
	if filter.Kind != "" {
		query += " AND kind = ?"
		args = append(args, filter.Kind)
	}
	if filter.Status != "" {
		query += " AND status = ?"
		args = append(args, filter.Status)
	}
	query += " ORDER BY updated_at DESC, id DESC"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list routines: %w", err)
	}
	defer rows.Close()
	items := make([]domain.Routine, 0)
	for rows.Next() {
		item, err := scanRoutine(rows)
		if err != nil {
			return nil, fmt.Errorf("list routines: scan: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list routines: iterate: %w", err)
	}
	return items, nil
}

func (s *Store) PauseRoutine(ctx context.Context, routineID, reason string) (domain.Routine, error) {
	return s.changeRoutineStatus(ctx, routineID, domain.RoutineStatusPaused, reason, time.Time{})
}

// ResumeRoutine explicitly enables a disabled or paused schedule. A supplied
// nextRunAt replaces the stored due time; otherwise a previously configured
// time must exist. Needs-attention records require an explicit resolution.
func (s *Store) ResumeRoutine(ctx context.Context, routineID string, nextRunAt time.Time) (domain.Routine, error) {
	return s.changeRoutineStatus(ctx, routineID, domain.RoutineStatusEnabled, "", nextRunAt)
}

func (s *Store) ResolveRoutineAttention(ctx context.Context, routineID string, nextRunAt time.Time) (domain.Routine, error) {
	return s.changeRoutineStatus(ctx, routineID, domain.RoutineStatusEnabled, "resolve_attention", nextRunAt)
}

func (s *Store) SetRoutineHeartbeatOptIn(ctx context.Context, routineID string, optedIn bool) (domain.Routine, error) {
	conn, rollback, err := s.beginRoutineImmediate(ctx)
	if err != nil {
		return domain.Routine{}, err
	}
	defer rollback()
	item, err := loadRoutineFrom(ctx, conn, routineID)
	if err != nil {
		return domain.Routine{}, err
	}
	if item.Kind != domain.RoutineKindHeartbeat {
		return domain.Routine{}, errors.New("heartbeat opt-in applies only to heartbeat routines")
	}
	if !optedIn {
		if active, err := routineHasActiveRun(ctx, conn, item.ID); err != nil {
			return domain.Routine{}, err
		} else if active {
			return domain.Routine{}, ErrRoutineRunActive
		}
		if item.Status != domain.RoutineStatusNeedsAttention {
			item.Status = domain.RoutineStatusDisabled
			item.AttentionReason = ""
		}
		item.NextRunAt = ""
		item.OccurrenceKey = ""
		item.RetryNotBefore = ""
	}
	item.HeartbeatOptIn = optedIn
	item.UpdatedAt = routineTimestamp(time.Now())
	if _, err := conn.ExecContext(ctx, `UPDATE routine_schedules
SET heartbeat_opt_in = ?, status = ?, next_run_at = ?, occurrence_key = ?, retry_not_before = ?, attention_reason = ?, updated_at = ? WHERE id = ?`,
		item.HeartbeatOptIn, item.Status, item.NextRunAt, item.OccurrenceKey, item.RetryNotBefore, item.AttentionReason, item.UpdatedAt, item.ID); err != nil {
		return domain.Routine{}, fmt.Errorf("set routine heartbeat opt-in: %w", err)
	}
	message := "heartbeat opt-in disabled"
	if optedIn {
		message = "heartbeat opt-in enabled"
	}
	if err := appendRoutineEvent(ctx, conn, item.ID, "", "routine.heartbeat_opt_in", message, item.UpdatedAt); err != nil {
		return domain.Routine{}, err
	}
	if err := commitRoutineImmediate(ctx, conn, "set routine heartbeat opt-in"); err != nil {
		return domain.Routine{}, err
	}
	return item, nil
}

func (s *Store) ListDueRoutines(ctx context.Context, at time.Time, limit int) ([]domain.Routine, error) {
	at = routineNow(at)
	limit = boundedRoutineLimit(limit)
	rows, err := s.db.QueryContext(ctx, `SELECT `+routineSelectColumns+`
FROM routine_schedules r
WHERE r.status = 'enabled' AND r.next_run_at <> '' AND r.next_run_at <= ?
  AND (r.retry_not_before = '' OR r.retry_not_before <= ?)
  AND NOT EXISTS (
    SELECT 1 FROM routine_run_ledger l
    WHERE l.routine_id = r.id AND l.state IN ('claimed', 'running')
  )
ORDER BY r.next_run_at, r.id LIMIT ?`, routineTimestamp(at), routineTimestamp(at), limit)
	if err != nil {
		return nil, fmt.Errorf("list due routines: %w", err)
	}
	defer rows.Close()
	items := make([]domain.Routine, 0)
	for rows.Next() {
		item, err := scanRoutine(rows)
		if err != nil {
			return nil, fmt.Errorf("list due routines: scan: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list due routines: iterate: %w", err)
	}
	return items, nil
}

// ClaimTestRoutineRun starts a leased ledger occurrence without requiring the
// routine to be enabled or due. It uses a distinct occurrence key so finishing
// the test does not consume the scheduled next run.
func (s *Store) ClaimTestRoutineRun(ctx context.Context, claim domain.RoutineClaim) (domain.RoutineRun, error) {
	claim.Trigger = domain.RoutineTriggerTest
	return s.ClaimRoutineRun(ctx, claim)
}

// ClaimRoutineRun atomically creates one leased ledger occurrence. Its
// idempotency key is mandatory and scoped to the routine. Repeating the exact
// claim returns the original row, even after it becomes terminal.
func (s *Store) ClaimRoutineRun(ctx context.Context, claim domain.RoutineClaim) (domain.RoutineRun, error) {
	claim.RoutineID = strings.TrimSpace(claim.RoutineID)
	claim.LeaseOwner = strings.TrimSpace(claim.LeaseOwner)
	claim.IdempotencyKey = strings.TrimSpace(claim.IdempotencyKey)
	claim.ApprovalID = strings.TrimSpace(claim.ApprovalID)
	claim.OccurrenceKey = strings.TrimSpace(claim.OccurrenceKey)
	claim.Trigger = strings.TrimSpace(claim.Trigger)
	if claim.Trigger == "" {
		claim.Trigger = domain.RoutineTriggerSchedule
	}
	claim.Now = routineNow(claim.Now)
	if claim.Trigger != domain.RoutineTriggerSchedule && claim.Trigger != domain.RoutineTriggerTest {
		return domain.RoutineRun{}, fmt.Errorf("unsupported routine trigger %q", claim.Trigger)
	}
	if claim.RoutineID == "" || !validRoutineTokenText(claim.LeaseOwner, maxRoutineOwnerBytes) {
		return domain.RoutineRun{}, errors.New("routine id and printable lease owner are required")
	}
	if !validRoutineTokenText(claim.IdempotencyKey, maxRoutineKeyBytes) {
		return domain.RoutineRun{}, errors.New("routine idempotency key is required")
	}
	if claim.ApprovalID != "" && !validRoutineTokenText(claim.ApprovalID, maxRoutineKeyBytes) {
		return domain.RoutineRun{}, ErrRoutineApprovalInvalid
	}
	if claim.LeaseDuration < minRoutineLease || claim.LeaseDuration > maxRoutineLease {
		return domain.RoutineRun{}, fmt.Errorf("routine lease must be between %s and %s", minRoutineLease, maxRoutineLease)
	}
	requestHash := routineClaimHash(claim.RoutineID, claim.LeaseOwner, claim.ApprovalID)
	conn, rollback, err := s.beginRoutineImmediate(ctx)
	if err != nil {
		return domain.RoutineRun{}, err
	}
	defer rollback()

	var storedHash []byte
	existing, err := scanRoutineRunWithHash(conn.QueryRowContext(ctx, "SELECT "+routineRunSelectColumns+", request_hash FROM routine_run_ledger WHERE routine_id = ? AND idempotency_key = ?", claim.RoutineID, claim.IdempotencyKey), &storedHash)
	if err == nil {
		if len(storedHash) != sha256.Size || !equalRoutineHash(storedHash, requestHash[:]) {
			return domain.RoutineRun{}, ErrRoutineIdempotencyConflict
		}
		if err := commitRoutineImmediate(ctx, conn, "read idempotent routine claim"); err != nil {
			return domain.RoutineRun{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.RoutineRun{}, fmt.Errorf("load idempotent routine claim: %w", err)
	}

	routine, err := loadRoutineFrom(ctx, conn, claim.RoutineID)
	if err != nil {
		return domain.RoutineRun{}, err
	}
	if routine.Status == domain.RoutineStatusNeedsAttention {
		return domain.RoutineRun{}, ErrRoutineNeedsAttention
	}
	isTest := claim.Trigger == domain.RoutineTriggerTest
	if !isTest {
		if routine.Status == domain.RoutineStatusPaused {
			return domain.RoutineRun{}, ErrRoutinePaused
		}
		if routine.Status != domain.RoutineStatusEnabled {
			return domain.RoutineRun{}, ErrRoutineDisabled
		}
		if routine.Kind == domain.RoutineKindHeartbeat && !routine.HeartbeatOptIn {
			return domain.RoutineRun{}, ErrRoutineHeartbeatOptIn
		}
	}
	if routine.ApprovalPolicy == domain.RoutineApprovalAlways && claim.ApprovalID == "" {
		return domain.RoutineRun{}, ErrRoutineApprovalRequired
	}
	occurrenceKey := routine.OccurrenceKey
	scheduledFor := routine.NextRunAt
	if isTest {
		occurrenceKey = claim.OccurrenceKey
		if occurrenceKey == "" {
			occurrenceKey = id.New("occurrence")
		}
		scheduledFor = routineTimestamp(claim.Now)
	}
	if claim.ApprovalID != "" {
		if err := validateRoutineApproval(ctx, conn, claim.ApprovalID, routine.ID, occurrenceKey, claim.Trigger); err != nil {
			return domain.RoutineRun{}, err
		}
	}
	if !isTest {
		if routine.NextRunAt == "" || routine.OccurrenceKey == "" || routine.NextRunAt > routineTimestamp(claim.Now) {
			return domain.RoutineRun{}, ErrRoutineNotDue
		}
		if routine.RetryNotBefore != "" && routine.RetryNotBefore > routineTimestamp(claim.Now) {
			return domain.RoutineRun{}, ErrRoutineNotDue
		}
	}
	if active, err := routineHasActiveRun(ctx, conn, routine.ID); err != nil {
		return domain.RoutineRun{}, err
	} else if active {
		return domain.RoutineRun{}, ErrRoutineRunActive
	}
	var previousAttempts int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM routine_run_ledger
WHERE routine_id = ? AND occurrence_key = ?`, routine.ID, occurrenceKey).Scan(&previousAttempts); err != nil {
		return domain.RoutineRun{}, fmt.Errorf("count routine attempts: %w", err)
	}
	attempt := previousAttempts + 1
	if attempt > routine.Retry.MaxAttempts {
		return domain.RoutineRun{}, ErrRoutineRetryExhausted
	}
	leaseToken, err := newRoutineLeaseToken()
	if err != nil {
		return domain.RoutineRun{}, err
	}
	timestamp := routineTimestamp(claim.Now)
	run := domain.RoutineRun{
		ID:             id.New("routine-run"),
		RoutineID:      routine.ID,
		State:          domain.RoutineLedgerClaimed,
		ScheduledFor:   scheduledFor,
		OccurrenceKey:  occurrenceKey,
		Attempt:        attempt,
		LeaseOwner:     claim.LeaseOwner,
		LeaseToken:     leaseToken,
		LeaseExpiresAt: routineTimestamp(claim.Now.Add(claim.LeaseDuration)),
		ApprovalID:     claim.ApprovalID,
		IdempotencyKey: claim.IdempotencyKey,
		Trigger:        claim.Trigger,
		ClaimedAt:      timestamp,
		CreatedAt:      timestamp,
		UpdatedAt:      timestamp,
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO routine_run_ledger
(id, routine_id, state, scheduled_for, occurrence_key, attempt, lease_owner, lease_token,
 lease_expires_at, approval_id, idempotency_key, request_hash, claimed_at,
 started_at, finished_at, outcome_reason, trigger, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '', ?, ?, ?)`,
		run.ID, run.RoutineID, run.State, run.ScheduledFor, run.OccurrenceKey, run.Attempt,
		run.LeaseOwner, run.LeaseToken, run.LeaseExpiresAt, run.ApprovalID,
		run.IdempotencyKey, requestHash[:], run.ClaimedAt, run.Trigger, run.CreatedAt, run.UpdatedAt); err != nil {
		if isSQLiteConstraintError(err) {
			if active, activeErr := routineHasActiveRun(ctx, conn, routine.ID); activeErr == nil && active {
				return domain.RoutineRun{}, ErrRoutineRunActive
			}
		}
		return domain.RoutineRun{}, fmt.Errorf("claim routine run: %w", err)
	}
	claimedMessage := fmt.Sprintf("attempt %d claimed", run.Attempt)
	if isTest {
		claimedMessage = fmt.Sprintf("test attempt %d claimed", run.Attempt)
	}
	if err := appendRoutineEvent(ctx, conn, routine.ID, run.ID, "run.claimed", claimedMessage, timestamp); err != nil {
		return domain.RoutineRun{}, err
	}
	if err := commitRoutineImmediate(ctx, conn, "claim routine run"); err != nil {
		return domain.RoutineRun{}, err
	}
	return run, nil
}

func (s *Store) StartRoutineRun(ctx context.Context, runID, leaseOwner, leaseToken string, at time.Time) (domain.RoutineRun, error) {
	at = routineNow(at)
	return s.transitionRoutineLease(ctx, runID, leaseOwner, leaseToken, domain.RoutineLedgerClaimed, domain.RoutineLedgerRunning, at, 0)
}

func (s *Store) RenewRoutineRunLease(ctx context.Context, runID, leaseOwner, leaseToken string, at time.Time, leaseDuration time.Duration) (domain.RoutineRun, error) {
	if leaseDuration < minRoutineLease || leaseDuration > maxRoutineLease {
		return domain.RoutineRun{}, fmt.Errorf("routine lease must be between %s and %s", minRoutineLease, maxRoutineLease)
	}
	at = routineNow(at)
	return s.transitionRoutineLease(ctx, runID, leaseOwner, leaseToken, "", "", at, leaseDuration)
}

func (s *Store) FinishRoutineRun(ctx context.Context, finish domain.RoutineFinish) (domain.RoutineRun, error) {
	finish.RunID = strings.TrimSpace(finish.RunID)
	finish.LeaseOwner = strings.TrimSpace(finish.LeaseOwner)
	finish.Reason = strings.TrimSpace(finish.Reason)
	finish.Now = routineNow(finish.Now)
	if finish.State != domain.RoutineLedgerCompleted && finish.State != domain.RoutineLedgerFailed {
		return domain.RoutineRun{}, errors.New("routine finish state must be completed or failed")
	}
	if finish.RunID == "" || finish.LeaseOwner == "" || finish.LeaseToken == "" {
		return domain.RoutineRun{}, ErrRoutineLeaseLost
	}
	if len([]byte(finish.Reason)) > maxRoutineReasonBytes {
		return domain.RoutineRun{}, fmt.Errorf("routine finish reason must be at most %d bytes", maxRoutineReasonBytes)
	}
	conn, rollback, err := s.beginRoutineImmediate(ctx)
	if err != nil {
		return domain.RoutineRun{}, err
	}
	defer rollback()
	run, err := loadRoutineRunFrom(ctx, conn, finish.RunID)
	if err != nil {
		return domain.RoutineRun{}, err
	}
	if !routineLeaseValid(run, finish.LeaseOwner, finish.LeaseToken, finish.Now) {
		return domain.RoutineRun{}, ErrRoutineLeaseLost
	}
	routine, err := loadRoutineFrom(ctx, conn, run.RoutineID)
	if err != nil {
		return domain.RoutineRun{}, err
	}
	isTest := run.Trigger == domain.RoutineTriggerTest
	if !isTest && finish.State == domain.RoutineLedgerCompleted && routine.Kind == domain.RoutineKindCron {
		if finish.NextRunAt.IsZero() || !finish.NextRunAt.After(finish.Now) {
			return domain.RoutineRun{}, ErrRoutineNextRunRequired
		}
	}
	timestamp := routineTimestamp(finish.Now)
	run.State = finish.State
	run.FinishedAt = timestamp
	run.OutcomeReason = finish.Reason
	run.LeaseToken = ""
	run.LeaseExpiresAt = ""
	run.UpdatedAt = timestamp
	result, err := conn.ExecContext(ctx, `UPDATE routine_run_ledger
SET state = ?, lease_token = '', lease_expires_at = '', finished_at = ?, outcome_reason = ?, updated_at = ?
WHERE id = ? AND lease_owner = ? AND lease_token = ? AND state IN ('claimed', 'running') AND lease_expires_at > ?`,
		run.State, run.FinishedAt, run.OutcomeReason, run.UpdatedAt, run.ID,
		finish.LeaseOwner, finish.LeaseToken, timestamp)
	if err != nil {
		return domain.RoutineRun{}, fmt.Errorf("finish routine run: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return domain.RoutineRun{}, ErrRoutineLeaseLost
	}
	if !isTest {
		if finish.State == domain.RoutineLedgerCompleted {
			nextRun := finish.NextRunAt.UTC()
			if routine.Kind == domain.RoutineKindHeartbeat {
				nextRun = finish.Now.Add(time.Duration(routine.HeartbeatIntervalSeconds) * time.Second)
			}
			routine.NextRunAt = routineTimestamp(nextRun)
			routine.OccurrenceKey = id.New("occurrence")
			routine.RetryNotBefore = ""
			routine.LastRunAt = timestamp
			routine.AttentionReason = ""
		} else if run.Attempt < routine.Retry.MaxAttempts {
			routine.RetryNotBefore = routineTimestamp(finish.Now.Add(time.Duration(routine.Retry.BackoffSeconds) * time.Second))
		} else {
			routine.Status = domain.RoutineStatusNeedsAttention
			routine.RetryNotBefore = ""
			routine.AttentionReason = routineFailureReason(finish.Reason)
		}
		routine.UpdatedAt = timestamp
		if _, err := conn.ExecContext(ctx, `UPDATE routine_schedules
SET status = ?, next_run_at = ?, occurrence_key = ?, retry_not_before = ?, last_run_at = ?, attention_reason = ?, updated_at = ? WHERE id = ?`,
			routine.Status, routine.NextRunAt, routine.OccurrenceKey, routine.RetryNotBefore, routine.LastRunAt,
			routine.AttentionReason, routine.UpdatedAt, routine.ID); err != nil {
			return domain.RoutineRun{}, fmt.Errorf("finish routine run: update routine: %w", err)
		}
	}
	if err := appendRoutineEvent(ctx, conn, routine.ID, run.ID, "run."+string(run.State), run.OutcomeReason, timestamp); err != nil {
		return domain.RoutineRun{}, err
	}
	if err := commitRoutineImmediate(ctx, conn, "finish routine run"); err != nil {
		return domain.RoutineRun{}, err
	}
	return run, nil
}

// RecoverStaleRoutineRuns maps expired claimed/running leases to unknown. It
// never guesses that external work completed, and it never automatically
// launches a replacement.
func (s *Store) RecoverStaleRoutineRuns(ctx context.Context, at time.Time, limit int) ([]domain.RoutineRun, error) {
	at = routineNow(at)
	limit = boundedRoutineLimit(limit)
	conn, rollback, err := s.beginRoutineImmediate(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback()
	rows, err := conn.QueryContext(ctx, `SELECT `+routineRunSelectColumns+`
FROM routine_run_ledger
WHERE state IN ('claimed', 'running') AND lease_expires_at <> '' AND lease_expires_at <= ?
ORDER BY lease_expires_at, id LIMIT ?`, routineTimestamp(at), limit)
	if err != nil {
		return nil, fmt.Errorf("recover stale routine runs: list: %w", err)
	}
	stale := make([]domain.RoutineRun, 0)
	for rows.Next() {
		run, scanErr := scanRoutineRun(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("recover stale routine runs: scan: %w", scanErr)
		}
		stale = append(stale, run)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("recover stale routine runs: iterate: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("recover stale routine runs: close: %w", err)
	}
	timestamp := routineTimestamp(at)
	recovered := make([]domain.RoutineRun, 0, len(stale))
	for _, run := range stale {
		result, err := conn.ExecContext(ctx, `UPDATE routine_run_ledger
SET state = 'unknown', lease_token = '', lease_expires_at = '', finished_at = ?,
    outcome_reason = 'lease expired before outcome was known', updated_at = ?
WHERE id = ? AND state IN ('claimed', 'running') AND lease_expires_at <> '' AND lease_expires_at <= ?`,
			timestamp, timestamp, run.ID, timestamp)
		if err != nil {
			return nil, fmt.Errorf("recover stale routine run: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("recover stale routine run: inspect: %w", err)
		}
		if changed != 1 {
			continue
		}
		run.State = domain.RoutineLedgerUnknown
		run.LeaseToken = ""
		run.LeaseExpiresAt = ""
		run.FinishedAt = timestamp
		run.OutcomeReason = "lease expired before outcome was known"
		run.UpdatedAt = timestamp
		if run.Trigger != domain.RoutineTriggerTest {
			routine, err := loadRoutineFrom(ctx, conn, run.RoutineID)
			if err != nil {
				return nil, err
			}
			// An expired lease says nothing about the external side effect. Do not
			// use the ordinary failure retry path here: launching another attempt
			// could duplicate a payment, message, browser action, or deployment
			// while the old worker is still alive. An operator must resolve this
			// occurrence explicitly before the routine can run again.
			routine.Status = domain.RoutineStatusNeedsAttention
			routine.RetryNotBefore = ""
			routine.AttentionReason = "run lease expired; outcome unknown"
			if _, err := conn.ExecContext(ctx, `UPDATE routine_schedules
SET status = ?, retry_not_before = ?, attention_reason = ?, updated_at = ? WHERE id = ?`,
				routine.Status, routine.RetryNotBefore, routine.AttentionReason, timestamp, routine.ID); err != nil {
				return nil, fmt.Errorf("recover stale routine run: update routine: %w", err)
			}
		}
		if err := appendRoutineEvent(ctx, conn, run.RoutineID, run.ID, "run.unknown", run.OutcomeReason, timestamp); err != nil {
			return nil, err
		}
		recovered = append(recovered, run)
	}
	if err := commitRoutineImmediate(ctx, conn, "recover stale routine runs"); err != nil {
		return nil, err
	}
	return recovered, nil
}

func (s *Store) ListRoutineRuns(ctx context.Context, routineID string, limit int) ([]domain.RoutineRun, error) {
	limit = boundedRoutineLimit(limit)
	rows, err := s.db.QueryContext(ctx, `SELECT `+routineRunSelectColumns+`
FROM routine_run_ledger WHERE routine_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, strings.TrimSpace(routineID), limit)
	if err != nil {
		return nil, fmt.Errorf("list routine runs: %w", err)
	}
	defer rows.Close()
	items := make([]domain.RoutineRun, 0)
	for rows.Next() {
		item, err := scanRoutineRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list routine runs: scan: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list routine runs: iterate: %w", err)
	}
	return items, nil
}

func (s *Store) ListRoutineHistory(ctx context.Context, routineID string, limit int) ([]domain.RoutineEvent, error) {
	limit = boundedRoutineLimit(limit)
	rows, err := s.db.QueryContext(ctx, `SELECT id, routine_id, run_id, type, message, created_at
FROM routine_history WHERE routine_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, strings.TrimSpace(routineID), limit)
	if err != nil {
		return nil, fmt.Errorf("list routine history: %w", err)
	}
	defer rows.Close()
	items := make([]domain.RoutineEvent, 0)
	for rows.Next() {
		var item domain.RoutineEvent
		if err := rows.Scan(&item.ID, &item.RoutineID, &item.RunID, &item.Type, &item.Message, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("list routine history: scan: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list routine history: iterate: %w", err)
	}
	return items, nil
}

func (s *Store) changeRoutineStatus(ctx context.Context, routineID string, target domain.RoutineStatus, reason string, nextRunAt time.Time) (domain.Routine, error) {
	conn, rollback, err := s.beginRoutineImmediate(ctx)
	if err != nil {
		return domain.Routine{}, err
	}
	defer rollback()
	item, err := loadRoutineFrom(ctx, conn, routineID)
	if err != nil {
		return domain.Routine{}, err
	}
	if active, err := routineHasActiveRun(ctx, conn, item.ID); err != nil {
		return domain.Routine{}, err
	} else if active {
		return domain.Routine{}, ErrRoutineRunActive
	}
	if target == domain.RoutineStatusEnabled {
		if reason == "resolve_attention" && item.Status != domain.RoutineStatusNeedsAttention {
			return domain.Routine{}, ErrRoutineNeedsAttention
		}
		if item.Status == domain.RoutineStatusNeedsAttention && reason != "resolve_attention" {
			return domain.Routine{}, ErrRoutineNeedsAttention
		}
		if item.Kind == domain.RoutineKindHeartbeat && !item.HeartbeatOptIn {
			return domain.Routine{}, ErrRoutineHeartbeatOptIn
		}
		if !nextRunAt.IsZero() {
			item.NextRunAt = routineTimestamp(nextRunAt)
			item.OccurrenceKey = id.New("occurrence")
		}
		if item.NextRunAt == "" {
			return domain.Routine{}, ErrRoutineNextRunRequired
		}
		if item.OccurrenceKey == "" {
			item.OccurrenceKey = id.New("occurrence")
		}
		item.Status = domain.RoutineStatusEnabled
		item.AttentionReason = ""
	} else if target == domain.RoutineStatusPaused {
		if item.Status == domain.RoutineStatusNeedsAttention {
			return domain.Routine{}, ErrRoutineNeedsAttention
		}
		item.Status = domain.RoutineStatusPaused
		item.AttentionReason = strings.TrimSpace(reason)
	} else {
		return domain.Routine{}, errors.New("unsupported routine status transition")
	}
	item.UpdatedAt = routineTimestamp(time.Now())
	if _, err := conn.ExecContext(ctx, `UPDATE routine_schedules
SET status = ?, next_run_at = ?, occurrence_key = ?, attention_reason = ?, updated_at = ? WHERE id = ?`,
		item.Status, item.NextRunAt, item.OccurrenceKey, item.AttentionReason, item.UpdatedAt, item.ID); err != nil {
		return domain.Routine{}, fmt.Errorf("change routine status: %w", err)
	}
	eventType := "routine.paused"
	if target == domain.RoutineStatusEnabled {
		eventType = "routine.resumed"
		if reason == "resolve_attention" {
			eventType = "routine.attention_resolved"
		}
	}
	if err := appendRoutineEvent(ctx, conn, item.ID, "", eventType, strings.TrimSpace(reason), item.UpdatedAt); err != nil {
		return domain.Routine{}, err
	}
	if err := commitRoutineImmediate(ctx, conn, "change routine status"); err != nil {
		return domain.Routine{}, err
	}
	return item, nil
}

func (s *Store) transitionRoutineLease(ctx context.Context, runID, leaseOwner, leaseToken string, from, to domain.RoutineLedgerState, at time.Time, leaseDuration time.Duration) (domain.RoutineRun, error) {
	runID = strings.TrimSpace(runID)
	leaseOwner = strings.TrimSpace(leaseOwner)
	if runID == "" || leaseOwner == "" || leaseToken == "" {
		return domain.RoutineRun{}, ErrRoutineLeaseLost
	}
	conn, rollback, err := s.beginRoutineImmediate(ctx)
	if err != nil {
		return domain.RoutineRun{}, err
	}
	defer rollback()
	run, err := loadRoutineRunFrom(ctx, conn, runID)
	if err != nil {
		return domain.RoutineRun{}, err
	}
	if !routineLeaseValid(run, leaseOwner, leaseToken, at) {
		return domain.RoutineRun{}, ErrRoutineLeaseLost
	}
	if leaseDuration == 0 && run.State == to {
		if err := commitRoutineImmediate(ctx, conn, "read idempotent routine start"); err != nil {
			return domain.RoutineRun{}, err
		}
		return run, nil
	}
	timestamp := routineTimestamp(at)
	if leaseDuration > 0 {
		run.LeaseExpiresAt = routineTimestamp(at.Add(leaseDuration))
		run.UpdatedAt = timestamp
		result, err := conn.ExecContext(ctx, `UPDATE routine_run_ledger
SET lease_expires_at = ?, updated_at = ?
WHERE id = ? AND lease_owner = ? AND lease_token = ? AND state IN ('claimed', 'running') AND lease_expires_at > ?`,
			run.LeaseExpiresAt, run.UpdatedAt, run.ID, leaseOwner, leaseToken, timestamp)
		if err != nil {
			return domain.RoutineRun{}, fmt.Errorf("renew routine lease: %w", err)
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return domain.RoutineRun{}, ErrRoutineLeaseLost
		}
	} else {
		if run.State != from {
			return domain.RoutineRun{}, ErrRoutineLeaseLost
		}
		run.State = to
		run.StartedAt = timestamp
		run.UpdatedAt = timestamp
		result, err := conn.ExecContext(ctx, `UPDATE routine_run_ledger
SET state = ?, started_at = ?, updated_at = ?
WHERE id = ? AND lease_owner = ? AND lease_token = ? AND state = ? AND lease_expires_at > ?`,
			run.State, run.StartedAt, run.UpdatedAt, run.ID, leaseOwner, leaseToken, from, timestamp)
		if err != nil {
			return domain.RoutineRun{}, fmt.Errorf("start routine run: %w", err)
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return domain.RoutineRun{}, ErrRoutineLeaseLost
		}
		if err := appendRoutineEvent(ctx, conn, run.RoutineID, run.ID, "run.running", "routine run started", timestamp); err != nil {
			return domain.RoutineRun{}, err
		}
	}
	if err := commitRoutineImmediate(ctx, conn, "transition routine lease"); err != nil {
		return domain.RoutineRun{}, err
	}
	return run, nil
}

type routineSQL interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type routineScanner interface {
	Scan(...any) error
}

func scanRoutine(row routineScanner) (domain.Routine, error) {
	var item domain.Routine
	var heartbeatOptIn int
	err := row.Scan(
		&item.ID, &item.BotID, &item.Name, &item.Description, &item.Kind, &item.Status,
		&item.CronExpression, &item.TimeZone, &heartbeatOptIn, &item.HeartbeatIntervalSeconds,
		&item.LeadHarness, &item.Worker, &item.ApprovalPolicy, &item.Retry.MaxAttempts,
		&item.Retry.BackoffSeconds, &item.NextRunAt, &item.OccurrenceKey, &item.RetryNotBefore, &item.LastRunAt,
		&item.AttentionReason, &item.CreatedAt, &item.UpdatedAt,
	)
	item.HeartbeatOptIn = heartbeatOptIn == 1
	return item, err
}

func scanRoutineRun(row routineScanner) (domain.RoutineRun, error) {
	var item domain.RoutineRun
	err := row.Scan(
		&item.ID, &item.RoutineID, &item.State, &item.ScheduledFor, &item.Attempt,
		&item.OccurrenceKey, &item.LeaseOwner, &item.LeaseToken, &item.LeaseExpiresAt, &item.ApprovalID,
		&item.IdempotencyKey, &item.ClaimedAt, &item.StartedAt, &item.FinishedAt,
		&item.OutcomeReason, &item.Trigger, &item.CreatedAt, &item.UpdatedAt,
	)
	if item.Trigger == "" {
		item.Trigger = domain.RoutineTriggerSchedule
	}
	return item, err
}

func scanRoutineRunWithHash(row routineScanner, hash *[]byte) (domain.RoutineRun, error) {
	var item domain.RoutineRun
	err := row.Scan(
		&item.ID, &item.RoutineID, &item.State, &item.ScheduledFor, &item.Attempt,
		&item.OccurrenceKey, &item.LeaseOwner, &item.LeaseToken, &item.LeaseExpiresAt, &item.ApprovalID,
		&item.IdempotencyKey, &item.ClaimedAt, &item.StartedAt, &item.FinishedAt,
		&item.OutcomeReason, &item.Trigger, &item.CreatedAt, &item.UpdatedAt, hash,
	)
	if item.Trigger == "" {
		item.Trigger = domain.RoutineTriggerSchedule
	}
	return item, err
}

func loadRoutineFrom(ctx context.Context, source routineSQL, routineID string) (domain.Routine, error) {
	item, err := scanRoutine(source.QueryRowContext(ctx, "SELECT "+routineSelectColumns+" FROM routine_schedules WHERE id = ?", strings.TrimSpace(routineID)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Routine{}, ErrRoutineNotFound
	}
	if err != nil {
		return domain.Routine{}, fmt.Errorf("load routine: %w", err)
	}
	return item, nil
}

func loadRoutineRunFrom(ctx context.Context, source routineSQL, runID string) (domain.RoutineRun, error) {
	item, err := scanRoutineRun(source.QueryRowContext(ctx, "SELECT "+routineRunSelectColumns+" FROM routine_run_ledger WHERE id = ?", strings.TrimSpace(runID)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RoutineRun{}, ErrRoutineRunNotFound
	}
	if err != nil {
		return domain.RoutineRun{}, fmt.Errorf("load routine run: %w", err)
	}
	return item, nil
}

func routineHasActiveRun(ctx context.Context, source routineSQL, routineID string) (bool, error) {
	var active int
	if err := source.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM routine_run_ledger WHERE routine_id = ? AND state IN ('claimed', 'running'))`, routineID).Scan(&active); err != nil {
		return false, fmt.Errorf("inspect active routine run: %w", err)
	}
	return active == 1, nil
}

func appendRoutineEvent(ctx context.Context, target interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, routineID, runID, eventType, message, timestamp string) error {
	if _, err := target.ExecContext(ctx, `INSERT INTO routine_history
(id, routine_id, run_id, type, message, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id.New("routine-event"), routineID, runID, eventType, message, timestamp); err != nil {
		return fmt.Errorf("append routine history: %w", err)
	}
	return nil
}

func (s *Store) beginRoutineImmediate(ctx context.Context) (*sql.Conn, func(), error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, func() {}, fmt.Errorf("acquire routine connection: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		_ = conn.Close()
		return nil, func() {}, fmt.Errorf("configure routine transaction: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Close()
		return nil, func() {}, fmt.Errorf("begin routine transaction: %w", err)
	}
	rollback := func() {
		// ROLLBACK after a successful COMMIT is a harmless no-op. Keeping cleanup
		// unconditional guarantees the dedicated connection is always returned.
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		_ = conn.Close()
	}
	return conn, rollback, nil
}

func commitRoutineImmediate(ctx context.Context, conn *sql.Conn, operation string) error {
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("%s: commit: %w", operation, err)
	}
	return nil
}

func normalizeRoutineDraft(draft domain.RoutineDraft) domain.RoutineDraft {
	draft.BotID = strings.TrimSpace(draft.BotID)
	draft.Name = strings.TrimSpace(draft.Name)
	draft.Description = strings.TrimSpace(draft.Description)
	draft.CronExpression = strings.Join(strings.Fields(draft.CronExpression), " ")
	draft.TimeZone = strings.TrimSpace(draft.TimeZone)
	if draft.ApprovalPolicy == "" {
		draft.ApprovalPolicy = domain.RoutineApprovalOnRisk
	}
	if draft.Retry.MaxAttempts == 0 {
		draft.Retry.MaxAttempts = 1
	}
	if draft.NextRunAt != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, draft.NextRunAt); err == nil {
			draft.NextRunAt = routineTimestamp(parsed)
		}
	}
	return draft
}

func routineNow(value time.Time) time.Time {
	if value.IsZero() {
		value = time.Now()
	}
	return value.UTC()
}

func routineTimestamp(value time.Time) string {
	// SQLite compares due and lease timestamps as text. Fixed-width UTC values
	// preserve chronological order even around whole/sub-second boundaries.
	return value.UTC().Format(routineTimeFormat)
}

func boundedRoutineLimit(limit int) int {
	if limit <= 0 || limit > maxRoutineListLimit {
		return maxRoutineListLimit
	}
	return limit
}

func validRoutineTokenText(value string, maxBytes int) bool {
	return value != "" && len([]byte(value)) <= maxBytes && strings.IndexFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character)
	}) < 0
}

func newRoutineLeaseToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate routine lease token: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func routineClaimHash(routineID, owner, approvalID string) [32]byte {
	return sha256.Sum256([]byte(routineID + "\x00" + owner + "\x00" + approvalID))
}

// RoutineApprovalAction binds an existing controller-owned approval to one
// routine occurrence. ClaimRoutineRun accepts an approval only when its stored
// action matches this value and the approval is already approved.
func RoutineApprovalAction(routineID, occurrenceKey string) string {
	return "routine.run:" + strings.TrimSpace(routineID) + ":" + strings.TrimSpace(occurrenceKey)
}

// RoutineTestApprovalAction binds an approval to a test occurrence so deny
// cannot rotate the scheduled next run.
func RoutineTestApprovalAction(routineID, occurrenceKey string) string {
	return "routine.test:" + strings.TrimSpace(routineID) + ":" + strings.TrimSpace(occurrenceKey)
}

// ParseRoutineApprovalAction extracts trigger, routine id, and occurrence key
// from a scheduled or test gate-approval action.
func ParseRoutineApprovalAction(action string) (trigger, routineID, occurrenceKey string, ok bool) {
	action = strings.TrimSpace(action)
	prefixes := []struct {
		prefix  string
		trigger string
	}{
		{"routine.test:", domain.RoutineTriggerTest},
		{"routine.run:", domain.RoutineTriggerSchedule},
	}
	for _, item := range prefixes {
		if !strings.HasPrefix(action, item.prefix) {
			continue
		}
		rest := strings.TrimPrefix(action, item.prefix)
		routineID, occurrenceKey, found := strings.Cut(rest, ":")
		if !found || strings.TrimSpace(routineID) == "" || strings.TrimSpace(occurrenceKey) == "" {
			return "", "", "", false
		}
		return item.trigger, routineID, occurrenceKey, true
	}
	return "", "", "", false
}

func (s *Store) RoutineLedgerHasApproval(ctx context.Context, approvalID string) (bool, error) {
	approvalID = strings.TrimSpace(approvalID)
	if approvalID == "" {
		return false, nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM routine_run_ledger WHERE approval_id = ?)`, approvalID).Scan(&exists); err != nil {
		return false, fmt.Errorf("lookup routine approval: %w", err)
	}
	return exists == 1, nil
}

func validateRoutineApproval(ctx context.Context, source routineSQL, approvalID, routineID, occurrenceKey, trigger string) error {
	var status, action string
	err := source.QueryRowContext(ctx, "SELECT status, action FROM approval_requests WHERE id = ?", approvalID).Scan(&status, &action)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrRoutineApprovalInvalid
	}
	if err != nil {
		return fmt.Errorf("validate routine approval: %w", err)
	}
	expected := RoutineApprovalAction(routineID, occurrenceKey)
	if trigger == domain.RoutineTriggerTest {
		expected = RoutineTestApprovalAction(routineID, occurrenceKey)
	}
	if status != "approved" || action != expected {
		return ErrRoutineApprovalInvalid
	}
	return nil
}

func equalRoutineHash(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}

func routineLeaseValid(run domain.RoutineRun, owner, token string, at time.Time) bool {
	if run.State != domain.RoutineLedgerClaimed && run.State != domain.RoutineLedgerRunning {
		return false
	}
	if run.LeaseOwner != owner || run.LeaseToken != token || run.LeaseExpiresAt == "" {
		return false
	}
	expiry, err := time.Parse(time.RFC3339Nano, run.LeaseExpiresAt)
	return err == nil && expiry.After(at)
}

func routineFailureReason(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "routine failed after final attempt"
	}
	return "routine failed after final attempt: " + strings.TrimSpace(reason)
}
