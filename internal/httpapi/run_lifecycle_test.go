package httpapi

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/events"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

type cancellationAwareExecutor struct {
}

func (executor *cancellationAwareExecutor) RunWithOptions(ctx context.Context, _ string, _ string, _ string, _ harness.RunOptions) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

type lateOutputExecutor struct {
	started  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func (executor *lateOutputExecutor) RunWithOptions(_ context.Context, _ string, _ string, _ string, _ harness.RunOptions) (string, error) {
	defer close(executor.finished)
	close(executor.started)
	<-executor.release
	return "late provider answer", nil
}

func TestRunCanBeStoppedImmediatelyAfterAccepted(t *testing.T) {
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
	executor := &cancellationAwareExecutor{}
	server := (&Server{
		Store: instance, Broker: events.New(), AllowHarnessExecution: true,
		runExecutorOverride: executor,
	}).Handler()

	response := performRequest(server, "POST", "/api/messages", `{"conversation_id":"`+conversation.ID+`","content":"stop me","provider":"grok"}`, "")
	if response.Code != 202 {
		t.Fatalf("create message = %d, body = %s", response.Code, response.Body.String())
	}
	var created struct {
		Run struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Run.ID == "" {
		t.Fatal("accepted message did not return a run id")
	}

	stopResponse := performRequest(server, "POST", "/api/runs/"+created.Run.ID+"/stop", "", "")
	if stopResponse.Code != 202 {
		t.Fatalf("immediate stop = %d, body = %s", stopResponse.Code, stopResponse.Body.String())
	}
	run := waitForTerminalRun(t, instance, conversation.ID)
	if run.Status != "stopped" {
		t.Fatalf("stopped run = %#v", run)
	}
}

func TestStoppedRunDoesNotPersistLateProviderAnswer(t *testing.T) {
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
	executor := &lateOutputExecutor{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	server := (&Server{
		Store: instance, Broker: events.New(), AllowHarnessExecution: true,
		runExecutorOverride: executor,
	}).Handler()

	response := performRequest(server, "POST", "/api/messages", `{"conversation_id":"`+conversation.ID+`","content":"stop before answer","provider":"grok"}`, "")
	if response.Code != 202 {
		t.Fatalf("create message = %d, body = %s", response.Code, response.Body.String())
	}
	var created struct {
		Run struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start before stop")
	}
	stopResponse := performRequest(server, "POST", "/api/runs/"+created.Run.ID+"/stop", "", "")
	if stopResponse.Code != 202 {
		t.Fatalf("stop = %d, body = %s", stopResponse.Code, stopResponse.Body.String())
	}
	close(executor.release)
	select {
	case <-executor.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not finish after release")
	}
	if run := waitForTerminalRun(t, instance, conversation.ID); run.Status != "stopped" {
		t.Fatalf("run after late provider answer = %#v", run)
	}
	messages, err := instance.ListMessages(t.Context(), conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("late assistant answer persisted: %#v", messages)
	}
}
