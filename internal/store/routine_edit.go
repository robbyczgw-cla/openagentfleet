package store

import (
	"context"
	"errors"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

// ErrRoutineEditRequiresPause guards the workspace editor. A routine that is
// enabled can be claimed by the scheduler between the read and the write, so
// the caller must pause or disable it before changing what it will run.
var ErrRoutineEditRequiresPause = errors.New("routine must be paused or disabled before editing")

// EditRoutineSchedule rewrites the name, instructions and cadence of an
// existing routine. It deliberately cannot change the owning agent, the kind,
// the harness pair, the approval policy or any opt-in bit: those stay with the
// create path so an edit can never widen what a routine is allowed to do.
//
// Cron routines take the supplied expression and zone. Heartbeat routines keep
// their interval and opt-in state and accept only a zone change, because their
// cadence lives in heartbeat_interval_seconds rather than an expression.
func (s *Store) EditRoutineSchedule(ctx context.Context, routineID, name, description, cron, zone string) (domain.Routine, error) {
	conn, rollback, err := s.beginRoutineImmediate(ctx)
	if err != nil {
		return domain.Routine{}, err
	}
	defer rollback()

	item, err := loadRoutineFrom(ctx, conn, routineID)
	if err != nil {
		return domain.Routine{}, err
	}
	if item.Status != domain.RoutineStatusDisabled && item.Status != domain.RoutineStatusPaused {
		return domain.Routine{}, ErrRoutineEditRequiresPause
	}
	// A paused routine can still own a claimed or running occurrence that was
	// started before the pause. Editing under it would change the definition of
	// work already in flight.
	active, err := routineHasActiveRun(ctx, conn, item.ID)
	if err != nil {
		return domain.Routine{}, err
	}
	if active {
		return domain.Routine{}, ErrRoutineRunActive
	}

	draft := domain.RoutineDraft{
		BotID:                    item.BotID,
		Name:                     name,
		Description:              description,
		Kind:                     item.Kind,
		CronExpression:           cron,
		TimeZone:                 zone,
		HeartbeatOptIn:           item.HeartbeatOptIn,
		HeartbeatIntervalSeconds: item.HeartbeatIntervalSeconds,
		LeadHarness:              item.LeadHarness,
		Worker:                   item.Worker,
		ApprovalPolicy:           item.ApprovalPolicy,
		Retry:                    item.Retry,
	}
	if item.Kind == domain.RoutineKindHeartbeat {
		// Validate rejects a heartbeat draft that carries an expression, so an
		// accidental cron field would surface as a confusing validation error.
		draft.CronExpression = ""
	}
	draft = normalizeRoutineDraft(draft)
	if err := draft.Validate(); err != nil {
		return domain.Routine{}, err
	}

	item.Name = draft.Name
	item.Description = draft.Description
	item.CronExpression = draft.CronExpression
	item.TimeZone = draft.TimeZone
	// The stored due time was derived from the previous cadence. Clearing it
	// makes the next explicit enable recompute from the edited schedule, and
	// drops the occurrence key so no stale claim can match.
	item.NextRunAt = ""
	item.OccurrenceKey = ""
	item.RetryNotBefore = ""
	item.UpdatedAt = routineTimestamp(time.Now())

	if _, err := conn.ExecContext(ctx, `UPDATE routine_schedules
SET name = ?, description = ?, cron_expression = ?, time_zone = ?, next_run_at = '', occurrence_key = '', retry_not_before = '', updated_at = ?
WHERE id = ?`,
		item.Name, item.Description, item.CronExpression, item.TimeZone, item.UpdatedAt, item.ID); err != nil {
		return domain.Routine{}, err
	}
	if err := appendRoutineEvent(ctx, conn, item.ID, "", "routine.updated", "name, instructions and schedule updated", item.UpdatedAt); err != nil {
		return domain.Routine{}, err
	}
	if err := commitRoutineImmediate(ctx, conn, "edit routine"); err != nil {
		return domain.Routine{}, err
	}
	return item, nil
}
