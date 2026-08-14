package events

import (
	"context"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestBrokerPublishesAndUnsubscribes(t *testing.T) {
	broker := New()
	ctx, cancel := context.WithCancel(context.Background())
	channel, unsubscribe := broker.Subscribe(ctx)
	broker.Publish(domain.StreamEvent{ID: "evt-1", Type: "run.started"})
	select {
	case event := <-channel:
		if event.ID != "evt-1" {
			t.Fatalf("event id = %q", event.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
	unsubscribe()
	cancel()
	broker.Publish(domain.StreamEvent{ID: "evt-2"})
	select {
	case _, ok := <-channel:
		if ok {
			t.Fatal("received event after unsubscribe")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription was not closed")
	}
}
