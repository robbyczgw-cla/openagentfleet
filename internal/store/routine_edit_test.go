package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestEditRoutineScheduleKeepsOwnershipAndOptIns(t *testing.T) {
	ctx := context.Background()
	instance, botID := openRoutineTestStore(t)
	base := time.Date(2027, time.October, 25, 1, 30, 0, 0, time.UTC)

	draft := routineTestDraft(botID, base.Add(time.Hour))
	draft.ApprovalPolicy = domain.RoutineApprovalAlways
	draft.Retry = domain.RoutineRetryPolicy{MaxAttempts: 3, BackoffSeconds: 60}
	created, err := instance.CreateRoutine(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}

	edited, err := instance.EditRoutineSchedule(ctx, created.ID,
		"  Weekly release notes  ", "  Summarize merged pull requests.  ", " 0   7 * * 1 ", "America/New_York")
	if err != nil {
		t.Fatalf("EditRoutineSchedule: %v", err)
	}
	if edited.Name != "Weekly release notes" || edited.Description != "Summarize merged pull requests." {
		t.Fatalf("name and description were not trimmed: %#v", edited)
	}
	if edited.CronExpression != "0 7 * * 1" {
		t.Fatalf("cron expression was not normalized: %q", edited.CronExpression)
	}
	// An edit changes what a routine does, never who runs it or how much it is
	// allowed to do without asking.
	if edited.BotID != created.BotID || edited.LeadHarness != created.LeadHarness || edited.Worker != created.Worker {
		t.Fatalf("edit reassigned the routine: %#v", edited)
	}
	if edited.ApprovalPolicy != domain.RoutineApprovalAlways || edited.Retry != created.Retry {
		t.Fatalf("edit widened the policy: %#v", edited)
	}
	if edited.Status != domain.RoutineStatusDisabled {
		t.Fatalf("edit changed lifecycle to %q", edited.Status)
	}
	if edited.NextRunAt != "" || edited.OccurrenceKey != "" || edited.RetryNotBefore != "" {
		t.Fatalf("due time from the previous cadence survived: %#v", edited)
	}

	reloaded, err := instance.GetRoutine(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.CronExpression != edited.CronExpression || reloaded.TimeZone != "America/New_York" || reloaded.UpdatedAt != edited.UpdatedAt {
		t.Fatalf("returned routine does not match the committed row: %#v vs %#v", edited, reloaded)
	}

	history, err := instance.ListRoutineHistory(ctx, created.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range history {
		if event.Type == "routine.updated" {
			found = true
		}
	}
	if !found {
		t.Fatalf("edit was not recorded in history: %#v", history)
	}
}

func TestEditRoutineScheduleRejectsUnsafeEdits(t *testing.T) {
	ctx := context.Background()
	instance, botID := openRoutineTestStore(t)
	base := time.Date(2027, time.October, 25, 1, 30, 0, 0, time.UTC)

	enabled := createAndResumeRoutine(t, instance, routineTestDraft(botID, base.Add(time.Hour)), base.Add(time.Hour))
	if _, err := instance.EditRoutineSchedule(ctx, enabled.ID, "New name", "", "0 9 * * *", "UTC"); !errors.Is(err, ErrRoutineEditRequiresPause) {
		t.Fatalf("edit while enabled = %v", err)
	}
	if _, err := instance.EditRoutineSchedule(ctx, "routine-missing", "New name", "", "0 9 * * *", "UTC"); !errors.Is(err, ErrRoutineNotFound) {
		t.Fatalf("edit unknown routine = %v", err)
	}

	paused, err := instance.PauseRoutine(ctx, enabled.ID, "editing")
	if err != nil {
		t.Fatal(err)
	}
	invalid := []struct {
		name string
		cron string
		zone string
		text string
	}{
		{name: "short cron", cron: "0 9 * *", zone: "UTC", text: "cron expression"},
		{name: "out of range field", cron: "0 24 * * *", zone: "UTC", text: "cron expression"},
		{name: "unknown zone", cron: "0 9 * * *", zone: "Mars/Olympus", text: "time zone"},
		{name: "blank zone", cron: "0 9 * * *", zone: "  ", text: "time zone"},
	}
	for _, testCase := range invalid {
		if _, err := instance.EditRoutineSchedule(ctx, paused.ID, "New name", "", testCase.cron, testCase.zone); err == nil || !strings.Contains(err.Error(), testCase.text) {
			t.Fatalf("%s = %v", testCase.name, err)
		}
	}
	if _, err := instance.EditRoutineSchedule(ctx, paused.ID, "   ", "", "0 9 * * *", "UTC"); err == nil {
		t.Fatal("blank name was accepted")
	}

	// Every rejection above must have rolled back cleanly.
	unchanged, err := instance.GetRoutine(ctx, paused.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Name != paused.Name || unchanged.CronExpression != paused.CronExpression || unchanged.TimeZone != paused.TimeZone {
		t.Fatalf("rejected edit mutated the routine: %#v", unchanged)
	}
}

func TestEditRoutineScheduleBlocksInFlightOccurrence(t *testing.T) {
	ctx := context.Background()
	instance, botID := openRoutineTestStore(t)
	base := time.Date(2027, time.October, 25, 1, 30, 0, 0, time.UTC)

	routine := createAndResumeRoutine(t, instance, routineTestDraft(botID, base.Add(time.Hour)), base.Add(time.Hour))
	paused, err := instance.PauseRoutine(ctx, routine.ID, "editing")
	if err != nil {
		t.Fatal(err)
	}
	// Test runs are allowed on a paused routine, which is the one way a paused
	// routine can still hold a lease while the workspace offers an edit form.
	claimed, err := instance.ClaimTestRoutineRun(ctx, routineTestClaim(paused.ID, "scheduler-a", "claim-1", base))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.EditRoutineSchedule(ctx, paused.ID, "New name", "", "0 9 * * *", "UTC"); !errors.Is(err, ErrRoutineRunActive) {
		t.Fatalf("edit during active occurrence = %v", err)
	}
	if _, err := instance.FinishRoutineRun(ctx, domain.RoutineFinish{
		RunID:      claimed.ID,
		LeaseOwner: claimed.LeaseOwner,
		LeaseToken: claimed.LeaseToken,
		State:      domain.RoutineLedgerCompleted,
		Now:        base.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.EditRoutineSchedule(ctx, paused.ID, "New name", "", "0 9 * * *", "UTC"); err != nil {
		t.Fatalf("edit after the occurrence finished = %v", err)
	}
}

func TestEditRoutineScheduleKeepsHeartbeatCadence(t *testing.T) {
	ctx := context.Background()
	instance, botID := openRoutineTestStore(t)

	created, err := instance.CreateRoutine(ctx, domain.RoutineDraft{
		BotID:                    botID,
		Name:                     "Watch the build queue",
		Kind:                     domain.RoutineKindHeartbeat,
		TimeZone:                 "Europe/Vienna",
		HeartbeatIntervalSeconds: 900,
		LeadHarness:              domain.RoutineLeadGrokBuild,
		Worker:                   domain.RoutineWorkerClaude,
		Retry:                    domain.RoutineRetryPolicy{MaxAttempts: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.HeartbeatOptIn {
		t.Fatal("heartbeat routine was created already opted in")
	}

	// A heartbeat routine has no expression. The edit renames it and moves its
	// zone without inventing a cadence or flipping the opt-in bit.
	edited, err := instance.EditRoutineSchedule(ctx, created.ID, "Watch CI", "Report red builds.", "0 9 * * *", "UTC")
	if err != nil {
		t.Fatalf("EditRoutineSchedule: %v", err)
	}
	if edited.CronExpression != "" {
		t.Fatalf("heartbeat routine gained a cron expression: %q", edited.CronExpression)
	}
	if edited.HeartbeatIntervalSeconds != 900 || edited.HeartbeatOptIn {
		t.Fatalf("heartbeat cadence or opt-in changed: %#v", edited)
	}
	if edited.Name != "Watch CI" || edited.TimeZone != "UTC" {
		t.Fatalf("heartbeat edit did not apply: %#v", edited)
	}
}
