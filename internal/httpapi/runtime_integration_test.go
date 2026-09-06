package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/browsermcp"
	"github.com/robbyczgw-cla/openagentfleet/internal/collaborationmcp"
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

type capabilityLeaseExecutor struct {
	server *Server
	t      *testing.T
}

func (e capabilityLeaseExecutor) RunWithOptions(_ context.Context, _, _, _ string, options harness.RunOptions) (string, error) {
	e.server.computerCapabilityMu.Lock()
	computer := e.server.computerCapabilities["computer-token"]
	e.server.computerCapabilityMu.Unlock()
	e.server.collabCapabilityMu.RLock()
	collab := e.server.collabCapabilities["collab-token"]
	e.server.collabCapabilityMu.RUnlock()
	for name, lease := range map[string]computerCapability{"computer": computer, "collaboration": collab} {
		if time.Until(lease.expiresAt) < 50*time.Second {
			e.t.Errorf("%s lease was not bound at execution: %v", name, lease.expiresAt)
		}
		if lease.runID == "" {
			e.t.Errorf("%s lease has no run", name)
		}
	}
	for _, server := range options.MCPServers {
		if server.Name == browsermcp.MCPServerName && server.Env[browsermcp.RunIDEnv] != computer.runID {
			e.t.Error("computer MCP run ID missing")
		}
		if server.Name == collaborationmcp.MCPServerName && server.Env[collaborationmcp.RunIDEnv] != collab.runID {
			e.t.Error("collaboration MCP run ID missing")
		}
	}
	return "done", nil
}

func TestQueuedTurnBindsCapabilityLeasesAtExecution(t *testing.T) {
	server, instance, botID := openRoutineScheduler(t)
	server.Broker = events.New()
	server.runExecutorOverride = capabilityLeaseExecutor{server, t}
	conversation, err := instance.CanonicalConversationForBot(t.Context(), botID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, run, _, err := instance.CreateMessageWithAttachmentsAndRun(t.Context(), conversation.ID, botID, "grok", "test", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate leases whose submission-time TTL elapsed while another turn ran.
	expired := computerCapability{runID: run.ID, expiresAt: time.Now().Add(-time.Minute)}
	server.computerCapabilities = map[string]computerCapability{"computer-token": expired}
	server.collabCapabilities = map[string]computerCapability{"collab-token": expired}
	servers := []harness.MCPServerSpec{
		{Name: browsermcp.MCPServerName, Env: map[string]string{browsermcp.RunTokenEnv: "computer-token"}},
		{Name: collaborationmcp.MCPServerName, Env: map[string]string{collaborationmcp.RunTokenEnv: "collab-token"}},
	}
	server.executeRunWithContext(t.Context(), run, "", "", "", "", "", "", 60, servers)
	if len(server.computerCapabilities) != 0 || len(server.collabCapabilities) != 0 {
		t.Fatal("completed turn retained capability leases")
	}
}

func TestDelegationStartFollowsExecutionAndSkipsCancelledTurns(t *testing.T) {
	for _, cancelQueued := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", cancelQueued), func(t *testing.T) {
			server, instance, sourceID := openRoutineScheduler(t)
			server.Broker = events.New()
			server.Turns = coordinator.NewTurnQueue()
			server.AllowHarnessExecution = true
			server.runExecutorOverride = &blockingExecutor{}
			if _, err := instance.PatchAgent(t.Context(), sourceID, domain.AgentProfileUpdate{}, func(metadata domain.AgentMetadata) (domain.AgentMetadata, error) {
				metadata.Collaboration = &domain.AgentCollaboration{Enabled: true}
				return domain.NormalizeAgentMetadata(metadata)
			}); err != nil {
				t.Fatal(err)
			}
			target, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Reviewer", Title: "Reviews", Description: "Review delegated work"})
			if err != nil {
				t.Fatal(err)
			}
			sourceConv, err := instance.CanonicalConversationForBot(t.Context(), sourceID)
			if err != nil {
				t.Fatal(err)
			}
			sourceRun, _, err := instance.CreateRunWithQueuedEvent(t.Context(), sourceConv.ID, sourceID, "grok", "coordinate")
			if err != nil {
				t.Fatal(err)
			}
			release := make(chan struct{})
			blockerStarted := make(chan struct{})
			server.launchAgentTurn(target.Bot.ID, "blocker", func(context.Context) { close(blockerStarted); <-release })
			<-blockerStarted
			stream, unsubscribe := server.Broker.Subscribe(t.Context())
			defer unsubscribe()
			_, run, err := server.startAgentCollaboration(t.Context(), sourceRun, target.Bot.ID, "review this", domain.HandoffModeDelegate)
			if err != nil {
				close(release)
				t.Fatal(err)
			}
			created := false
			draining := true
			for draining {
				select {
				case event := <-stream:
					if event.Type == domain.EventAgentDelegationCreated {
						created = true
					}
					if event.Type == domain.EventAgentDelegationStarted {
						close(release)
						t.Fatal("delegation started while queued")
					}
				default:
					draining = false
				}
			}
			if !created {
				close(release)
				t.Fatal("missing created event")
			}
			if cancelQueued {
				response := performRequest(server.Handler(), "POST", "/api/runs/"+run.ID+"/stop", "", "")
				if response.Code != 202 {
					close(release)
					t.Fatalf("stop = %d: %s", response.Code, response.Body.String())
				}
			}
			close(release)
			deadline := time.After(5 * time.Second)
			runStarted, delegationStarted := false, false
			for {
				select {
				case event := <-stream:
					if event.RunID != run.ID {
						continue
					}
					if event.Type == "run.started" {
						runStarted = true
					}
					if event.Type == domain.EventAgentDelegationStarted {
						if cancelQueued {
							t.Fatal("cancelled delegation emitted started")
						}
						if !runStarted {
							t.Fatal("delegation start preceded durable run start")
						}
						delegationStarted = true
					}
					if event.Type == domain.EventAgentDelegationCompleted || event.Type == domain.EventAgentDelegationFailed {
						if !cancelQueued && !delegationStarted {
							t.Fatal("delegation completed without starting")
						}
						// Wait for the queued callback and its cleanup before closing the store.
						waitForInactiveRuntimeTurn(t, server, run.ID)
						select {
						case event := <-stream:
							if cancelQueued && event.Type == domain.EventAgentDelegationStarted {
								t.Fatal("cancelled delegation emitted late started")
							}
						default:
						}
						return
					}
				case <-deadline:
					t.Fatal("delegation did not finish")
				}
			}
		})
	}
}

func waitForInactiveRuntimeTurn(t *testing.T, server *Server, runID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		server.activeMu.Lock()
		active := server.activeRuns[runID] != nil
		server.activeMu.Unlock()
		if !active {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("run remained registered")
}

func TestStoppingQueuedRoutinePreservesUserStop(t *testing.T) {
	server, instance, botID := openRoutineScheduler(t)
	server.Turns = coordinator.NewTurnQueue()
	server.Broker = events.New()
	server.AllowHarnessExecution = true
	server.runExecutorOverride = &blockingExecutor{}
	routine := createEnabledRoutine(t, instance, botID, time.Now().Add(time.Hour), domain.RoutineApprovalOnRisk)
	release := make(chan struct{})
	started := make(chan struct{})
	server.launchAgentTurn(botID, "blocker", func(context.Context) { close(started); <-release })
	<-started
	defer close(release)
	done := make(chan error, 1)
	go func() { done <- server.executeScheduledRoutine(t.Context(), routine, domain.RoutineRun{}) }()
	conversation, err := instance.CanonicalConversationForBot(t.Context(), botID)
	if err != nil {
		t.Fatal(err)
	}
	var runID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := instance.ListRuns(t.Context(), conversation.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) > 0 {
			server.activeMu.Lock()
			active := server.activeRuns[runs[0].ID] != nil
			server.activeMu.Unlock()
			if active {
				runID = runs[0].ID
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if runID == "" {
		t.Fatal("routine was not queued")
	}
	response := performRequest(server.Handler(), "POST", "/api/runs/"+runID+"/stop", "", "")
	if response.Code != 202 {
		t.Fatalf("stop = %d: %s", response.Code, response.Body.String())
	}
	select {
	case err := <-done:
		if err == nil || err.Error() != "agent run stopped" {
			t.Fatalf("routine result = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued routine did not stop")
	}
	stored, err := instance.ListRunEvents(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	stopped := 0
	for _, event := range stored {
		if event.Type == "run.started" {
			t.Fatal("stopped routine started")
		}
		if event.Type == "run.stopped" {
			stopped++
			var data map[string]string
			if err := json.Unmarshal([]byte(event.Data), &data); err != nil {
				t.Fatal(err)
			}
			if data["reason"] != "user_requested" {
				t.Fatalf("stop reason = %v", data)
			}
		}
	}
	if stopped != 1 {
		t.Fatalf("stopped events = %d", stopped)
	}
}

func TestSkippedTurnDoesNotActivateCapabilities(t *testing.T) {
	for _, withWorkers := range []bool{false, true} {
		t.Run(fmt.Sprint("workers=", withWorkers), func(t *testing.T) {
			server, instance, botID := openRoutineScheduler(t)
			server.Broker = events.New()
			executor := &blockingExecutor{started: make(chan struct{}, 1)}
			server.runExecutorOverride = executor
			conversation, err := instance.CanonicalConversationForBot(t.Context(), botID)
			if err != nil {
				t.Fatal(err)
			}
			run, _, err := instance.CreateRunWithQueuedEvent(t.Context(), conversation.ID, botID, "grok", "skipped")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			specs := []harness.MCPServerSpec{
				{Name: browsermcp.MCPServerName, Env: map[string]string{browsermcp.RunTokenEnv: "computer"}},
				{Name: collaborationmcp.MCPServerName, Env: map[string]string{collaborationmcp.RunTokenEnv: "collab"}},
			}
			if withWorkers {
				server.executeLeadWorkerRunWithContext(ctx, run, "", "", "", "", "", "", 0, specs, "", nil)
			} else {
				server.executeRunWithContext(ctx, run, "", "", "", "", "", "", 0, specs)
			}
			if len(server.computerCapabilities) > 0 || len(server.collabCapabilities) > 0 {
				t.Fatal("skipped turn retained capabilities")
			}
			select {
			case <-executor.started:
				t.Fatal("skipped turn launched provider")
			default:
			}
			stored, err := instance.ListRunEvents(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			stopped := 0
			for _, event := range stored {
				if event.Type == "run.started" {
					t.Fatal("skipped turn emitted started")
				}
				if event.Type == "run.stopped" {
					stopped++
				}
			}
			if stopped != 1 {
				t.Fatalf("stopped events = %d", stopped)
			}
		})
	}
}
