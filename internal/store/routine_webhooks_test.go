package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestRoutineWebhookSecretIsHashedAndRequiredForEnabledClaim(t *testing.T) {
	ctx := context.Background()
	instance, botID := openRoutineTestStore(t)
	base := time.Date(2027, 8, 9, 16, 0, 0, 0, time.UTC)
	created, err := instance.CreateRoutine(ctx, routineTestDraft(botID, base))
	if err != nil {
		t.Fatal(err)
	}
	view, secret, err := instance.RotateRoutineWebhook(ctx, created.ID)
	if err != nil || !view.Configured || secret == "" {
		t.Fatalf("RotateRoutineWebhook = %#v %q %v", view, secret, err)
	}
	got, err := instance.GetRoutineWebhook(ctx, created.ID)
	if err != nil || !got.Configured || got.CreatedAt == "" {
		t.Fatalf("GetRoutineWebhook = %#v %v", got, err)
	}
	if err := instance.AuthenticateRoutineWebhook(ctx, created.ID, "wrong"); !errors.Is(err, ErrRoutineWebhookInvalid) {
		t.Fatalf("wrong secret = %v", err)
	}
	if err := instance.AuthenticateRoutineWebhook(ctx, created.ID, secret); err != nil {
		t.Fatalf("good secret = %v", err)
	}
	if _, err := instance.ClaimWebhookRoutineRun(ctx, routineTestClaim(created.ID, "hook", "hook-1", base)); !errors.Is(err, ErrRoutineDisabled) {
		t.Fatalf("webhook on disabled = %v", err)
	}
	enabled := createAndResumeRoutine(t, instance, routineTestDraft(botID, base.Add(time.Hour)), time.Time{})
	if _, secret, err = instance.RotateRoutineWebhook(ctx, enabled.ID); err != nil {
		t.Fatal(err)
	}
	run, err := instance.ClaimWebhookRoutineRun(ctx, routineTestClaim(enabled.ID, "hook", "hook-2", base.Add(time.Hour)))
	if err != nil || run.Trigger != domain.RoutineTriggerWebhook {
		t.Fatalf("ClaimWebhookRoutineRun = %#v %v", run, err)
	}
	before, err := instance.GetRoutine(ctx, enabled.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.StartRoutineRun(ctx, run.ID, run.LeaseOwner, run.LeaseToken, base.Add(time.Hour+time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.FinishRoutineRun(ctx, domain.RoutineFinish{
		RunID: run.ID, LeaseOwner: run.LeaseOwner, LeaseToken: run.LeaseToken,
		State: domain.RoutineLedgerCompleted, NextRunAt: base.Add(2 * time.Hour), Now: base.Add(time.Hour + 2*time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	after, err := instance.GetRoutine(ctx, enabled.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.NextRunAt != before.NextRunAt || after.OccurrenceKey != before.OccurrenceKey {
		t.Fatalf("webhook finish advanced schedule: before=%#v after=%#v", before, after)
	}
	if err := instance.RevokeRoutineWebhook(ctx, enabled.ID); err != nil {
		t.Fatal(err)
	}
	if err := instance.AuthenticateRoutineWebhook(ctx, enabled.ID, secret); !errors.Is(err, ErrRoutineWebhookInvalid) {
		t.Fatalf("revoked secret still worked: %v", err)
	}
}
