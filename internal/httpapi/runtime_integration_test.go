package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/coordinator"
	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/events"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
)

func TestAgentLaunchPreservesSubmissionOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := &Server{Turns: coordinator.NewTurnQueue()}
		release := make(chan struct{})
		got := make(chan int, 32)
		server.launchAgentTurn("agent", "blocker", func(context.Context) { <-release })
		synctest.Wait()
		for i := 0; i < cap(got); i++ {
			server.launchAgentTurn("agent", fmt.Sprint(i), func(context.Context) { got <- i })
		}
		synctest.Wait()
		close(release)
		synctest.Wait()
		for want := 0; want < cap(got); want++ {
			if actual := <-got; actual != want {
				t.Fatalf("turn = %d, want %d", actual, want)
			}
		}
	})
}

func TestScheduledRoutineWaitsForAgentChat(t *testing.T) {
	server, instance, botID := openRoutineScheduler(t)
	server.Turns = coordinator.NewTurnQueue()
	server.Broker = events.New()
	server.AllowHarnessExecution = true
	executor := &blockingExecutor{started: make(chan struct{}, 1)}
	server.runExecutorOverride = executor
	routine := createEnabledRoutine(t, instance, botID, time.Now().Add(time.Hour), domain.RoutineApprovalOnRisk)
	release := make(chan struct{})
	started := make(chan struct{})
	server.launchAgentTurn(botID, "chat", func(context.Context) { close(started); <-release })
	<-started
	done := make(chan error, 1)
	go func() { done <- server.executeScheduledRoutine(t.Context(), routine, domain.RoutineRun{}) }()
	select {
	case <-executor.started:
		close(release)
		<-done
		t.Fatal("routine started while the Agent's chat was still running")
	case err := <-done:
		close(release)
		t.Fatalf("routine finished before chat: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("routine did not finish after chat released the Agent")
	}
	select {
	case <-executor.started:
	default:
		t.Fatal("routine never reached the provider")
	}
}

type runtimeEventExecutor struct{}

func (runtimeEventExecutor) RunWithOptions(_ context.Context, _, _, _ string, options harness.RunOptions) (string, error) {
	for _, kind := range []string{"text", "thought", "plan", "turn/plan/updated", "item/started", "tool_call"} {
		options.OnLine(harness.OutputLine{Stream: "stdout", Type: kind, Text: kind})
	}
	return "done", nil
}

func TestRuntimePersistsProviderWorkEvents(t *testing.T) {
	server, instance, botID := openRoutineScheduler(t)
	server.Broker = events.New()
	server.runExecutorOverride = runtimeEventExecutor{}
	conversation, err := instance.CanonicalConversationForBot(t.Context(), botID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, run, _, err := instance.CreateMessageWithAttachmentsAndRun(t.Context(), conversation.ID, botID, "grok", "test", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	server.executeRunWithContext(t.Context(), run, "", "", "", "", "", "", 0, nil)
	stored, err := instance.ListRunEvents(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, event := range stored {
		if event.Type != domain.EventProviderOutput {
			continue
		}
		var payload map[string]string
		if err := json.Unmarshal([]byte(event.Data), &payload); err != nil {
			t.Fatal(err)
		}
		counts[payload["type"]]++
	}
	for _, kind := range []string{"plan", "turn/plan/updated", "item/started", "tool_call"} {
		if counts[kind] != 1 {
			t.Errorf("persisted %s %d times, want 1", kind, counts[kind])
		}
	}
	for _, kind := range []string{"text", "thought"} {
		if counts[kind] != 0 {
			t.Errorf("persisted transient %s", kind)
		}
	}
}

func TestCancelledQueuedAgentTurnReleasesExecutorResources(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := &Server{Turns: coordinator.NewTurnQueue()}
		release := make(chan struct{})
		cleaned := false
		server.launchAgentTurn("agent", "blocker", func(context.Context) { <-release })
		synctest.Wait()
		server.launchAgentTurn("agent", "cancelled", func(ctx context.Context) {
			if ctx.Err() == nil {
				t.Error("cancelled turn reached execution without cancellation")
			}
			cleaned = true
		})
		server.activeMu.Lock()
		cancel := server.activeRuns["cancelled"]
		server.activeMu.Unlock()
		cancel()
		close(release)
		synctest.Wait()
		if !cleaned {
			t.Fatal("cancelled turn skipped executor cleanup")
		}
		server.activeMu.Lock()
		defer server.activeMu.Unlock()
		if len(server.activeRuns) != 0 {
			t.Fatalf("active runs after completion: %d", len(server.activeRuns))
		}
	})
}
