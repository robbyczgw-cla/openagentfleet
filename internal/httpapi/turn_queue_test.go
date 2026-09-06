package httpapi

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/coordinator"
	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/events"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

type blockingExecutor struct {
	started chan struct{}
	release chan struct{}
	current atomic.Int32
	max     atomic.Int32
}

func (e *blockingExecutor) RunWithOptions(ctx context.Context, _ string, _ string, _ string, _ harness.RunOptions) (string, error) {
	running := e.current.Add(1)
	if running > e.max.Load() {
		e.max.Store(running)
	}
	if e.started != nil {
		select {
		case e.started <- struct{}{}:
		default:
		}
	}
	if e.release != nil {
		select {
		case <-e.release:
		case <-ctx.Done():
			e.current.Add(-1)
			return "", ctx.Err()
		}
	}
	e.current.Add(-1)
	return "ok", nil
}

func TestSameAgentTurnsSerializeThroughCoordinator(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	executor := &blockingExecutor{release: make(chan struct{})}
	handler := (&Server{
		Store: instance, Broker: events.New(), AllowHarnessExecution: true,
		runExecutorOverride: executor, Turns: coordinator.NewTurnQueue(),
	}).Handler()

	first := performRequest(handler, "POST", "/api/messages", `{"conversation_id":"`+conversation.ID+`","content":"one","provider":"grok"}`, "")
	second := performRequest(handler, "POST", "/api/messages", `{"conversation_id":"`+conversation.ID+`","content":"two","provider":"grok"}`, "")
	if first.Code != 202 || second.Code != 202 {
		t.Fatalf("codes = %d %d bodies = %s %s", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	time.Sleep(80 * time.Millisecond)
	if executor.max.Load() > 1 {
		t.Fatalf("same agent ran concurrently: max=%d", executor.max.Load())
	}
	close(executor.release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := instance.ListRuns(t.Context(), conversation.ID)
		if err != nil {
			t.Fatal(err)
		}
		completed := 0
		for _, run := range runs {
			if run.Status == "completed" || run.Status == "failed" || run.Status == "stopped" {
				completed++
			}
		}
		if completed == 2 {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("queued turns did not finish")
}

func TestDifferentAgentsDoNotShareTurnLock(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	firstConv, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	secondAgent, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Reviewer", Title: "Builder", Description: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if secondAgent.Conversation == nil {
		t.Fatal("second agent has no conversation")
	}
	executor := &blockingExecutor{started: make(chan struct{}, 2), release: make(chan struct{})}
	handler := (&Server{
		Store: instance, Broker: events.New(), AllowHarnessExecution: true,
		runExecutorOverride: executor, Turns: coordinator.NewTurnQueue(),
	}).Handler()
	go performRequest(handler, "POST", "/api/messages", `{"conversation_id":"`+firstConv.ID+`","content":"a","provider":"grok"}`, "")
	go performRequest(handler, "POST", "/api/messages", `{"conversation_id":"`+secondAgent.Conversation.ID+`","content":"b","provider":"grok"}`, "")
	deadline := time.After(2 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-executor.started:
		case <-deadline:
			t.Fatalf("different agents did not start concurrently; started=%d max=%d", i, executor.max.Load())
		}
	}
	close(executor.release)
}
