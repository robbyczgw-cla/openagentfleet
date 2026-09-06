package coordinator

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTurnQueueSameAgentSerialized(t *testing.T) {
	q := NewTurnQueue()
	started := make(chan struct{})
	block := make(chan struct{})
	var order []int
	var mu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		err := q.Enqueue(context.Background(), "agent-a", "turn-1", func(context.Context) error {
			close(started)
			<-block
			mu.Lock()
			order = append(order, 1)
			mu.Unlock()
			return nil
		})
		if err != nil {
			t.Errorf("turn-1: %v", err)
		}
	}()
	<-started
	secondStarted := make(chan struct{})
	go func() {
		defer wg.Done()
		err := q.Enqueue(context.Background(), "agent-a", "turn-2", func(context.Context) error {
			close(secondStarted)
			mu.Lock()
			order = append(order, 2)
			mu.Unlock()
			return nil
		})
		if err != nil {
			t.Errorf("turn-2: %v", err)
		}
	}()
	waitQueued(t, q, "agent-a", 1)
	select {
	case <-secondStarted:
		t.Fatal("same-agent second turn started before the first finished")
	case <-time.After(40 * time.Millisecond):
	}
	close(block)
	waitDone(t, &wg)
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("order = %v, want [1 2]", order)
	}
}

func TestTurnQueueDifferentAgentsConcurrent(t *testing.T) {
	q := NewTurnQueue()
	aStarted := make(chan struct{})
	bStarted := make(chan struct{})
	release := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = q.Enqueue(context.Background(), "agent-a", "turn-a", func(context.Context) error {
			close(aStarted)
			<-release
			return nil
		})
	}()
	go func() {
		defer wg.Done()
		_ = q.Enqueue(context.Background(), "agent-b", "turn-b", func(context.Context) error {
			close(bStarted)
			<-release
			return nil
		})
	}()

	waitChan(t, aStarted, "agent-a did not start")
	waitChan(t, bStarted, "agent-b did not start")
	close(release)
	waitDone(t, &wg)
}

func TestTurnQueueFailureDoesNotPoisonLaterTurns(t *testing.T) {
	q := NewTurnQueue()
	boom := errors.New("boom")
	if err := q.Enqueue(context.Background(), "agent-a", "turn-fail", func(context.Context) error {
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("failed turn: %v", err)
	}
	if err := q.Enqueue(context.Background(), "agent-a", "turn-ok", func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("later turn: %v", err)
	}
}

func TestTurnQueueCancelQueuedTurnDoesNotBlockLaterTurns(t *testing.T) {
	q := NewTurnQueue()
	started := make(chan struct{})
	block := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = q.Enqueue(context.Background(), "agent-a", "turn-1", func(context.Context) error {
			close(started)
			<-block
			return nil
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	var ran atomic.Bool
	queuedErr := make(chan error, 1)
	go func() {
		queuedErr <- q.Enqueue(ctx, "agent-a", "turn-2", func(context.Context) error {
			ran.Store(true)
			return nil
		})
	}()
	waitQueued(t, q, "agent-a", 1)
	cancel()
	err := waitErr(t, queuedErr)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("queued turn: %v", err)
	}
	if ran.Load() {
		t.Fatal("canceled queued turn ran")
	}

	laterErr := make(chan error, 1)
	go func() {
		laterErr <- q.Enqueue(context.Background(), "agent-a", "turn-3", func(context.Context) error {
			return nil
		})
	}()
	waitQueued(t, q, "agent-a", 1)
	close(block)
	if err := waitErr(t, laterErr); err != nil {
		t.Fatalf("later turn after cancel: %v", err)
	}
	waitDone(t, &wg)
}

func TestCoordinatorHoldsTurnQueue(t *testing.T) {
	c := New(nil, nil, nil)
	if c.Turns == nil {
		t.Fatal("coordinator turns queue is nil")
	}
	if err := c.Turns.Enqueue(context.Background(), "agent-a", "turn-1", func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func waitQueued(t *testing.T, q *TurnQueue, agentID string, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		agent := q.agents[agentID]
		count := 0
		if agent != nil {
			count = len(agent.jobs)
		}
		q.mu.Unlock()
		if count >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d queued jobs for %s", n, agentID)
}

func waitChan(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}

func waitErr(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for enqueue result")
		return nil
	}
}

func waitDone(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turns to finish")
	}
}
