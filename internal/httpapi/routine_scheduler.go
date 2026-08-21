package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

const (
	routineSchedulerInterval = 5 * time.Second
	routineLeaseDuration     = 15 * time.Minute
	routineLeaseRenewEvery   = 30 * time.Second
	routineTickLimit         = 20
)

var (
	errRoutineWaitingApproval = errors.New("routine occurrence is waiting for approval")
	errRoutineDenied          = errors.New("routine occurrence was denied")
)

// RunRoutineScheduler recovers stale leases and claims due occurrences until
// ctx is cancelled. The first tick runs immediately so a just-enabled routine
// does not wait a full interval.
func (s *Server) RunRoutineScheduler(ctx context.Context) {
	if s == nil || s.Store == nil {
		return
	}
	s.routineSchedCtx = ctx
	defer func() { s.routineSchedCtx = nil }()
	if err := s.TickRoutines(ctx); err != nil {
		slog.Warn("routine scheduler tick", "error", err)
	}
	ticker := time.NewTicker(routineSchedulerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.TickRoutines(ctx); err != nil {
				slog.Warn("routine scheduler tick", "error", err)
			}
		}
	}
}

func (s *Server) TickRoutines(ctx context.Context) error {
	if s == nil || s.Store == nil {
		return nil
	}
	if !s.routineTickMu.TryLock() {
		return nil
	}
	defer s.routineTickMu.Unlock()
	now := s.currentTime()
	if _, err := s.Store.RecoverStaleRoutineRuns(ctx, now, routineTickLimit); err != nil {
		return err
	}
	preferences, err := s.Store.GetPreferences(ctx)
	if err != nil {
		return err
	}
	if !preferences.Normalize().Features.Routines {
		return nil
	}
	due, err := s.Store.ListDueRoutines(ctx, now, routineTickLimit)
	if err != nil {
		return err
	}
	for _, routine := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if routine.Kind == domain.RoutineKindHeartbeat && !preferences.Normalize().Features.Heartbeat {
			continue
		}
		s.claimDueRoutine(ctx, routine, now)
	}
	return nil
}

func (s *Server) claimDueRoutine(ctx context.Context, routine domain.Routine, now time.Time) {
	approvalID, err := s.routineClaimApprovalID(ctx, routine, now)
	if err != nil {
		if !errors.Is(err, errRoutineWaitingApproval) {
			slog.Warn("routine claim skipped", "routine", routine.ID, "error", err)
		}
		return
	}
	claimed, err := s.Store.ClaimRoutineRun(ctx, domain.RoutineClaim{
		RoutineID:      routine.ID,
		LeaseOwner:     s.routineOwner(),
		LeaseDuration:  s.leaseDuration(),
		IdempotencyKey: id.New("claim"),
		ApprovalID:     approvalID,
		Now:            now,
	})
	if err != nil {
		slog.Warn("routine claim failed", "routine", routine.ID, "error", err)
		return
	}
	s.launchRoutineOccurrence(ctx, routine, claimed)
}

func (s *Server) launchRoutineOccurrence(ctx context.Context, routine domain.Routine, claimed domain.RoutineRun) {
	runCtx := s.occurrenceContext(ctx)
	if s.routineRunner != nil {
		s.completeRoutineOccurrence(runCtx, routine, claimed)
		return
	}
	go s.completeRoutineOccurrence(runCtx, routine, claimed)
}

func (s *Server) completeRoutineOccurrence(ctx context.Context, routine domain.Routine, claimed domain.RoutineRun) {
	now := s.currentTime()
	running, err := s.Store.StartRoutineRun(ctx, claimed.ID, claimed.LeaseOwner, claimed.LeaseToken, now)
	if err != nil {
		s.finishRoutineOccurrence(ctx, routine, claimed, domain.RoutineLedgerFailed, err.Error(), now)
		return
	}
	claimed = running
	stopRenew := s.renewRoutineLeaseUntil(ctx, claimed)
	defer stopRenew()
	runner := s.routineRunner
	if runner == nil {
		runner = s.executeScheduledRoutine
	}
	runErr := runner(ctx, routine, claimed)
	finishedAt := s.currentTime()
	if runErr != nil {
		s.finishRoutineOccurrence(ctx, routine, claimed, domain.RoutineLedgerFailed, runErr.Error(), finishedAt)
		return
	}
	s.finishRoutineOccurrence(ctx, routine, claimed, domain.RoutineLedgerCompleted, "routine run completed", finishedAt)
}

func (s *Server) renewRoutineLeaseUntil(ctx context.Context, claimed domain.RoutineRun) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(s.leaseRenewEvery())
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := s.Store.RenewRoutineRunLease(ctx, claimed.ID, claimed.LeaseOwner, claimed.LeaseToken, s.currentTime(), s.leaseDuration()); err != nil {
					slog.Warn("routine lease renew failed", "run", claimed.ID, "error", err)
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

func (s *Server) finishRoutineOccurrence(ctx context.Context, routine domain.Routine, claimed domain.RoutineRun, state domain.RoutineLedgerState, reason string, now time.Time) {
	finish := domain.RoutineFinish{
		RunID:      claimed.ID,
		LeaseOwner: claimed.LeaseOwner,
		LeaseToken: claimed.LeaseToken,
		State:      state,
		Reason:     reason,
		Now:        now,
	}
	if state == domain.RoutineLedgerCompleted && routine.Kind == domain.RoutineKindCron && !domain.RoutineSkipsSchedule(claimed.Trigger) {
		next, err := domain.NextCronTime(routine.CronExpression, routine.TimeZone, now)
		if err != nil {
			finish.State = domain.RoutineLedgerFailed
			finish.Reason = err.Error()
		} else {
			finish.NextRunAt = next
		}
	}
	if _, err := s.Store.FinishRoutineRun(ctx, finish); err != nil {
		slog.Warn("routine finish failed", "routine", routine.ID, "run", claimed.ID, "error", err)
		return
	}
	if updated, err := s.Store.GetRoutine(ctx, routine.ID); err == nil {
		s.publishRoutine(updated)
	}
}

func (s *Server) executeScheduledRoutine(ctx context.Context, routine domain.Routine, occurrence domain.RoutineRun) error {
	conversation, err := s.Store.CanonicalConversationForBot(ctx, routine.BotID)
	if err != nil {
		return err
	}
	agent, hasAgent, err := s.agentForBot(ctx, routine.BotID)
	if err != nil {
		return err
	}
	_, model, reasoning, tier, permission, webSearch, timeoutSeconds, mcpIDs, err := s.leadRunSettings(ctx, agent, hasAgent)
	if err != nil {
		return err
	}
	provider := configuredLeadProvider(string(routine.LeadHarness))
	content, prompt := scheduledRoutinePrompt(routine, occurrence.Trigger)
	memories, err := s.Store.RetrieveBotMemories(ctx, routine.BotID, memoryPromptMaxCount, memoryPromptMaxBytes)
	if err != nil {
		return err
	}
	prompt = promptWithBotMemory(prompt, memories)
	systemPrompt := ""
	if hasAgent {
		systemPrompt = agentSystemPrompt(agent.Bot)
	}
	mcpServers, err := s.leadMCPServerSpecs(ctx, mcpIDs)
	if err != nil {
		return err
	}
	if err := rejectPiLeadMCP(provider, mcpServers); err != nil {
		return err
	}
	message, _, run, queuedEvent, err := s.Store.CreateMessageWithAttachmentsAndRun(ctx, conversation.ID, conversation.BotID, provider, content, prompt, nil)
	if err != nil {
		return err
	}
	_ = message
	s.publishStoredRunEvent(run, queuedEvent)
	if !s.AllowHarnessExecution {
		if _, blockErr := s.commitRunLifecycleEvent(ctx, run, "blocked", "harness execution is disabled", "run.blocked", `{"reason":"execution_disabled"}`); blockErr != nil {
			return blockErr
		}
		return errors.New("harness execution is disabled")
	}
	if s.harnessRunExecutor() == nil {
		if _, failErr := s.commitRunLifecycleEvent(ctx, run, "failed", "harness runner unavailable", "run.failed", `{"reason":"runner_unavailable"}`); failErr != nil {
			return failErr
		}
		return errors.New("harness runner unavailable")
	}
	s.executeRunWithContext(ctx, run, systemPrompt, model, reasoning, tier, permission, webSearch, timeoutSeconds, mcpServers)
	current, err := s.Store.GetRun(ctx, run.ID)
	if err != nil {
		return err
	}
	if current.Status != "completed" {
		if current.Error != "" {
			return fmt.Errorf("agent run %s: %s", current.Status, current.Error)
		}
		return fmt.Errorf("agent run %s", current.Status)
	}
	return nil
}

func (s *Server) routineClaimApprovalID(ctx context.Context, routine domain.Routine, now time.Time) (string, error) {
	if routine.ApprovalPolicy != domain.RoutineApprovalAlways {
		return "", nil
	}
	action := store.RoutineApprovalAction(routine.ID, routine.OccurrenceKey)
	approvals, err := s.Store.ListApprovals(ctx, "")
	if err != nil {
		return "", err
	}
	var pending bool
	for _, approval := range approvals {
		if approval.Action != action {
			continue
		}
		switch approval.Status {
		case "approved":
			used, usedErr := s.Store.RoutineLedgerHasApproval(ctx, approval.ID)
			if usedErr != nil {
				return "", usedErr
			}
			if used {
				continue
			}
			return approval.ID, nil
		case "pending":
			pending = true
		case "denied", "cancelled":
			return "", errRoutineDenied
		}
	}
	if pending {
		return "", errRoutineWaitingApproval
	}
	if err := s.requestRoutineApproval(ctx, routine, now, routine.OccurrenceKey, domain.RoutineTriggerSchedule); err != nil {
		return "", err
	}
	return "", errRoutineWaitingApproval
}

func (s *Server) routineTestClaimApprovalID(ctx context.Context, routine domain.Routine, now time.Time) (approvalID, occurrenceKey string, err error) {
	if routine.ApprovalPolicy != domain.RoutineApprovalAlways {
		return "", "", nil
	}
	approvals, err := s.Store.ListApprovals(ctx, "")
	if err != nil {
		return "", "", err
	}
	var pending bool
	for _, approval := range approvals {
		trigger, routineID, key, ok := store.ParseRoutineApprovalAction(approval.Action)
		if !ok || trigger != domain.RoutineTriggerTest || routineID != routine.ID {
			continue
		}
		switch approval.Status {
		case "approved":
			used, usedErr := s.Store.RoutineLedgerHasApproval(ctx, approval.ID)
			if usedErr != nil {
				return "", "", usedErr
			}
			if used {
				continue
			}
			return approval.ID, key, nil
		case "pending":
			pending = true
		}
	}
	if pending {
		return "", "", errRoutineWaitingApproval
	}
	if err := s.requestRoutineApproval(ctx, routine, now, id.New("occurrence"), domain.RoutineTriggerTest); err != nil {
		return "", "", err
	}
	return "", "", errRoutineWaitingApproval
}

func (s *Server) routineWebhookClaimApprovalID(ctx context.Context, routine domain.Routine, now time.Time) (approvalID, occurrenceKey string, err error) {
	if routine.ApprovalPolicy != domain.RoutineApprovalAlways {
		return "", "", nil
	}
	approvals, err := s.Store.ListApprovals(ctx, "")
	if err != nil {
		return "", "", err
	}
	var pending bool
	for _, approval := range approvals {
		trigger, routineID, key, ok := store.ParseRoutineApprovalAction(approval.Action)
		if !ok || trigger != domain.RoutineTriggerWebhook || routineID != routine.ID {
			continue
		}
		switch approval.Status {
		case "approved":
			used, usedErr := s.Store.RoutineLedgerHasApproval(ctx, approval.ID)
			if usedErr != nil {
				return "", "", usedErr
			}
			if used {
				continue
			}
			return approval.ID, key, nil
		case "pending":
			pending = true
		}
	}
	if pending {
		return "", "", errRoutineWaitingApproval
	}
	if err := s.requestRoutineApproval(ctx, routine, now, id.New("occurrence"), domain.RoutineTriggerWebhook); err != nil {
		return "", "", err
	}
	return "", "", errRoutineWaitingApproval
}

func (s *Server) requestRoutineApproval(ctx context.Context, routine domain.Routine, now time.Time, occurrenceKey, trigger string) error {
	occurrenceKey = strings.TrimSpace(occurrenceKey)
	if occurrenceKey == "" {
		occurrenceKey = routine.OccurrenceKey
	}
	action := store.RoutineApprovalAction(routine.ID, occurrenceKey)
	title := "Scheduled routine: " + routine.Name
	prompt := "Approve scheduled routine: " + routine.Name
	if trigger == domain.RoutineTriggerTest {
		action = store.RoutineTestApprovalAction(routine.ID, occurrenceKey)
		title = "Test routine: " + routine.Name
		prompt = "Approve test routine: " + routine.Name
	}
	if trigger == domain.RoutineTriggerWebhook {
		action = store.RoutineWebhookApprovalAction(routine.ID, occurrenceKey)
		title = "Webhook routine: " + routine.Name
		prompt = "Approve webhook routine: " + routine.Name
	}
	existing, err := s.Store.ListApprovals(ctx, "pending")
	if err != nil {
		return err
	}
	for _, approval := range existing {
		if approval.Action == action {
			return nil
		}
	}
	conversation, err := s.Store.CanonicalConversationForBot(ctx, routine.BotID)
	if err != nil {
		return err
	}
	run, queued, err := s.Store.CreateRunWithQueuedEvent(ctx, conversation.ID, conversation.BotID, "openagentfleet", prompt)
	if err != nil {
		return err
	}
	s.publishStoredRunEvent(run, queued)
	payload, _ := json.Marshal(map[string]any{
		"options": []map[string]string{
			{"optionId": "allow_once", "name": "Allow this run", "kind": "allow_once"},
			{"optionId": "reject", "name": "Deny this run", "kind": "reject_once"},
		},
		"tool_call": map[string]string{"title": title},
	})
	approval, err := s.Store.CreateApproval(ctx, run.ID, "openagentfleet", action, string(payload))
	if err != nil {
		return err
	}
	if _, err := s.commitRunLifecycleEvent(ctx, run, "waiting_for_approval", "", "run.waiting_for_approval", `{"status":"waiting_for_approval"}`); err != nil {
		return err
	}
	approvalPayload, _ := json.Marshal(approval)
	_, _ = s.emitRunEvent(ctx, run, "approval.requested", string(approvalPayload))
	_ = now
	s.publishRoutine(routine)
	return nil
}

func (s *Server) finishRoutineApproval(ctx context.Context, approval domain.ApprovalRequest, status string) {
	trigger, routineID, occurrenceKey, ok := store.ParseRoutineApprovalAction(approval.Action)
	if !ok {
		return
	}
	run, err := s.Store.GetRun(ctx, approval.RunID)
	if err != nil {
		return
	}
	if status == "approved" {
		_ = s.commitTerminalRunLifecycleEvent(run, "stopped", "", "run.stopped", `{"status":"stopped","reason":"routine_gate_approved"}`)
		if trigger == domain.RoutineTriggerTest {
			s.startApprovedRoutineTest(ctx, routineID, occurrenceKey, approval.ID)
			return
		}
		if trigger == domain.RoutineTriggerWebhook {
			s.startApprovedRoutineWebhook(ctx, routineID, occurrenceKey, approval.ID)
			return
		}
		go func() { _ = s.TickRoutines(s.occurrenceContext(context.Background())) }()
		return
	}
	_ = s.commitTerminalRunLifecycleEvent(run, "stopped", "routine occurrence denied", "run.stopped", `{"status":"stopped","reason":"routine_denied"}`)
	if domain.RoutineSkipsSchedule(trigger) {
		if item, getErr := s.Store.GetRoutine(ctx, routineID); getErr == nil {
			s.publishRoutine(item)
		}
		return
	}
	if routineID != "" {
		if err := s.skipRoutineOccurrence(ctx, routineID); err != nil {
			slog.Warn("skip denied routine occurrence", "routine", routineID, "error", err)
		}
	}
}

func (s *Server) startApprovedRoutineTest(ctx context.Context, routineID, occurrenceKey, approvalID string) {
	item, err := s.Store.GetRoutine(ctx, routineID)
	if err != nil {
		slog.Warn("approved test routine missing", "routine", routineID, "error", err)
		return
	}
	if item.Status == domain.RoutineStatusNeedsAttention {
		slog.Warn("approved test routine needs attention", "routine", routineID)
		return
	}
	claimed, err := s.Store.ClaimTestRoutineRun(ctx, domain.RoutineClaim{
		RoutineID:      routineID,
		LeaseOwner:     s.routineOwner(),
		LeaseDuration:  s.leaseDuration(),
		IdempotencyKey: id.New("claim"),
		ApprovalID:     approvalID,
		OccurrenceKey:  occurrenceKey,
		Now:            s.currentTime(),
	})
	if err != nil {
		slog.Warn("approved test routine claim failed", "routine", routineID, "error", err)
		return
	}
	s.launchRoutineOccurrence(ctx, item, claimed)
}

func (s *Server) startApprovedRoutineWebhook(ctx context.Context, routineID, occurrenceKey, approvalID string) {
	item, err := s.Store.GetRoutine(ctx, routineID)
	if err != nil {
		slog.Warn("approved webhook routine missing", "routine", routineID, "error", err)
		return
	}
	claimed, err := s.Store.ClaimWebhookRoutineRun(ctx, domain.RoutineClaim{
		RoutineID:      routineID,
		LeaseOwner:     s.routineOwner(),
		LeaseDuration:  s.leaseDuration(),
		IdempotencyKey: id.New("claim"),
		ApprovalID:     approvalID,
		OccurrenceKey:  occurrenceKey,
		Now:            s.currentTime(),
	})
	if err != nil {
		slog.Warn("approved webhook routine claim failed", "routine", routineID, "error", err)
		return
	}
	s.launchRoutineOccurrence(ctx, item, claimed)
}

func (s *Server) skipRoutineOccurrence(ctx context.Context, routineID string) error {
	item, err := s.Store.GetRoutine(ctx, routineID)
	if err != nil {
		return err
	}
	next, err := domain.DefaultNextRunAt(item, s.currentTime())
	if err != nil {
		return err
	}
	updated, err := s.Store.ResumeRoutine(ctx, routineID, next)
	if err != nil {
		return err
	}
	s.publishRoutine(updated)
	return nil
}

func (s *Server) publishRoutine(item domain.Routine) {
	if s.Broker == nil {
		return
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return
	}
	s.Broker.Publish(domain.StreamEvent{
		ID:        id.New("evt"),
		BotID:     item.BotID,
		Type:      "routine.updated",
		Data:      string(payload),
		CreatedAt: s.currentTime().UTC().Format(time.RFC3339Nano),
	})
}

func (s *Server) occurrenceContext(fallback context.Context) context.Context {
	if s != nil && s.routineSchedCtx != nil {
		return s.routineSchedCtx
	}
	if fallback != nil {
		return fallback
	}
	return context.Background()
}

func (s *Server) leaseDuration() time.Duration {
	if s != nil && s.routineLease >= 5*time.Second {
		return s.routineLease
	}
	return routineLeaseDuration
}

func (s *Server) leaseRenewEvery() time.Duration {
	if s != nil && s.routineLeaseRenewEvery > 0 {
		return s.routineLeaseRenewEvery
	}
	return routineLeaseRenewEvery
}

func (s *Server) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Server) routineOwner() string {
	if s != nil && strings.TrimSpace(s.routineLeaseOwner) != "" {
		return s.routineLeaseOwner
	}
	return "botd"
}

func scheduledRoutinePrompt(routine domain.Routine, trigger string) (content, prompt string) {
	label := "Scheduled routine"
	due := "is due. Complete this task now."
	if trigger == domain.RoutineTriggerTest {
		label = "Test routine"
		due = "was requested as a test run. Complete this task now."
	}
	if trigger == domain.RoutineTriggerWebhook {
		label = "Webhook routine"
		due = "was triggered by a signed webhook. Complete this task now."
	}
	content = label + ": " + strings.TrimSpace(routine.Name)
	if description := strings.TrimSpace(routine.Description); description != "" {
		content += "\n\n" + description
	}
	prompt = "OpenAgentFleet " + strings.ToLower(label) + " " + strings.TrimSpace(routine.Name) + " " + due + "\n\n" + content
	return content, prompt
}
