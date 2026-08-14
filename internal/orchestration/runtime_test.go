package orchestration

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var runtimeTestLeads = []LeadHarness{LeadGrokBuild, LeadCodexAppServer, LeadOpenCode}

var runtimeTestWorkers = []Worker{
	WorkerPi,
	WorkerClaude,
	WorkerCodex,
	WorkerGrok,
	WorkerOpenCode,
	WorkerCursor,
}

type leadExecutorFunc func(context.Context, LeadRequest) (LeadResult, error)

func (fn leadExecutorFunc) ExecuteLead(ctx context.Context, request LeadRequest) (LeadResult, error) {
	return fn(ctx, request)
}

type workerExecutorFunc func(context.Context, WorkerRequest) (WorkerResult, error)

func (fn workerExecutorFunc) ExecuteWorker(ctx context.Context, request WorkerRequest) (WorkerResult, error) {
	return fn(ctx, request)
}

func TestRuntimeIsDisabledByDefault(t *testing.T) {
	var called atomic.Int32
	runtime := NewRuntime(RuntimeConfig{}, map[LeadHarness]LeadExecutor{
		LeadGrokBuild: leadExecutorFunc(func(context.Context, LeadRequest) (LeadResult, error) {
			called.Add(1)
			return LeadResult{}, nil
		}),
	}, map[Worker]WorkerExecutor{
		WorkerPi: workerExecutorFunc(func(context.Context, WorkerRequest) (WorkerResult, error) {
			called.Add(1)
			return WorkerResult{}, nil
		}),
	}, nil)

	result, err := runtime.Run(context.Background(), "run-disabled", readyDecision(t, LeadGrokBuild, WorkerPi))
	if !errors.Is(err, ErrRuntimeDisabled) {
		t.Fatalf("Run() error = %v, want ErrRuntimeDisabled", err)
	}
	if result.State != RuntimeRejected || called.Load() != 0 {
		t.Fatalf("result = %#v, calls = %d; want rejected with no calls", result, called.Load())
	}
	assertEventKinds(t, result.Events, LifecycleRunRejected)
}

func TestRuntimeRoutesEveryLeadWorkerCombinationWithoutSubstitution(t *testing.T) {
	for _, lead := range runtimeTestLeads {
		for _, worker := range runtimeTestWorkers {
			lead, worker := lead, worker
			t.Run(string(lead)+"/"+string(worker), func(t *testing.T) {
				var leadRequests []LeadRequest
				var workerRequests []WorkerRequest
				leads := map[LeadHarness]LeadExecutor{
					lead: leadExecutorFunc(func(_ context.Context, request LeadRequest) (LeadResult, error) {
						leadRequests = append(leadRequests, request)
						return LeadResult{Output: "lead:" + string(lead)}, nil
					}),
				}
				workers := map[Worker]WorkerExecutor{
					worker: workerExecutorFunc(func(_ context.Context, request WorkerRequest) (WorkerResult, error) {
						workerRequests = append(workerRequests, request)
						return WorkerResult{Output: "worker:" + string(worker)}, nil
					}),
				}
				runtime := NewRuntime(RuntimeConfig{Enabled: true}, leads, workers, nil)

				result, err := runtime.Run(context.Background(), "matrix-"+string(lead)+"-"+string(worker), readyDecision(t, lead, worker))
				if err != nil {
					t.Fatalf("Run() error = %v", err)
				}
				if result.State != RuntimeCompleted {
					t.Fatalf("state = %q, want %q", result.State, RuntimeCompleted)
				}
				if len(leadRequests) != 1 || leadRequests[0].Plan.Lead != lead {
					t.Fatalf("lead requests = %#v, want exact lead %q", leadRequests, lead)
				}
				if len(workerRequests) != 1 || workerRequests[0].Route.Worker != worker {
					t.Fatalf("worker requests = %#v, want exact worker %q", workerRequests, worker)
				}
				if workerRequests[0].Lead != lead || workerRequests[0].LeadOutput != "lead:"+string(lead) {
					t.Fatalf("worker request lost lead identity/output: %#v", workerRequests[0])
				}
				if workerRequests[0].Role != WorkerRolePrimary || workerRequests[0].DelegatedIndex != -1 || workerRequests[0].MaxTurns != 0 || workerRequests[0].MaxDurationSeconds != 0 {
					t.Fatalf("primary metadata = %#v, want unbounded primary metadata", workerRequests[0])
				}
				assertMonotonicEvents(t, result.Events)
				assertEventKinds(t, result.Events,
					LifecycleRunStarted,
					LifecycleLeadStarted,
					LifecycleLeadCompleted,
					LifecycleWorkerStarted,
					LifecycleWorkerCompleted,
					LifecycleRunCompleted,
				)
			})
		}
	}
}

func TestRuntimeDelegatedFanOutFanInIsDeterministicAndBounded(t *testing.T) {
	plan := DefaultRunPlan(LeadCodexAppServer, WorkerPi, "/workspace/project")
	plan.Authorization.LeadCapabilities = append(plan.Authorization.LeadCapabilities, CapabilityDelegate)
	plan.Authorization.AllowedWorkers = append([]Worker(nil), runtimeTestWorkers...)
	for index, worker := range runtimeTestWorkers[1:] {
		plan.Delegated = append(plan.Delegated, DelegatedWorker{
			Route: WorkerRoute{
				Worker: worker,
				Options: WorkerOptions{
					Model:      "model-" + string(worker),
					Reasoning:  ReasoningEffort([]ReasoningEffort{ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningXHigh, ReasoningDefault}[index]),
					Permission: PermissionReadOnly,
				},
			},
			Scope:              fmt.Sprintf("scope-%d", index),
			MaxTurns:           uint16(index + 2),
			MaxDurationSeconds: 30,
		})
	}
	decision, err := Decide(plan)
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}

	leads := map[LeadHarness]LeadExecutor{
		LeadCodexAppServer: leadExecutorFunc(func(context.Context, LeadRequest) (LeadResult, error) {
			return LeadResult{Output: "bounded plan"}, nil
		}),
	}
	workers := map[Worker]WorkerExecutor{
		WorkerPi: workerExecutorFunc(func(_ context.Context, request WorkerRequest) (WorkerResult, error) {
			if request.Role != WorkerRolePrimary {
				t.Fatalf("pi role = %q, want primary", request.Role)
			}
			return WorkerResult{Output: "primary"}, nil
		}),
	}
	releases := make([]chan struct{}, len(plan.Delegated))
	var received sync.Map
	for index, delegated := range plan.Delegated {
		index, worker := index, delegated.Route.Worker
		releases[index] = make(chan struct{})
		workers[worker] = workerExecutorFunc(func(ctx context.Context, request WorkerRequest) (WorkerResult, error) {
			received.Store(index, request)
			select {
			case <-releases[index]:
				return WorkerResult{Output: "result-" + string(worker), TurnsUsed: request.MaxTurns}, nil
			case <-ctx.Done():
				return WorkerResult{}, ctx.Err()
			}
		})
	}
	var sinkEvents []LifecycleEvent
	runtime := NewRuntime(RuntimeConfig{Enabled: true}, leads, workers, func(event LifecycleEvent) {
		sinkEvents = append(sinkEvents, event)
	})

	type response struct {
		result RuntimeResult
		err    error
	}
	finished := make(chan response, 1)
	go func() {
		result, err := runtime.Run(context.Background(), "fan-out", decision)
		finished <- response{result: result, err: err}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		allStarted := true
		for index := range plan.Delegated {
			if _, ok := received.Load(index); !ok {
				allStarted = false
				break
			}
		}
		if allStarted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("delegated workers did not fan out")
		}
		time.Sleep(time.Millisecond)
	}
	for index := len(releases) - 1; index >= 0; index-- {
		close(releases[index])
	}
	responseValue := <-finished
	if responseValue.err != nil {
		t.Fatalf("Run() error = %v", responseValue.err)
	}
	result := responseValue.result
	if result.State != RuntimeCompleted || len(result.Delegated) != len(plan.Delegated) {
		t.Fatalf("result state/count = %q/%d", result.State, len(result.Delegated))
	}
	for index, execution := range result.Delegated {
		want := plan.Delegated[index]
		if execution.DelegatedIndex != index || execution.Route != want.Route || execution.Scope != want.Scope || execution.MaxTurns != want.MaxTurns || execution.MaxDurationSeconds != want.MaxDurationSeconds {
			t.Fatalf("delegated[%d] metadata = %#v, want %#v", index, execution, want)
		}
		stored, _ := received.Load(index)
		request := stored.(WorkerRequest)
		if request.Route != want.Route || request.Scope != want.Scope || request.MaxTurns != want.MaxTurns || request.MaxDurationSeconds != want.MaxDurationSeconds || request.LeadOutput != "bounded plan" {
			t.Fatalf("request[%d] = %#v, want exact plan metadata", index, request)
		}
	}
	if !reflect.DeepEqual(result.Events, sinkEvents) {
		t.Fatalf("sink events differ from result events\nsink: %#v\nresult: %#v", sinkEvents, result.Events)
	}
	assertMonotonicEvents(t, result.Events)
	var started, finishedEvents []Worker
	for _, event := range result.Events {
		if event.WorkerRole != WorkerRoleDelegated {
			continue
		}
		switch event.Kind {
		case LifecycleWorkerStarted:
			started = append(started, event.Worker)
		case LifecycleWorkerCompleted:
			finishedEvents = append(finishedEvents, event.Worker)
		}
	}
	wantWorkers := append([]Worker(nil), runtimeTestWorkers[1:]...)
	if !reflect.DeepEqual(started, wantWorkers) || !reflect.DeepEqual(finishedEvents, wantWorkers) {
		t.Fatalf("delegated event order started=%v finished=%v, want %v", started, finishedEvents, wantWorkers)
	}
}

func TestRuntimePreflightRejectsMissingExactExecutorWithoutSubstitution(t *testing.T) {
	var calls atomic.Int32
	leads := map[LeadHarness]LeadExecutor{
		LeadGrokBuild: leadExecutorFunc(func(context.Context, LeadRequest) (LeadResult, error) {
			calls.Add(1)
			return LeadResult{}, nil
		}),
	}
	workers := map[Worker]WorkerExecutor{
		WorkerClaude: workerExecutorFunc(func(context.Context, WorkerRequest) (WorkerResult, error) {
			calls.Add(1)
			return WorkerResult{}, nil
		}),
	}
	runtime := NewRuntime(RuntimeConfig{Enabled: true}, leads, workers, nil)
	result, err := runtime.Run(context.Background(), "missing-pi", readyDecision(t, LeadGrokBuild, WorkerPi))
	if !errors.Is(err, ErrExecutorUnavailable) || !strings.Contains(err.Error(), string(WorkerPi)) {
		t.Fatalf("Run() error = %v, want unavailable pi", err)
	}
	if calls.Load() != 0 || result.State != RuntimeRejected {
		t.Fatalf("calls/state = %d/%q, want 0/rejected", calls.Load(), result.State)
	}
	assertEventKinds(t, result.Events, LifecycleRunRejected)
}

func TestRuntimeLeadCannotMutateAuthorizedTopology(t *testing.T) {
	decision := delegatedPlan(t, LeadGrokBuild, WorkerPi, WorkerClaude)
	var claudeCalls, grokCalls atomic.Int32
	runtime := NewRuntime(RuntimeConfig{Enabled: true}, map[LeadHarness]LeadExecutor{
		LeadGrokBuild: leadExecutorFunc(func(_ context.Context, request LeadRequest) (LeadResult, error) {
			request.Plan.Delegated[0].Route.Worker = WorkerGrok
			request.Plan.Authorization.AllowedWorkers[0] = WorkerGrok
			return LeadResult{}, nil
		}),
	}, map[Worker]WorkerExecutor{
		WorkerPi: workerExecutorFunc(func(context.Context, WorkerRequest) (WorkerResult, error) {
			return WorkerResult{}, nil
		}),
		WorkerClaude: workerExecutorFunc(func(_ context.Context, request WorkerRequest) (WorkerResult, error) {
			claudeCalls.Add(1)
			if request.Route.Worker != WorkerClaude {
				t.Errorf("worker route = %q, want claude", request.Route.Worker)
			}
			return WorkerResult{}, nil
		}),
		WorkerGrok: workerExecutorFunc(func(context.Context, WorkerRequest) (WorkerResult, error) {
			grokCalls.Add(1)
			return WorkerResult{}, nil
		}),
	}, nil)

	result, err := runtime.Run(context.Background(), "immutable-topology", decision)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Delegated[0].Route.Worker != WorkerClaude || claudeCalls.Load() != 1 || grokCalls.Load() != 0 {
		t.Fatalf("route/calls = %q/%d/%d, want claude/1/0", result.Delegated[0].Route.Worker, claudeCalls.Load(), grokCalls.Load())
	}
}

func TestRuntimeAlreadyCanceledContextSkipsExecutors(t *testing.T) {
	var calls atomic.Int32
	runtime := NewRuntime(RuntimeConfig{Enabled: true}, map[LeadHarness]LeadExecutor{
		LeadGrokBuild: leadExecutorFunc(func(context.Context, LeadRequest) (LeadResult, error) {
			calls.Add(1)
			return LeadResult{}, nil
		}),
	}, map[Worker]WorkerExecutor{
		WorkerPi: workerExecutorFunc(func(context.Context, WorkerRequest) (WorkerResult, error) {
			calls.Add(1)
			return WorkerResult{}, nil
		}),
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := runtime.Run(ctx, "already-canceled", readyDecision(t, LeadGrokBuild, WorkerPi))
	if !errors.Is(err, context.Canceled) || result.State != RuntimeCanceled || calls.Load() != 0 {
		t.Fatalf("Run() result/error/calls = %#v/%v/%d, want canceled/no calls", result, err, calls.Load())
	}
	assertEventKinds(t, result.Events, LifecycleRunCanceled)
}

func TestRuntimeRejectsPendingOrInvalidDecisionBeforeExecutors(t *testing.T) {
	var calls atomic.Int32
	runtime := NewRuntime(RuntimeConfig{Enabled: true}, map[LeadHarness]LeadExecutor{
		LeadGrokBuild: leadExecutorFunc(func(context.Context, LeadRequest) (LeadResult, error) {
			calls.Add(1)
			return LeadResult{}, nil
		}),
	}, map[Worker]WorkerExecutor{
		WorkerPi: workerExecutorFunc(func(context.Context, WorkerRequest) (WorkerResult, error) {
			calls.Add(1)
			return WorkerResult{}, nil
		}),
	}, nil)

	pendingPlan := DefaultRunPlan(LeadGrokBuild, WorkerPi, "/workspace")
	pendingPlan.Computer.Browser = true
	pendingPlan.Authorization.LeadCapabilities = append(pendingPlan.Authorization.LeadCapabilities, CapabilityBrowser)
	pendingPlan.Approval = HumanApproval{Status: ApprovalPending, ApprovalID: "approval-1", Capabilities: []Capability{CapabilityBrowser}}
	pending, err := Decide(pendingPlan)
	if err != nil {
		t.Fatalf("Decide(pending) error = %v", err)
	}

	invalid := readyDecision(t, LeadGrokBuild, WorkerPi)
	invalid.Plan.Authorization.AllowedWorkers = nil
	for name, decision := range map[string]RoutingDecision{"pending": pending, "invalid": invalid} {
		t.Run(name, func(t *testing.T) {
			result, err := runtime.Run(context.Background(), "reject-"+name, decision)
			if err == nil || result.State != RuntimeRejected {
				t.Fatalf("Run() result/error = %#v/%v, want rejection", result, err)
			}
			assertEventKinds(t, result.Events, LifecycleRunRejected)
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("executor calls = %d, want 0", calls.Load())
	}
}

func TestRuntimeLeadAndPrimaryFailuresStopDelegation(t *testing.T) {
	boom := errors.New("boom")
	for _, test := range []struct {
		name        string
		leadError   error
		workerError error
		wantCalls   int32
	}{
		{name: "lead", leadError: boom, wantCalls: 0},
		{name: "primary", workerError: boom, wantCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := delegatedPlan(t, LeadGrokBuild, WorkerPi, WorkerClaude)
			var workerCalls atomic.Int32
			runtime := NewRuntime(RuntimeConfig{Enabled: true}, map[LeadHarness]LeadExecutor{
				LeadGrokBuild: leadExecutorFunc(func(context.Context, LeadRequest) (LeadResult, error) {
					return LeadResult{}, test.leadError
				}),
			}, map[Worker]WorkerExecutor{
				WorkerPi: workerExecutorFunc(func(context.Context, WorkerRequest) (WorkerResult, error) {
					workerCalls.Add(1)
					return WorkerResult{}, test.workerError
				}),
				WorkerClaude: workerExecutorFunc(func(context.Context, WorkerRequest) (WorkerResult, error) {
					workerCalls.Add(100)
					return WorkerResult{}, nil
				}),
			}, nil)
			result, err := runtime.Run(context.Background(), "failure-"+test.name, plan)
			if !errors.Is(err, boom) || result.State != RuntimeFailed {
				t.Fatalf("Run() result/error = %#v/%v, want failed boom", result, err)
			}
			if workerCalls.Load() != test.wantCalls {
				t.Fatalf("worker calls = %d, want %d", workerCalls.Load(), test.wantCalls)
			}
		})
	}
}

func TestRuntimeEnforcesChildTimeoutAndReportedTurnLimit(t *testing.T) {
	for _, test := range []struct {
		name       string
		execute    workerExecutorFunc
		wantStatus ExecutionStatus
		wantError  error
	}{
		{
			name: "timeout",
			execute: func(ctx context.Context, _ WorkerRequest) (WorkerResult, error) {
				<-ctx.Done()
				return WorkerResult{}, ctx.Err()
			},
			wantStatus: ExecutionTimedOut,
			wantError:  ErrChildTimeout,
		},
		{
			name: "turn limit",
			execute: func(_ context.Context, request WorkerRequest) (WorkerResult, error) {
				return WorkerResult{TurnsUsed: request.MaxTurns + 1}, nil
			},
			wantStatus: ExecutionFailed,
			wantError:  ErrTurnLimitExceeded,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision := delegatedPlan(t, LeadGrokBuild, WorkerPi, WorkerClaude)
			runtime := NewRuntime(RuntimeConfig{Enabled: true}, map[LeadHarness]LeadExecutor{
				LeadGrokBuild: leadExecutorFunc(func(context.Context, LeadRequest) (LeadResult, error) {
					return LeadResult{}, nil
				}),
			}, map[Worker]WorkerExecutor{
				WorkerPi: workerExecutorFunc(func(context.Context, WorkerRequest) (WorkerResult, error) {
					return WorkerResult{}, nil
				}),
				WorkerClaude: test.execute,
			}, nil)
			runtime.durationUnit = 5 * time.Millisecond
			result, err := runtime.Run(context.Background(), "bound-"+test.name, decision)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("Run() error = %v, want %v", err, test.wantError)
			}
			if result.Delegated[0].Status != test.wantStatus {
				t.Fatalf("status = %q, want %q", result.Delegated[0].Status, test.wantStatus)
			}
		})
	}
}

func TestRuntimeParentCancellationCancelsAllChildren(t *testing.T) {
	decision := delegatedPlan(t, LeadCodexAppServer, WorkerPi, WorkerClaude, WorkerGrok, WorkerCursor)
	started := make(chan struct{}, len(decision.Plan.Delegated))
	blockingWorker := workerExecutorFunc(func(ctx context.Context, _ WorkerRequest) (WorkerResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		return WorkerResult{}, ctx.Err()
	})
	runtime := NewRuntime(RuntimeConfig{Enabled: true}, map[LeadHarness]LeadExecutor{
		LeadCodexAppServer: leadExecutorFunc(func(context.Context, LeadRequest) (LeadResult, error) {
			return LeadResult{}, nil
		}),
	}, map[Worker]WorkerExecutor{
		WorkerPi: workerExecutorFunc(func(context.Context, WorkerRequest) (WorkerResult, error) {
			return WorkerResult{}, nil
		}),
		WorkerClaude: blockingWorker,
		WorkerGrok:   blockingWorker,
		WorkerCursor: blockingWorker,
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	type response struct {
		result RuntimeResult
		err    error
	}
	done := make(chan response, 1)
	go func() {
		result, err := runtime.Run(ctx, "cancel-children", decision)
		done <- response{result: result, err: err}
	}()
	for range decision.Plan.Delegated {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("worker did not start")
		}
	}
	cancel()
	responseValue := <-done
	if !errors.Is(responseValue.err, context.Canceled) || responseValue.result.State != RuntimeCanceled {
		t.Fatalf("Run() result/error = %#v/%v, want canceled", responseValue.result, responseValue.err)
	}
	for index, execution := range responseValue.result.Delegated {
		if execution.Status != ExecutionCanceled || !errors.Is(execution.Err, context.Canceled) {
			t.Fatalf("delegated[%d] = %#v, want canceled", index, execution)
		}
	}
	if responseValue.result.Events[len(responseValue.result.Events)-1].Kind != LifecycleRunCanceled {
		t.Fatalf("last event = %q, want run canceled", responseValue.result.Events[len(responseValue.result.Events)-1].Kind)
	}
}

func TestRuntimeRecoversExecutorPanics(t *testing.T) {
	for _, test := range []struct {
		name    string
		lead    LeadExecutor
		primary WorkerExecutor
	}{
		{
			name: "lead",
			lead: leadExecutorFunc(func(context.Context, LeadRequest) (LeadResult, error) {
				panic("lead panic")
			}),
			primary: workerExecutorFunc(func(context.Context, WorkerRequest) (WorkerResult, error) {
				return WorkerResult{}, nil
			}),
		},
		{
			name: "worker",
			lead: leadExecutorFunc(func(context.Context, LeadRequest) (LeadResult, error) {
				return LeadResult{}, nil
			}),
			primary: workerExecutorFunc(func(context.Context, WorkerRequest) (WorkerResult, error) {
				panic("worker panic")
			}),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := NewRuntime(RuntimeConfig{Enabled: true}, map[LeadHarness]LeadExecutor{LeadGrokBuild: test.lead}, map[Worker]WorkerExecutor{WorkerPi: test.primary}, nil)
			result, err := runtime.Run(context.Background(), "panic-"+test.name, readyDecision(t, LeadGrokBuild, WorkerPi))
			if !errors.Is(err, ErrExecutorPanic) || result.State != RuntimeFailed {
				t.Fatalf("Run() result/error = %#v/%v, want recovered failure", result, err)
			}
		})
	}
}

func readyDecision(t *testing.T, lead LeadHarness, worker Worker) RoutingDecision {
	t.Helper()
	decision, err := Decide(DefaultRunPlan(lead, worker, "/workspace"))
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	return decision
}

func delegatedPlan(t *testing.T, lead LeadHarness, primary Worker, delegated ...Worker) RoutingDecision {
	t.Helper()
	plan := DefaultRunPlan(lead, primary, "/workspace")
	plan.Authorization.LeadCapabilities = append(plan.Authorization.LeadCapabilities, CapabilityDelegate)
	for index, worker := range delegated {
		plan.Authorization.AllowedWorkers = append(plan.Authorization.AllowedWorkers, worker)
		plan.Delegated = append(plan.Delegated, DelegatedWorker{
			Route:              WorkerRoute{Worker: worker, Options: WorkerOptions{Reasoning: ReasoningHigh, Permission: PermissionReadOnly}},
			Scope:              fmt.Sprintf("scope-%d", index),
			MaxTurns:           4,
			MaxDurationSeconds: 10,
		})
	}
	decision, err := Decide(plan)
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	return decision
}

func assertEventKinds(t *testing.T, events []LifecycleEvent, want ...LifecycleKind) {
	t.Helper()
	got := make([]LifecycleKind, len(events))
	for index := range events {
		got[index] = events[index].Kind
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event kinds = %v, want %v", got, want)
	}
}

func assertMonotonicEvents(t *testing.T, events []LifecycleEvent) {
	t.Helper()
	for index, event := range events {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("event[%d].Sequence = %d, want %d", index, event.Sequence, index+1)
		}
	}
}
