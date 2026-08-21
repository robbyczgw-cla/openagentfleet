package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestRoutineMigrationIsAutomaticAdditiveAndIdempotent(t *testing.T) {
	ctx := context.Background()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := instance.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
WHERE type = 'table' AND name = 'routine_schedules'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 1 {
		t.Fatalf("routine migration was not wired into Store.Open: tables = %d", before)
	}
	if _, err := instance.db.Exec("CREATE TABLE migration_marker (value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.db.Exec("INSERT INTO migration_marker(value) VALUES ('preserved')"); err != nil {
		t.Fatal(err)
	}
	if err := instance.MigrateRoutines(ctx); err != nil {
		t.Fatalf("MigrateRoutines first call: %v", err)
	}
	if err := instance.MigrateRoutines(ctx); err != nil {
		t.Fatalf("MigrateRoutines second call: %v", err)
	}
	var marker string
	if err := instance.db.QueryRow("SELECT value FROM migration_marker").Scan(&marker); err != nil || marker != "preserved" {
		t.Fatalf("migration marker = %q, %v", marker, err)
	}
	for _, table := range []string{"routine_schedules", "routine_run_ledger", "routine_history"} {
		var exists int
		if err := instance.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
WHERE type = 'table' AND name = ?`, table).Scan(&exists); err != nil || exists != 1 {
			t.Fatalf("table %s exists = %d, %v", table, exists, err)
		}
	}
}

func TestRoutineCreateValidationDisabledDefaultAndFilters(t *testing.T) {
	ctx := context.Background()
	instance, botID := openRoutineTestStore(t)
	base := time.Date(2027, time.October, 25, 1, 30, 0, 0, time.UTC)

	tests := []struct {
		name   string
		mutate func(*domain.RoutineDraft)
	}{
		{name: "bot", mutate: func(value *domain.RoutineDraft) { value.BotID = "" }},
		{name: "name", mutate: func(value *domain.RoutineDraft) { value.Name = "\x00" }},
		{name: "timezone", mutate: func(value *domain.RoutineDraft) { value.TimeZone = "Mars/Olympus" }},
		{name: "cron fields", mutate: func(value *domain.RoutineDraft) { value.CronExpression = "0 9 * *" }},
		{name: "cron minute range", mutate: func(value *domain.RoutineDraft) { value.CronExpression = "60 9 * * *" }},
		{name: "cron zero step", mutate: func(value *domain.RoutineDraft) { value.CronExpression = "*/0 9 * * *" }},
		{name: "cron control", mutate: func(value *domain.RoutineDraft) { value.CronExpression = "0 9 * * MON" }},
		{name: "lead", mutate: func(value *domain.RoutineDraft) { value.LeadHarness = "atlas" }},
		{name: "worker", mutate: func(value *domain.RoutineDraft) { value.Worker = "unknown" }},
		{name: "retry", mutate: func(value *domain.RoutineDraft) { value.Retry.MaxAttempts = 11 }},
		{name: "heartbeat fields on cron", mutate: func(value *domain.RoutineDraft) { value.HeartbeatOptIn = true }},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			draft := routineTestDraft(botID, base)
			testCase.mutate(&draft)
			if _, err := instance.CreateRoutine(ctx, draft); err == nil {
				t.Fatal("invalid routine draft was accepted")
			}
		})
	}
	missingBot := routineTestDraft("bot-missing", base)
	if _, err := instance.CreateRoutine(ctx, missingBot); !errors.Is(err, ErrRoutineBotNotFound) {
		t.Fatalf("CreateRoutine missing bot = %v", err)
	}

	created, err := instance.CreateRoutine(ctx, routineTestDraft(botID, base))
	if err != nil {
		t.Fatalf("CreateRoutine: %v", err)
	}
	if created.Status != domain.RoutineStatusDisabled || created.NextRunAt != routineTimestamp(base) {
		t.Fatalf("created routine = %#v", created)
	}
	due, err := instance.ListDueRoutines(ctx, base.Add(time.Hour), 20)
	if err != nil || len(due) != 0 {
		t.Fatalf("disabled routine became due: %#v, %v", due, err)
	}
	resumed, err := instance.ResumeRoutine(ctx, created.ID, time.Time{})
	if err != nil || resumed.Status != domain.RoutineStatusEnabled {
		t.Fatalf("ResumeRoutine = %#v, %v", resumed, err)
	}
	due, err = instance.ListDueRoutines(ctx, base, 20)
	if err != nil || len(due) != 1 || due[0].ID != created.ID {
		t.Fatalf("enabled due routines = %#v, %v", due, err)
	}
	paused, err := instance.PauseRoutine(ctx, created.ID, "maintenance")
	if err != nil || paused.Status != domain.RoutineStatusPaused || paused.AttentionReason != "maintenance" {
		t.Fatalf("PauseRoutine = %#v, %v", paused, err)
	}
	if _, err := instance.ResumeRoutine(ctx, created.ID, base.Add(2*time.Hour)); err != nil {
		t.Fatalf("resume paused routine: %v", err)
	}
	items, err := instance.ListRoutines(ctx, domain.RoutineListFilter{
		BotID: botID, Kind: domain.RoutineKindCron, Status: domain.RoutineStatusEnabled,
	})
	if err != nil || len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("filtered routines = %#v, %v", items, err)
	}
	loaded, err := instance.GetRoutine(ctx, created.ID)
	if err != nil || loaded.ID != created.ID || loaded.Status != domain.RoutineStatusEnabled {
		t.Fatalf("GetRoutine = %#v, %v", loaded, err)
	}
	if _, err := instance.GetRoutine(ctx, "missing"); !errors.Is(err, ErrRoutineNotFound) {
		t.Fatalf("GetRoutine missing = %v", err)
	}
}

func TestRoutineFixedWidthTimestampsOrderSubsecondsCorrectly(t *testing.T) {
	ctx := context.Background()
	instance, botID := openRoutineTestStore(t)
	whole := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	dueAt := whole.Add(500 * time.Millisecond)
	routine := createAndResumeRoutine(t, instance, routineTestDraft(botID, dueAt), time.Time{})
	if len(routine.NextRunAt) != len(routineTimestamp(whole)) {
		t.Fatalf("timestamp width differs: %q vs %q", routine.NextRunAt, routineTimestamp(whole))
	}
	before, err := instance.ListDueRoutines(ctx, whole.Add(200*time.Millisecond), 10)
	if err != nil || len(before) != 0 {
		t.Fatalf("subsecond routine was due early: %#v, %v", before, err)
	}
	after, err := instance.ListDueRoutines(ctx, whole.Add(600*time.Millisecond), 10)
	if err != nil || len(after) != 1 || after[0].ID != routine.ID {
		t.Fatalf("subsecond routine was not due: %#v, %v", after, err)
	}
}

func TestRoutineHeartbeatRequiresSeparateOptIn(t *testing.T) {
	ctx := context.Background()
	instance, botID := openRoutineTestStore(t)
	base := time.Date(2027, 2, 3, 10, 0, 0, 0, time.UTC)
	draft := routineTestDraft(botID, base)
	draft.Kind = domain.RoutineKindHeartbeat
	draft.CronExpression = ""
	draft.HeartbeatIntervalSeconds = 60
	draft.HeartbeatOptIn = false
	created, err := instance.CreateRoutine(ctx, draft)
	if err != nil {
		t.Fatalf("CreateRoutine heartbeat: %v", err)
	}
	if _, err := instance.ResumeRoutine(ctx, created.ID, time.Time{}); !errors.Is(err, ErrRoutineHeartbeatOptIn) {
		t.Fatalf("heartbeat resumed without opt-in: %v", err)
	}
	optedIn, err := instance.SetRoutineHeartbeatOptIn(ctx, created.ID, true)
	if err != nil || !optedIn.HeartbeatOptIn || optedIn.Status != domain.RoutineStatusDisabled {
		t.Fatalf("SetRoutineHeartbeatOptIn(true) = %#v, %v", optedIn, err)
	}
	resumed, err := instance.ResumeRoutine(ctx, created.ID, time.Time{})
	if err != nil || resumed.Status != domain.RoutineStatusEnabled {
		t.Fatalf("ResumeRoutine heartbeat = %#v, %v", resumed, err)
	}
	disabled, err := instance.SetRoutineHeartbeatOptIn(ctx, created.ID, false)
	if err != nil || disabled.HeartbeatOptIn || disabled.Status != domain.RoutineStatusDisabled || disabled.NextRunAt != "" {
		t.Fatalf("SetRoutineHeartbeatOptIn(false) = %#v, %v", disabled, err)
	}
	tooFast := draft
	tooFast.Name = "Too fast"
	tooFast.HeartbeatIntervalSeconds = domain.RoutineHeartbeatMinSeconds - 1
	if _, err := instance.CreateRoutine(ctx, tooFast); err == nil {
		t.Fatal("unsafe heartbeat interval was accepted")
	}
}

func TestRoutineClaimLeaseLifecycleApprovalAndIdempotency(t *testing.T) {
	ctx := context.Background()
	instance, botID := openRoutineTestStore(t)
	base := time.Date(2027, 3, 4, 11, 0, 0, 0, time.UTC)
	draft := routineTestDraft(botID, base)
	draft.ApprovalPolicy = domain.RoutineApprovalAlways
	routine := createAndResumeRoutine(t, instance, draft, time.Time{})
	claim := routineTestClaim(routine.ID, "scheduler-a", "claim-1", base)
	if _, err := instance.ClaimRoutineRun(ctx, claim); !errors.Is(err, ErrRoutineApprovalRequired) {
		t.Fatalf("claim without approval = %v", err)
	}
	claim.ApprovalID = "fabricated-approval"
	if _, err := instance.ClaimRoutineRun(ctx, claim); !errors.Is(err, ErrRoutineApprovalInvalid) {
		t.Fatalf("claim with fabricated approval = %v", err)
	}
	conversation, err := instance.GetConversation(ctx, "")
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	controllerRun, err := instance.CreateRun(ctx, conversation.ID, botID, "openagentfleet", "approve routine occurrence")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	approval, err := instance.CreateApproval(ctx, controllerRun.ID, "openagentfleet", RoutineApprovalAction(routine.ID, routine.OccurrenceKey), "{}")
	if err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}
	if err := instance.ResolveApproval(ctx, approval.ID, "approved", "approve"); err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}
	claim.ApprovalID = approval.ID
	run, err := instance.ClaimRoutineRun(ctx, claim)
	if err != nil {
		t.Fatalf("ClaimRoutineRun: %v", err)
	}
	if run.State != domain.RoutineLedgerClaimed || run.Attempt != 1 || run.LeaseToken == "" || run.ScheduledFor != routine.NextRunAt {
		t.Fatalf("claimed run = %#v", run)
	}
	dueWhileClaimed, err := instance.ListDueRoutines(ctx, base.Add(time.Second), 10)
	if err != nil || len(dueWhileClaimed) != 0 {
		t.Fatalf("active occurrence remained due: %#v, %v", dueWhileClaimed, err)
	}
	repeated, err := instance.ClaimRoutineRun(ctx, claim)
	if err != nil || repeated.ID != run.ID || repeated.LeaseToken != run.LeaseToken {
		t.Fatalf("idempotent claim = %#v, %v", repeated, err)
	}
	conflict := claim
	conflict.LeaseOwner = "scheduler-b"
	if _, err := instance.ClaimRoutineRun(ctx, conflict); !errors.Is(err, ErrRoutineIdempotencyConflict) {
		t.Fatalf("idempotency conflict = %v", err)
	}
	second := claim
	second.IdempotencyKey = "claim-2"
	if _, err := instance.ClaimRoutineRun(ctx, second); !errors.Is(err, ErrRoutineRunActive) {
		t.Fatalf("second active claim = %v", err)
	}
	if _, err := instance.PauseRoutine(ctx, routine.ID, "pause"); !errors.Is(err, ErrRoutineRunActive) {
		t.Fatalf("pause with active claim = %v", err)
	}
	if _, err := instance.RenewRoutineRunLease(ctx, run.ID, run.LeaseOwner, "wrong", base.Add(time.Second), 30*time.Second); !errors.Is(err, ErrRoutineLeaseLost) {
		t.Fatalf("renew wrong token = %v", err)
	}
	renewed, err := instance.RenewRoutineRunLease(ctx, run.ID, run.LeaseOwner, run.LeaseToken, base.Add(time.Second), 45*time.Second)
	if err != nil || renewed.LeaseExpiresAt != routineTimestamp(base.Add(46*time.Second)) {
		t.Fatalf("RenewRoutineRunLease = %#v, %v", renewed, err)
	}
	running, err := instance.StartRoutineRun(ctx, run.ID, run.LeaseOwner, run.LeaseToken, base.Add(2*time.Second))
	if err != nil || running.State != domain.RoutineLedgerRunning || running.StartedAt == "" {
		t.Fatalf("StartRoutineRun = %#v, %v", running, err)
	}
	restarted, err := instance.StartRoutineRun(ctx, run.ID, run.LeaseOwner, run.LeaseToken, base.Add(2500*time.Millisecond))
	if err != nil || restarted.State != domain.RoutineLedgerRunning || restarted.StartedAt != running.StartedAt {
		t.Fatalf("idempotent StartRoutineRun = %#v, %v", restarted, err)
	}
	finish := domain.RoutineFinish{
		RunID: run.ID, LeaseOwner: run.LeaseOwner, LeaseToken: run.LeaseToken,
		State: domain.RoutineLedgerCompleted, Now: base.Add(3 * time.Second),
	}
	if _, err := instance.FinishRoutineRun(ctx, finish); !errors.Is(err, ErrRoutineNextRunRequired) {
		t.Fatalf("cron finished without next time = %v", err)
	}
	finish.NextRunAt = base.Add(time.Hour)
	completed, err := instance.FinishRoutineRun(ctx, finish)
	if err != nil || completed.State != domain.RoutineLedgerCompleted || completed.LeaseToken != "" {
		t.Fatalf("FinishRoutineRun = %#v, %v", completed, err)
	}
	if _, err := instance.StartRoutineRun(ctx, run.ID, run.LeaseOwner, run.LeaseToken, base.Add(4*time.Second)); !errors.Is(err, ErrRoutineLeaseLost) {
		t.Fatalf("terminal run restarted = %v", err)
	}
	replayedAfterFinish, err := instance.ClaimRoutineRun(ctx, claim)
	if err != nil || replayedAfterFinish.ID != run.ID || replayedAfterFinish.State != domain.RoutineLedgerCompleted {
		t.Fatalf("idempotent claim after finish = %#v, %v", replayedAfterFinish, err)
	}
	updated, err := instance.GetRoutine(ctx, routine.ID)
	if err != nil || updated.NextRunAt != routineTimestamp(finish.NextRunAt) || updated.LastRunAt != routineTimestamp(finish.Now) {
		t.Fatalf("routine after completion = %#v, %v", updated, err)
	}
	runs, err := instance.ListRoutineRuns(ctx, routine.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].State != domain.RoutineLedgerCompleted {
		t.Fatalf("ListRoutineRuns = %#v, %v", runs, err)
	}
	history, err := instance.ListRoutineHistory(ctx, routine.ID, 20)
	if err != nil || len(history) < 4 || history[0].Type != "run.completed" {
		t.Fatalf("ListRoutineHistory = %#v, %v", history, err)
	}
}

func TestRoutineConcurrentClaimHasExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	instance, botID := openRoutineTestStore(t)
	base := time.Date(2027, 4, 5, 12, 0, 0, 0, time.UTC)
	routine := createAndResumeRoutine(t, instance, routineTestDraft(botID, base), time.Time{})
	const contenders = 16
	start := make(chan struct{})
	errorsByContender := make(chan error, contenders)
	var wait sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, err := instance.ClaimRoutineRun(ctx, routineTestClaim(routine.ID, fmt.Sprintf("scheduler-%d", index), fmt.Sprintf("claim-%d", index), base))
			errorsByContender <- err
		}(index)
	}
	close(start)
	wait.Wait()
	close(errorsByContender)
	winners := 0
	activeFailures := 0
	for err := range errorsByContender {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrRoutineRunActive):
			activeFailures++
		default:
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	if winners != 1 || activeFailures != contenders-1 {
		t.Fatalf("claim winners=%d active failures=%d", winners, activeFailures)
	}
}

func TestRoutineFailureRetriesSameOccurrenceThenNeedsAttention(t *testing.T) {
	ctx := context.Background()
	instance, botID := openRoutineTestStore(t)
	base := time.Date(2027, 5, 6, 13, 0, 0, 0, time.UTC)
	draft := routineTestDraft(botID, base)
	draft.Retry = domain.RoutineRetryPolicy{MaxAttempts: 2, BackoffSeconds: 10}
	routine := createAndResumeRoutine(t, instance, draft, time.Time{})
	first := claimRoutineForTest(t, instance, routineTestClaim(routine.ID, "scheduler", "attempt-1", base))
	failed, err := instance.FinishRoutineRun(ctx, domain.RoutineFinish{
		RunID: first.ID, LeaseOwner: first.LeaseOwner, LeaseToken: first.LeaseToken,
		State: domain.RoutineLedgerFailed, Reason: "network unavailable", Now: base.Add(time.Second),
	})
	if err != nil || failed.State != domain.RoutineLedgerFailed {
		t.Fatalf("first failure = %#v, %v", failed, err)
	}
	notYet, err := instance.ListDueRoutines(ctx, base.Add(10*time.Second), 10)
	if err != nil || len(notYet) != 0 {
		t.Fatalf("retry became due before backoff: %#v, %v", notYet, err)
	}
	due, err := instance.ListDueRoutines(ctx, base.Add(11*time.Second), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("retry not due after backoff: %#v, %v", due, err)
	}
	second := claimRoutineForTest(t, instance, routineTestClaim(routine.ID, "scheduler", "attempt-2", base.Add(11*time.Second)))
	if second.Attempt != 2 || second.ScheduledFor != first.ScheduledFor {
		t.Fatalf("second attempt = %#v; first = %#v", second, first)
	}
	if _, err := instance.FinishRoutineRun(ctx, domain.RoutineFinish{
		RunID: second.ID, LeaseOwner: second.LeaseOwner, LeaseToken: second.LeaseToken,
		State: domain.RoutineLedgerFailed, Reason: "still unavailable", Now: base.Add(12 * time.Second),
	}); err != nil {
		t.Fatalf("second failure: %v", err)
	}
	attention, err := instance.GetRoutine(ctx, routine.ID)
	if err != nil || attention.Status != domain.RoutineStatusNeedsAttention || attention.AttentionReason == "" {
		t.Fatalf("routine after final failure = %#v, %v", attention, err)
	}
	if _, err := instance.ResumeRoutine(ctx, routine.ID, base.Add(time.Hour)); !errors.Is(err, ErrRoutineNeedsAttention) {
		t.Fatalf("needs-attention routine resumed directly: %v", err)
	}
	resolved, err := instance.ResolveRoutineAttention(ctx, routine.ID, base.Add(time.Hour))
	if err != nil || resolved.Status != domain.RoutineStatusEnabled || resolved.AttentionReason != "" {
		t.Fatalf("ResolveRoutineAttention = %#v, %v", resolved, err)
	}
}

func TestRoutineNewOccurrenceMayReuseTimestampWithoutInheritingAttempts(t *testing.T) {
	ctx := context.Background()
	instance, botID := openRoutineTestStore(t)
	base := time.Date(2027, 5, 20, 8, 0, 0, 0, time.UTC)
	draft := routineTestDraft(botID, base)
	draft.Retry.MaxAttempts = 2
	routine := createAndResumeRoutine(t, instance, draft, time.Time{})
	first := claimRoutineForTest(t, instance, routineTestClaim(routine.ID, "scheduler", "first-occurrence", base))
	if _, err := instance.FinishRoutineRun(ctx, domain.RoutineFinish{
		RunID: first.ID, LeaseOwner: first.LeaseOwner, LeaseToken: first.LeaseToken,
		State: domain.RoutineLedgerCompleted, NextRunAt: base.Add(time.Hour), Now: base.Add(time.Second),
	}); err != nil {
		t.Fatalf("finish first occurrence: %v", err)
	}
	rescheduled, err := instance.ResumeRoutine(ctx, routine.ID, base)
	if err != nil {
		t.Fatalf("reschedule same timestamp: %v", err)
	}
	if rescheduled.OccurrenceKey == first.OccurrenceKey {
		t.Fatal("new occurrence reused the old occurrence key")
	}
	second := claimRoutineForTest(t, instance, routineTestClaim(routine.ID, "scheduler", "second-occurrence", base.Add(2*time.Second)))
	if second.Attempt != 1 || second.ScheduledFor != first.ScheduledFor || second.OccurrenceKey == first.OccurrenceKey {
		t.Fatalf("reused timestamp occurrence = %#v; first = %#v", second, first)
	}
}

func TestRoutineLeaseExpiryAndDueGatesFailClosed(t *testing.T) {
	ctx := context.Background()
	instance, botID := openRoutineTestStore(t)
	base := time.Date(2027, 5, 21, 8, 0, 0, 0, time.UTC)
	draft := routineTestDraft(botID, base.Add(time.Minute))
	routine := createAndResumeRoutine(t, instance, draft, time.Time{})
	claim := routineTestClaim(routine.ID, "scheduler", "not-due", base)
	if _, err := instance.ClaimRoutineRun(ctx, claim); !errors.Is(err, ErrRoutineNotDue) {
		t.Fatalf("future occurrence claimed = %v", err)
	}
	claim.IdempotencyKey = "due"
	claim.Now = base.Add(time.Minute)
	run := claimRoutineForTest(t, instance, claim)
	if _, err := instance.RenewRoutineRunLease(ctx, run.ID, run.LeaseOwner, run.LeaseToken, claim.Now.Add(5*time.Second), 30*time.Second); !errors.Is(err, ErrRoutineLeaseLost) {
		t.Fatalf("lease renewed at exact expiry = %v", err)
	}
	if _, err := instance.StartRoutineRun(ctx, run.ID, run.LeaseOwner, run.LeaseToken, claim.Now.Add(5*time.Second)); !errors.Is(err, ErrRoutineLeaseLost) {
		t.Fatalf("run started at exact lease expiry = %v", err)
	}
}

func TestRoutineStaleRecoveryIsUnknownAndInvalidatesOldLease(t *testing.T) {
	ctx := context.Background()
	instance, botID := openRoutineTestStore(t)
	base := time.Date(2027, 6, 7, 14, 0, 0, 0, time.UTC)
	draft := routineTestDraft(botID, base)
	draft.Retry = domain.RoutineRetryPolicy{MaxAttempts: 2, BackoffSeconds: 0}
	routine := createAndResumeRoutine(t, instance, draft, time.Time{})
	first := claimRoutineForTest(t, instance, routineTestClaim(routine.ID, "scheduler-a", "stale-1", base))
	if _, err := instance.StartRoutineRun(ctx, first.ID, first.LeaseOwner, first.LeaseToken, base.Add(time.Second)); err != nil {
		t.Fatalf("StartRoutineRun: %v", err)
	}
	recovered, err := instance.RecoverStaleRoutineRuns(ctx, base.Add(6*time.Second), 10)
	if err != nil || len(recovered) != 1 || recovered[0].State != domain.RoutineLedgerUnknown {
		t.Fatalf("RecoverStaleRoutineRuns = %#v, %v", recovered, err)
	}
	if _, err := instance.FinishRoutineRun(ctx, domain.RoutineFinish{
		RunID: first.ID, LeaseOwner: first.LeaseOwner, LeaseToken: first.LeaseToken,
		State: domain.RoutineLedgerCompleted, NextRunAt: base.Add(time.Hour), Now: base.Add(7 * time.Second),
	}); !errors.Is(err, ErrRoutineLeaseLost) {
		t.Fatalf("stale lease completed recovered run: %v", err)
	}
	again, err := instance.RecoverStaleRoutineRuns(ctx, base.Add(7*time.Second), 10)
	if err != nil || len(again) != 0 {
		t.Fatalf("stale recovery was not idempotent: %#v, %v", again, err)
	}
	if _, err := instance.ClaimRoutineRun(ctx, routineTestClaim(routine.ID, "scheduler-b", "stale-2", base.Add(7*time.Second))); !errors.Is(err, ErrRoutineNeedsAttention) {
		t.Fatalf("claim after stale recovery = %v, want explicit attention", err)
	}
	if _, err := instance.RecoverStaleRoutineRuns(ctx, base.Add(13*time.Second), 10); err != nil {
		t.Fatalf("second stale recovery: %v", err)
	}
	attention, err := instance.GetRoutine(ctx, routine.ID)
	if err != nil || attention.Status != domain.RoutineStatusNeedsAttention {
		t.Fatalf("routine did not stop after stale retry exhaustion: %#v, %v", attention, err)
	}
}

func TestRoutineTestClaimOnDisabledLeavesScheduleUnchanged(t *testing.T) {
	ctx := context.Background()
	instance, botID := openRoutineTestStore(t)
	base := time.Date(2027, 8, 9, 16, 0, 0, 0, time.UTC)
	created, err := instance.CreateRoutine(ctx, routineTestDraft(botID, base))
	if err != nil {
		t.Fatalf("CreateRoutine: %v", err)
	}
	if created.Status != domain.RoutineStatusDisabled {
		t.Fatalf("created = %#v", created)
	}
	if _, err := instance.ClaimRoutineRun(ctx, routineTestClaim(created.ID, "scheduler", "scheduled", base)); !errors.Is(err, ErrRoutineDisabled) {
		t.Fatalf("scheduled claim on disabled = %v", err)
	}
	run, err := instance.ClaimTestRoutineRun(ctx, routineTestClaim(created.ID, "tester", "test-1", base))
	if err != nil || run.Trigger != domain.RoutineTriggerTest || run.OccurrenceKey == created.OccurrenceKey {
		t.Fatalf("ClaimTestRoutineRun = %#v, %v", run, err)
	}
	if _, err := instance.StartRoutineRun(ctx, run.ID, run.LeaseOwner, run.LeaseToken, base.Add(time.Second)); err != nil {
		t.Fatalf("StartRoutineRun: %v", err)
	}
	if _, err := instance.FinishRoutineRun(ctx, domain.RoutineFinish{
		RunID: run.ID, LeaseOwner: run.LeaseOwner, LeaseToken: run.LeaseToken,
		State: domain.RoutineLedgerCompleted, NextRunAt: base.Add(time.Hour), Now: base.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("FinishRoutineRun test: %v", err)
	}
	updated, err := instance.GetRoutine(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.RoutineStatusDisabled || updated.NextRunAt != created.NextRunAt || updated.OccurrenceKey != created.OccurrenceKey || updated.LastRunAt != "" {
		t.Fatalf("test finish rotated the schedule: before=%#v after=%#v", created, updated)
	}

	failed := createAndResumeRoutine(t, instance, routineTestDraft(botID, base.Add(2*time.Hour)), time.Time{})
	first := claimRoutineForTest(t, instance, routineTestClaim(failed.ID, "scheduler", "fail-1", base.Add(2*time.Hour)))
	if _, err := instance.FinishRoutineRun(ctx, domain.RoutineFinish{
		RunID: first.ID, LeaseOwner: first.LeaseOwner, LeaseToken: first.LeaseToken,
		State: domain.RoutineLedgerFailed, Reason: "boom", Now: base.Add(2*time.Hour + time.Second),
	}); err != nil {
		t.Fatalf("scheduled failure: %v", err)
	}
	attention, err := instance.GetRoutine(ctx, failed.ID)
	if err != nil || attention.Status != domain.RoutineStatusNeedsAttention {
		t.Fatalf("needs attention = %#v, %v", attention, err)
	}
	if _, err := instance.ClaimTestRoutineRun(ctx, routineTestClaim(failed.ID, "tester", "test-attention", base.Add(3*time.Hour))); !errors.Is(err, ErrRoutineNeedsAttention) {
		t.Fatalf("test claim on needs_attention = %v", err)
	}
}

func TestRoutineHeartbeatCompletionComputesNextRunWithoutScheduler(t *testing.T) {
	ctx := context.Background()
	instance, botID := openRoutineTestStore(t)
	base := time.Date(2027, 7, 8, 15, 0, 0, 0, time.UTC)
	draft := routineTestDraft(botID, base)
	draft.Kind = domain.RoutineKindHeartbeat
	draft.CronExpression = ""
	draft.HeartbeatOptIn = true
	draft.HeartbeatIntervalSeconds = 90
	routine := createAndResumeRoutine(t, instance, draft, time.Time{})
	run := claimRoutineForTest(t, instance, routineTestClaim(routine.ID, "heartbeat", "heartbeat-1", base))
	completed, err := instance.FinishRoutineRun(ctx, domain.RoutineFinish{
		RunID: run.ID, LeaseOwner: run.LeaseOwner, LeaseToken: run.LeaseToken,
		State: domain.RoutineLedgerCompleted, Now: base.Add(time.Second),
	})
	if err != nil || completed.State != domain.RoutineLedgerCompleted {
		t.Fatalf("heartbeat finish = %#v, %v", completed, err)
	}
	updated, err := instance.GetRoutine(ctx, routine.ID)
	want := routineTimestamp(base.Add(91 * time.Second))
	if err != nil || updated.NextRunAt != want {
		t.Fatalf("heartbeat next run = %q, want %q, err=%v", updated.NextRunAt, want, err)
	}
}

func TestRoutineHeartbeatOptOutCannotHideActiveOrNeedsAttentionState(t *testing.T) {
	ctx := context.Background()
	instance, botID := openRoutineTestStore(t)
	base := time.Date(2027, 7, 9, 15, 0, 0, 0, time.UTC)
	draft := routineTestDraft(botID, base)
	draft.Kind = domain.RoutineKindHeartbeat
	draft.CronExpression = ""
	draft.HeartbeatOptIn = true
	draft.HeartbeatIntervalSeconds = 60
	routine := createAndResumeRoutine(t, instance, draft, time.Time{})
	run := claimRoutineForTest(t, instance, routineTestClaim(routine.ID, "heartbeat", "heartbeat-stale", base))
	if _, err := instance.SetRoutineHeartbeatOptIn(ctx, routine.ID, false); !errors.Is(err, ErrRoutineRunActive) {
		t.Fatalf("active heartbeat opted out = %v", err)
	}
	if _, err := instance.RecoverStaleRoutineRuns(ctx, base.Add(6*time.Second), 10); err != nil {
		t.Fatalf("RecoverStaleRoutineRuns: %v", err)
	}
	optedOut, err := instance.SetRoutineHeartbeatOptIn(ctx, routine.ID, false)
	if err != nil || optedOut.Status != domain.RoutineStatusNeedsAttention || optedOut.AttentionReason == "" || optedOut.HeartbeatOptIn {
		t.Fatalf("needs-attention heartbeat opt-out = %#v, %v (run=%s)", optedOut, err, run.ID)
	}
	if _, err := instance.ResolveRoutineAttention(ctx, routine.ID, base.Add(time.Hour)); !errors.Is(err, ErrRoutineHeartbeatOptIn) {
		t.Fatalf("opted-out heartbeat attention resolved into execution = %v", err)
	}
}

func openRoutineTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	ctx := context.Background()
	if err := instance.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	if err := instance.MigrateRoutines(ctx); err != nil {
		t.Fatal(err)
	}
	bots, err := instance.ListBots(ctx)
	if err != nil || len(bots) != 1 {
		t.Fatalf("ListBots = %#v, %v", bots, err)
	}
	return instance, bots[0].ID
}

func routineTestDraft(botID string, nextRunAt time.Time) domain.RoutineDraft {
	return domain.RoutineDraft{
		BotID:          botID,
		Name:           "Check failed CI",
		Description:    "Inspect a bounded project status without starting a harness here.",
		Kind:           domain.RoutineKindCron,
		CronExpression: "0 9 * * *",
		TimeZone:       "Europe/Vienna",
		LeadHarness:    domain.RoutineLeadGrokBuild,
		Worker:         domain.RoutineWorkerClaude,
		ApprovalPolicy: domain.RoutineApprovalOnRisk,
		Retry:          domain.RoutineRetryPolicy{MaxAttempts: 1, BackoffSeconds: 0},
		NextRunAt:      nextRunAt.Format(time.RFC3339Nano),
	}
}

func createAndResumeRoutine(t *testing.T, instance *Store, draft domain.RoutineDraft, nextRunAt time.Time) domain.Routine {
	t.Helper()
	created, err := instance.CreateRoutine(context.Background(), draft)
	if err != nil {
		t.Fatalf("CreateRoutine: %v", err)
	}
	resumed, err := instance.ResumeRoutine(context.Background(), created.ID, nextRunAt)
	if err != nil {
		t.Fatalf("ResumeRoutine: %v", err)
	}
	return resumed
}

func routineTestClaim(routineID, owner, key string, at time.Time) domain.RoutineClaim {
	return domain.RoutineClaim{
		RoutineID: routineID, LeaseOwner: owner, LeaseDuration: 5 * time.Second,
		IdempotencyKey: key, Now: at,
	}
}

func claimRoutineForTest(t *testing.T, instance *Store, claim domain.RoutineClaim) domain.RoutineRun {
	t.Helper()
	run, err := instance.ClaimRoutineRun(context.Background(), claim)
	if err != nil {
		t.Fatalf("ClaimRoutineRun: %v", err)
	}
	return run
}
