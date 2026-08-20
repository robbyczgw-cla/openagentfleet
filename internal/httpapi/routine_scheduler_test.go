package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

func TestRoutineSchedulerDoesNotClaimWhenFeatureOff(t *testing.T) {
	server, instance, botID := openRoutineScheduler(t)
	now := time.Date(2027, time.March, 4, 11, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }
	var runs atomic.Int32
	server.routineRunner = func(context.Context, domain.Routine, domain.RoutineRun) error {
		runs.Add(1)
		return nil
	}
	createEnabledRoutine(t, instance, botID, now, domain.RoutineApprovalOnRisk)
	if err := server.TickRoutines(t.Context()); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 0 {
		t.Fatalf("claimed while routines feature is off: %d", runs.Load())
	}
}

func TestRoutineSchedulerClaimsOnceAndAdvancesCron(t *testing.T) {
	server, instance, botID := openRoutineScheduler(t)
	now := time.Date(2027, time.March, 4, 11, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }
	enableRoutineFeatures(t, instance, false)

	var runs atomic.Int32
	server.routineRunner = func(context.Context, domain.Routine, domain.RoutineRun) error {
		runs.Add(1)
		return nil
	}

	routine := createEnabledRoutine(t, instance, botID, now, domain.RoutineApprovalOnRisk)
	if err := server.TickRoutines(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := server.TickRoutines(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("executor calls = %d, want 1", got)
	}
	ledger, err := instance.ListRoutineRuns(t.Context(), routine.ID, 10)
	if err != nil || len(ledger) != 1 || ledger[0].State != domain.RoutineLedgerCompleted {
		t.Fatalf("ledger = %#v, %v", ledger, err)
	}
	updated, err := instance.GetRoutine(t.Context(), routine.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.RoutineStatusEnabled || updated.NextRunAt <= now.Format(time.RFC3339Nano) {
		t.Fatalf("next run was not advanced: %#v", updated)
	}
}

func TestRoutineSchedulerSkipsHeartbeatWithoutFeatureAndOptIn(t *testing.T) {
	server, instance, botID := openRoutineScheduler(t)
	now := time.Date(2027, time.March, 4, 11, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }
	enableRoutineFeatures(t, instance, false)

	var runs atomic.Int32
	server.routineRunner = func(context.Context, domain.Routine, domain.RoutineRun) error {
		runs.Add(1)
		return nil
	}

	draft := domain.RoutineDraft{
		BotID:                    botID,
		Name:                     "Inbox pulse",
		Kind:                     domain.RoutineKindHeartbeat,
		TimeZone:                 "UTC",
		HeartbeatIntervalSeconds: 60,
		LeadHarness:              domain.RoutineLeadGrokBuild,
		Worker:                   domain.RoutineWorkerGrok,
		ApprovalPolicy:           domain.RoutineApprovalNever,
		Retry:                    domain.RoutineRetryPolicy{MaxAttempts: 1, BackoffSeconds: 0},
		NextRunAt:                now.Format(time.RFC3339Nano),
	}
	created, err := instance.CreateRoutine(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.ResumeRoutine(t.Context(), created.ID, time.Time{}); err == nil {
		t.Fatal("heartbeat resumed without opt-in")
	}
	if _, err := instance.SetRoutineHeartbeatOptIn(t.Context(), created.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.ResumeRoutine(t.Context(), created.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := server.TickRoutines(t.Context()); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 0 {
		t.Fatalf("heartbeat ran with feature off: %d", runs.Load())
	}

	enableRoutineFeatures(t, instance, true)
	if err := server.TickRoutines(t.Context()); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 1 {
		t.Fatalf("heartbeat executor calls = %d, want 1", runs.Load())
	}
}

func TestRoutineSchedulerAlwaysApprovalGatesClaim(t *testing.T) {
	server, instance, botID := openRoutineScheduler(t)
	now := time.Date(2027, time.March, 4, 11, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }
	enableRoutineFeatures(t, instance, false)

	var runs atomic.Int32
	server.routineRunner = func(context.Context, domain.Routine, domain.RoutineRun) error {
		runs.Add(1)
		return nil
	}

	routine := createEnabledRoutine(t, instance, botID, now, domain.RoutineApprovalAlways)
	if err := server.TickRoutines(t.Context()); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 0 {
		t.Fatal("always-approval routine ran before approval")
	}
	ledger, err := instance.ListRoutineRuns(t.Context(), routine.ID, 10)
	if err != nil || len(ledger) != 0 {
		t.Fatalf("claimed without approval: %#v, %v", ledger, err)
	}
	approvals, err := instance.ListApprovals(t.Context(), "pending")
	if err != nil || len(approvals) != 1 {
		t.Fatalf("pending approvals = %#v, %v", approvals, err)
	}

	if err := instance.ResolveApproval(t.Context(), approvals[0].ID, "approved", "allow_once"); err != nil {
		t.Fatal(err)
	}
	if err := server.TickRoutines(t.Context()); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 1 {
		t.Fatalf("approved routine executor calls = %d, want 1", runs.Load())
	}
}

func TestRoutineSchedulerDoesNotReuseConsumedApproval(t *testing.T) {
	server, instance, botID := openRoutineScheduler(t)
	now := time.Date(2027, time.March, 4, 11, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }
	enableRoutineFeatures(t, instance, false)
	var runs atomic.Int32
	server.routineRunner = func(context.Context, domain.Routine, domain.RoutineRun) error {
		runs.Add(1)
		return errors.New("boom")
	}
	created, err := instance.CreateRoutine(t.Context(), domain.RoutineDraft{
		BotID:          botID,
		Name:           "Retry gated",
		Kind:           domain.RoutineKindCron,
		CronExpression: "0 9 * * *",
		TimeZone:       "UTC",
		LeadHarness:    domain.RoutineLeadGrokBuild,
		Worker:         domain.RoutineWorkerGrok,
		ApprovalPolicy: domain.RoutineApprovalAlways,
		Retry:          domain.RoutineRetryPolicy{MaxAttempts: 2, BackoffSeconds: 0},
		NextRunAt:      now.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.ResumeRoutine(t.Context(), created.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := server.TickRoutines(t.Context()); err != nil {
		t.Fatal(err)
	}
	approvals, err := instance.ListApprovals(t.Context(), "pending")
	if err != nil || len(approvals) != 1 {
		t.Fatalf("pending = %#v, %v", approvals, err)
	}
	if err := instance.ResolveApproval(t.Context(), approvals[0].ID, "approved", "allow_once"); err != nil {
		t.Fatal(err)
	}
	if err := server.TickRoutines(t.Context()); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 1 {
		t.Fatalf("first attempt = %d", runs.Load())
	}
	if err := server.TickRoutines(t.Context()); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 1 {
		t.Fatalf("consumed approval was reused: %d", runs.Load())
	}
	pending, err := instance.ListApprovals(t.Context(), "pending")
	if err != nil || len(pending) != 1 {
		t.Fatalf("retry pending = %#v, %v", pending, err)
	}
}

func TestRoutineSchedulerRenewsLeaseDuringLongRun(t *testing.T) {
	server, instance, botID := openRoutineScheduler(t)
	enableRoutineFeatures(t, instance, false)
	server.routineLease = 5 * time.Second
	server.routineLeaseRenewEvery = 400 * time.Millisecond
	started := make(chan struct{})
	block := make(chan struct{})
	server.routineRunner = func(context.Context, domain.Routine, domain.RoutineRun) error {
		close(started)
		<-block
		return nil
	}
	createEnabledRoutine(t, instance, botID, time.Now().UTC(), domain.RoutineApprovalNever)
	done := make(chan error, 1)
	go func() { done <- server.TickRoutines(context.Background()) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not start")
	}
	time.Sleep(6 * time.Second)
	stale, err := instance.RecoverStaleRoutineRuns(context.Background(), time.Now().UTC(), 10)
	if err != nil || len(stale) != 0 {
		t.Fatalf("lease expired while runner held it: %#v, %v", stale, err)
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRoutineSchedulerDeniedOccurrenceAdvances(t *testing.T) {
	server, instance, botID := openRoutineScheduler(t)
	now := time.Date(2027, time.March, 4, 11, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }
	enableRoutineFeatures(t, instance, false)
	var runs atomic.Int32
	server.routineRunner = func(context.Context, domain.Routine, domain.RoutineRun) error {
		runs.Add(1)
		return nil
	}
	routine := createEnabledRoutine(t, instance, botID, now, domain.RoutineApprovalAlways)
	if err := server.TickRoutines(t.Context()); err != nil {
		t.Fatal(err)
	}
	approvals, err := instance.ListApprovals(t.Context(), "pending")
	if err != nil || len(approvals) != 1 {
		t.Fatalf("pending approvals = %#v, %v", approvals, err)
	}
	denied := performRequest(server.Handler(), http.MethodPost, "/api/approvals/"+approvals[0].ID, `{"status":"denied","option_id":"reject"}`, "")
	if denied.Code != http.StatusOK {
		t.Fatalf("deny = %d %s", denied.Code, denied.Body.String())
	}
	updated, err := instance.GetRoutine(t.Context(), routine.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.RoutineStatusEnabled {
		t.Fatalf("denied routine status = %s", updated.Status)
	}
	if updated.OccurrenceKey == "" || updated.OccurrenceKey == routine.OccurrenceKey {
		t.Fatalf("denied occurrence was not skipped: %#v", updated)
	}
	if updated.NextRunAt <= now.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("denied next run stayed due: %#v", updated)
	}
	if err := server.TickRoutines(t.Context()); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 0 {
		t.Fatalf("denied occurrence still ran: %d", runs.Load())
	}
}

func TestEnableAndResolveIgnorePastNextRun(t *testing.T) {
	server, instance, botID := openRoutineScheduler(t)
	now := time.Date(2027, time.March, 4, 11, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }
	enableRoutineFeatures(t, instance, false)
	handler := server.Handler()
	created := performRequest(handler, http.MethodPost, "/api/routines", `{
		"bot_id":"`+botID+`",
		"name":"Past due",
		"kind":"cron",
		"cron_expression":"0 9 * * *",
		"time_zone":"UTC",
		"lead_harness":"grok_build",
		"worker":"grok",
		"retry":{"max_attempts":1,"backoff_seconds":0}
	}`, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	var payload struct {
		Routine domain.Routine `json:"routine"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	past := time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	enabled := performRequest(handler, http.MethodPost, "/api/routines/"+payload.Routine.ID+"/enable", `{"next_run_at":"`+past+`"}`, "")
	if enabled.Code != http.StatusOK {
		t.Fatalf("enable past = %d %s", enabled.Code, enabled.Body.String())
	}
	if err := json.Unmarshal(enabled.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Routine.NextRunAt <= now.Format(time.RFC3339Nano) {
		t.Fatalf("enable kept a past next run: %#v", payload.Routine)
	}
	_ = instance
}

func TestRoutinePauseEnableHistoryAPI(t *testing.T) {
	server, instance, botID := openRoutineScheduler(t)
	enableRoutineFeatures(t, instance, false)
	nextRun := time.Date(2027, time.October, 25, 9, 0, 0, 0, time.UTC)
	handler := server.Handler()

	created := performRequest(handler, http.MethodPost, "/api/routines", `{
		"bot_id":"`+botID+`",
		"name":"Weekly notes",
		"kind":"cron",
		"cron_expression":"0 9 * * 1",
		"time_zone":"Europe/Vienna",
		"lead_harness":"grok_build",
		"worker":"claude",
		"retry":{"max_attempts":1,"backoff_seconds":0}
	}`, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	var payload struct {
		Routine domain.Routine `json:"routine"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Routine.Status != domain.RoutineStatusDisabled || payload.Routine.NextRunAt != "" {
		t.Fatalf("created = %#v", payload.Routine)
	}

	enabled := performRequest(handler, http.MethodPost, "/api/routines/"+payload.Routine.ID+"/enable", "", "")
	if enabled.Code != http.StatusOK || !strings.Contains(enabled.Body.String(), `"status":"enabled"`) {
		t.Fatalf("enable without next_run = %d %s", enabled.Code, enabled.Body.String())
	}
	if err := json.Unmarshal(enabled.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Routine.NextRunAt == "" {
		t.Fatal("enable did not compute next run")
	}

	paused := performRequest(handler, http.MethodPost, "/api/routines/"+payload.Routine.ID+"/pause", `{"reason":"maintenance"}`, "")
	if paused.Code != http.StatusOK || !strings.Contains(paused.Body.String(), `"status":"paused"`) {
		t.Fatalf("pause = %d %s", paused.Code, paused.Body.String())
	}

	resumed := performRequest(handler, http.MethodPost, "/api/routines/"+payload.Routine.ID+"/enable", `{"next_run_at":"`+nextRun.Format(time.RFC3339Nano)+`"}`, "")
	if resumed.Code != http.StatusOK || !strings.Contains(resumed.Body.String(), `"status":"enabled"`) {
		t.Fatalf("resume = %d %s", resumed.Code, resumed.Body.String())
	}

	detail := performRequest(handler, http.MethodGet, "/api/routines/"+payload.Routine.ID, "", "")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"history"`) {
		t.Fatalf("get routine = %d %s", detail.Code, detail.Body.String())
	}
	_ = instance
}

func openRoutineScheduler(t *testing.T) (*Server, *store.Store, string) {
	t.Helper()
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: instance, routineLeaseOwner: "botd-test"}
	return server, instance, conversation.BotID
}

func enableRoutineFeatures(t *testing.T, instance *store.Store, heartbeat bool) {
	t.Helper()
	prefs, err := instance.GetPreferences(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	prefs.Features.Routines = true
	prefs.Features.Heartbeat = heartbeat
	if _, err := instance.SavePreferences(t.Context(), prefs); err != nil {
		t.Fatal(err)
	}
}

func createEnabledRoutine(t *testing.T, instance *store.Store, botID string, nextRunAt time.Time, policy domain.RoutineApprovalPolicy) domain.Routine {
	t.Helper()
	created, err := instance.CreateRoutine(t.Context(), domain.RoutineDraft{
		BotID:          botID,
		Name:           "Check failed CI",
		Description:    "Inspect a bounded project status.",
		Kind:           domain.RoutineKindCron,
		CronExpression: "0 9 * * *",
		TimeZone:       "UTC",
		LeadHarness:    domain.RoutineLeadGrokBuild,
		Worker:         domain.RoutineWorkerGrok,
		ApprovalPolicy: policy,
		Retry:          domain.RoutineRetryPolicy{MaxAttempts: 1, BackoffSeconds: 0},
		NextRunAt:      nextRunAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := instance.ResumeRoutine(t.Context(), created.ID, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return resumed
}
