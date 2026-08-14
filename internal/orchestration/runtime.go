package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	// ErrRuntimeDisabled is returned by the zero-value, fail-closed runtime
	// configuration. Enabling orchestration must always be explicit.
	ErrRuntimeDisabled = errors.New("orchestration runtime is disabled")
	// ErrExecutorUnavailable means the exact executor selected by the plan was
	// not registered. The runtime never substitutes another lead or worker.
	ErrExecutorUnavailable = errors.New("orchestration executor is unavailable")
	// ErrChildTimeout marks a delegated worker whose plan-owned duration bound
	// expired. Primary workers are bounded only by the parent context because
	// RunPlan does not define a separate primary timeout.
	ErrChildTimeout = errors.New("delegated worker timed out")
	// ErrTurnLimitExceeded marks a delegated executor that reported using more
	// turns than the plan authorized.
	ErrTurnLimitExceeded = errors.New("delegated worker exceeded max turns")
	// ErrExecutorPanic converts an adapter panic into a failed run instead of
	// allowing one injected executor to crash the controller.
	ErrExecutorPanic = errors.New("orchestration executor panicked")
)

// RuntimeConfig is deliberately off by default. RuntimeConfig{} performs no
// lead or worker execution.
type RuntimeConfig struct {
	Enabled bool
}

// RuntimeState is the controller-owned terminal state of one execution.
type RuntimeState string

const (
	RuntimeCompleted RuntimeState = "completed"
	RuntimeFailed    RuntimeState = "failed"
	RuntimeCanceled  RuntimeState = "canceled"
	RuntimeRejected  RuntimeState = "rejected"
)

// ActorRole identifies which layer a lifecycle event describes.
type ActorRole string

const (
	ActorController ActorRole = "controller"
	ActorLead       ActorRole = "lead"
	ActorWorker     ActorRole = "worker"
)

// WorkerRole separates the plan's primary worker from bounded delegated
// children. Only delegated workers carry child turn and duration bounds.
type WorkerRole string

const (
	WorkerRolePrimary   WorkerRole = "primary"
	WorkerRoleDelegated WorkerRole = "delegated"
)

// ExecutionStatus is the status of a lead or worker invocation.
type ExecutionStatus string

const (
	ExecutionCompleted ExecutionStatus = "completed"
	ExecutionFailed    ExecutionStatus = "failed"
	ExecutionCanceled  ExecutionStatus = "canceled"
	ExecutionTimedOut  ExecutionStatus = "timed_out"
)

// LifecycleKind is a deterministic controller event. Delegated start and
// finish events are emitted in RunPlan order, independent of completion order.
type LifecycleKind string

const (
	LifecycleRunRejected     LifecycleKind = "run_rejected"
	LifecycleRunStarted      LifecycleKind = "run_started"
	LifecycleRunCompleted    LifecycleKind = "run_completed"
	LifecycleRunFailed       LifecycleKind = "run_failed"
	LifecycleRunCanceled     LifecycleKind = "run_canceled"
	LifecycleLeadStarted     LifecycleKind = "lead_started"
	LifecycleLeadCompleted   LifecycleKind = "lead_completed"
	LifecycleLeadFailed      LifecycleKind = "lead_failed"
	LifecycleLeadCanceled    LifecycleKind = "lead_canceled"
	LifecycleWorkerStarted   LifecycleKind = "worker_started"
	LifecycleWorkerCompleted LifecycleKind = "worker_completed"
	LifecycleWorkerFailed    LifecycleKind = "worker_failed"
	LifecycleWorkerCanceled  LifecycleKind = "worker_canceled"
	LifecycleWorkerTimedOut  LifecycleKind = "worker_timed_out"
)

// LifecycleEvent contains routing identity and bounds, but no model output or
// secrets. Sequence starts at one for every Run call.
type LifecycleEvent struct {
	Sequence           uint64        `json:"sequence"`
	Kind               LifecycleKind `json:"kind"`
	RunID              string        `json:"run_id"`
	Actor              ActorRole     `json:"actor"`
	Lead               LeadHarness   `json:"lead,omitempty"`
	Worker             Worker        `json:"worker,omitempty"`
	WorkerRole         WorkerRole    `json:"worker_role,omitempty"`
	DelegatedIndex     int           `json:"delegated_index"`
	Scope              string        `json:"scope,omitempty"`
	MaxTurns           uint16        `json:"max_turns,omitempty"`
	MaxDurationSeconds uint32        `json:"max_duration_seconds,omitempty"`
	Error              string        `json:"error,omitempty"`
}

// LifecycleSink receives controller events after they have been appended to
// RuntimeResult. It must be safe to call synchronously.
type LifecycleSink func(LifecycleEvent)

// LeadRequest is a snapshot of the validated plan. It exposes no runtime or
// delegation callback, so the lead cannot add routes beyond the plan.
type LeadRequest struct {
	RunID string
	Plan  RunPlan
}

// LeadResult is intentionally route-free. Output may be supplied to the exact
// workers already authorized by RunPlan, but cannot change their identities.
type LeadResult struct {
	Output string
}

// WorkerRequest contains the exact selected route. DelegatedIndex is -1 for
// the primary worker and the RunPlan slice index for a delegated child.
// MaxTurns and MaxDurationSeconds are zero for the primary worker.
type WorkerRequest struct {
	RunID              string
	Lead               LeadHarness
	Role               WorkerRole
	DelegatedIndex     int
	Route              WorkerRoute
	Workdir            string
	Scope              string
	MaxTurns           uint16
	MaxDurationSeconds uint32
	LeadOutput         string
}

// WorkerResult is adapter-neutral. Delegated turns are checked against the
// authorized maximum when the executor reports a non-zero value.
type WorkerResult struct {
	Output    string
	TurnsUsed uint16
}

// LeadExecutor and WorkerExecutor are injected adapters. Implementations may
// use processes in another package, but this runtime does not. Executors must
// honor context cancellation and be safe for concurrent delegated calls.
type LeadExecutor interface {
	ExecuteLead(context.Context, LeadRequest) (LeadResult, error)
}

type WorkerExecutor interface {
	ExecuteWorker(context.Context, WorkerRequest) (WorkerResult, error)
}

// LeadExecution captures one lead invocation.
type LeadExecution struct {
	Lead   LeadHarness
	Status ExecutionStatus
	Result LeadResult
	Err    error
}

// WorkerExecution captures one primary or delegated invocation. Delegated
// results are stored in RunPlan order, never completion order.
type WorkerExecution struct {
	Role               WorkerRole
	DelegatedIndex     int
	Route              WorkerRoute
	Scope              string
	MaxTurns           uint16
	MaxDurationSeconds uint32
	Status             ExecutionStatus
	Result             WorkerResult
	Err                error
}

// RuntimeResult is complete even when Run returns an error, allowing callers
// to persist deterministic lifecycle and per-child outcomes.
type RuntimeResult struct {
	RunID     string
	State     RuntimeState
	Lead      LeadExecution
	Primary   WorkerExecution
	Delegated []WorkerExecution
	Events    []LifecycleEvent
}

// Runtime is a stateless controller core. The executor registries are copied
// at construction so later caller mutations cannot alter routing mid-run.
type Runtime struct {
	config       RuntimeConfig
	leads        map[LeadHarness]LeadExecutor
	workers      map[Worker]WorkerExecutor
	sink         LifecycleSink
	durationUnit time.Duration
}

// NewRuntime creates a process-free orchestration runtime. Registries may be
// partial because providers are optional; Run preflights every exact executor
// selected by its plan before invoking any of them.
func NewRuntime(config RuntimeConfig, leads map[LeadHarness]LeadExecutor, workers map[Worker]WorkerExecutor, sink LifecycleSink) *Runtime {
	leadCopy := make(map[LeadHarness]LeadExecutor, len(leads))
	for lead, executor := range leads {
		leadCopy[lead] = executor
	}
	workerCopy := make(map[Worker]WorkerExecutor, len(workers))
	for worker, executor := range workers {
		workerCopy[worker] = executor
	}
	return &Runtime{
		config:       config,
		leads:        leadCopy,
		workers:      workerCopy,
		sink:         sink,
		durationUnit: time.Second,
	}
}

// Run executes one validated, explicitly enabled Lead -> Worker plan. The lead
// runs first, then the primary worker, then delegated children fan out in plan
// order and fan in concurrently. Workers receive no delegation capability.
func (r *Runtime) Run(ctx context.Context, runID string, decision RoutingDecision) (RuntimeResult, error) {
	result := RuntimeResult{RunID: runID, State: RuntimeRejected}
	var sequence uint64
	emit := func(event LifecycleEvent) {
		sequence++
		event.Sequence = sequence
		event.RunID = runID
		result.Events = append(result.Events, event)
		if r != nil && r.sink != nil {
			r.sink(event)
		}
	}
	reject := func(err error) (RuntimeResult, error) {
		emit(LifecycleEvent{Kind: LifecycleRunRejected, Actor: ActorController, Error: err.Error()})
		return result, err
	}

	if r == nil || !r.config.Enabled {
		return reject(ErrRuntimeDisabled)
	}
	if ctx == nil {
		return reject(errors.New("orchestration context is nil"))
	}
	if strings.TrimSpace(runID) == "" {
		return reject(errors.New("orchestration run_id is required"))
	}

	decision = snapshotDecision(decision)
	if err := decision.ValidateForExecution(); err != nil {
		return reject(fmt.Errorf("validate orchestration decision: %w", err))
	}
	leadExecutor, workerExecutors, err := r.preflight(decision.Plan)
	if err != nil {
		return reject(err)
	}
	if ctx.Err() != nil {
		result.State = RuntimeCanceled
		emit(LifecycleEvent{Kind: LifecycleRunCanceled, Actor: ActorController, Lead: decision.Plan.Lead, Error: ctx.Err().Error()})
		return result, ctx.Err()
	}

	result.Lead = LeadExecution{Lead: decision.Plan.Lead}
	result.Primary = newWorkerExecution(WorkerRolePrimary, -1, decision.Plan.Primary, "", 0, 0)
	result.Delegated = make([]WorkerExecution, len(decision.Plan.Delegated))
	for index, delegated := range decision.Plan.Delegated {
		result.Delegated[index] = newWorkerExecution(
			WorkerRoleDelegated,
			index,
			delegated.Route,
			delegated.Scope,
			delegated.MaxTurns,
			delegated.MaxDurationSeconds,
		)
	}

	result.State = RuntimeFailed
	emit(LifecycleEvent{Kind: LifecycleRunStarted, Actor: ActorController, Lead: decision.Plan.Lead})
	emit(LifecycleEvent{Kind: LifecycleLeadStarted, Actor: ActorLead, Lead: decision.Plan.Lead})
	// Give the lead a separate deep snapshot. A lead may inspect its authorized
	// routes, but mutating that request must not alter the runtime-owned plan
	// after exact-executor preflight.
	leadPlan := snapshotDecision(decision).Plan
	leadResult, leadErr := executeLeadSafely(ctx, leadExecutor, LeadRequest{RunID: runID, Plan: leadPlan})
	result.Lead.Result = leadResult
	result.Lead.Err = leadErr
	if ctx.Err() != nil {
		result.State = RuntimeCanceled
		result.Lead.Status = ExecutionCanceled
		if leadErr == nil {
			leadErr = ctx.Err()
			result.Lead.Err = leadErr
		}
		emit(LifecycleEvent{Kind: LifecycleLeadCanceled, Actor: ActorLead, Lead: decision.Plan.Lead, Error: leadErr.Error()})
		emit(LifecycleEvent{Kind: LifecycleRunCanceled, Actor: ActorController, Lead: decision.Plan.Lead, Error: ctx.Err().Error()})
		return result, ctx.Err()
	}
	if leadErr != nil {
		result.Lead.Status = ExecutionFailed
		emit(LifecycleEvent{Kind: LifecycleLeadFailed, Actor: ActorLead, Lead: decision.Plan.Lead, Error: leadErr.Error()})
		emit(LifecycleEvent{Kind: LifecycleRunFailed, Actor: ActorController, Lead: decision.Plan.Lead, Error: leadErr.Error()})
		return result, fmt.Errorf("execute lead %q: %w", decision.Plan.Lead, leadErr)
	}
	result.Lead.Status = ExecutionCompleted
	emit(LifecycleEvent{Kind: LifecycleLeadCompleted, Actor: ActorLead, Lead: decision.Plan.Lead})

	primaryRequest := WorkerRequest{
		RunID:          runID,
		Lead:           decision.Plan.Lead,
		Role:           WorkerRolePrimary,
		DelegatedIndex: -1,
		Route:          decision.Plan.Primary,
		Workdir:        decision.Plan.Workdir,
		LeadOutput:     leadResult.Output,
	}
	emit(workerLifecycleEvent(LifecycleWorkerStarted, decision.Plan.Lead, result.Primary, ""))
	result.Primary = r.executeWorker(ctx, workerExecutors[decision.Plan.Primary.Worker], primaryRequest, result.Primary)
	emit(workerLifecycleEvent(lifecycleForWorker(result.Primary), decision.Plan.Lead, result.Primary, errorText(result.Primary.Err)))
	if result.Primary.Status != ExecutionCompleted {
		if result.Primary.Status == ExecutionCanceled {
			result.State = RuntimeCanceled
			emit(LifecycleEvent{Kind: LifecycleRunCanceled, Actor: ActorController, Lead: decision.Plan.Lead, Error: errorText(result.Primary.Err)})
			return result, result.Primary.Err
		}
		emit(LifecycleEvent{Kind: LifecycleRunFailed, Actor: ActorController, Lead: decision.Plan.Lead, Error: errorText(result.Primary.Err)})
		return result, fmt.Errorf("execute primary worker %q: %w", decision.Plan.Primary.Worker, result.Primary.Err)
	}

	var wait sync.WaitGroup
	for index, delegated := range decision.Plan.Delegated {
		emit(workerLifecycleEvent(LifecycleWorkerStarted, decision.Plan.Lead, result.Delegated[index], ""))
		wait.Add(1)
		go func(index int, delegated DelegatedWorker) {
			defer wait.Done()
			request := WorkerRequest{
				RunID:              runID,
				Lead:               decision.Plan.Lead,
				Role:               WorkerRoleDelegated,
				DelegatedIndex:     index,
				Route:              delegated.Route,
				Workdir:            decision.Plan.Workdir,
				Scope:              delegated.Scope,
				MaxTurns:           delegated.MaxTurns,
				MaxDurationSeconds: delegated.MaxDurationSeconds,
				LeadOutput:         leadResult.Output,
			}
			result.Delegated[index] = r.executeWorker(ctx, workerExecutors[delegated.Route.Worker], request, result.Delegated[index])
		}(index, delegated)
	}
	wait.Wait()

	var delegatedErrors []error
	for _, execution := range result.Delegated {
		emit(workerLifecycleEvent(lifecycleForWorker(execution), decision.Plan.Lead, execution, errorText(execution.Err)))
		if execution.Status != ExecutionCompleted {
			delegatedErrors = append(delegatedErrors, execution.Err)
		}
	}
	if ctx.Err() != nil {
		result.State = RuntimeCanceled
		emit(LifecycleEvent{Kind: LifecycleRunCanceled, Actor: ActorController, Lead: decision.Plan.Lead, Error: ctx.Err().Error()})
		return result, ctx.Err()
	}
	if len(delegatedErrors) > 0 {
		joined := errors.Join(delegatedErrors...)
		emit(LifecycleEvent{Kind: LifecycleRunFailed, Actor: ActorController, Lead: decision.Plan.Lead, Error: joined.Error()})
		return result, joined
	}

	result.State = RuntimeCompleted
	emit(LifecycleEvent{Kind: LifecycleRunCompleted, Actor: ActorController, Lead: decision.Plan.Lead})
	return result, nil
}

func (r *Runtime) preflight(plan RunPlan) (LeadExecutor, map[Worker]WorkerExecutor, error) {
	leadExecutor, ok := r.leads[plan.Lead]
	if !ok || leadExecutor == nil {
		return nil, nil, fmt.Errorf("%w: lead %q", ErrExecutorUnavailable, plan.Lead)
	}
	selected := make(map[Worker]WorkerExecutor, 1+len(plan.Delegated))
	routes := append([]WorkerRoute{plan.Primary}, delegatedRoutes(plan.Delegated)...)
	for _, route := range routes {
		executor, registered := r.workers[route.Worker]
		if !registered || executor == nil {
			return nil, nil, fmt.Errorf("%w: worker %q", ErrExecutorUnavailable, route.Worker)
		}
		selected[route.Worker] = executor
	}
	return leadExecutor, selected, nil
}

func (r *Runtime) executeWorker(parent context.Context, executor WorkerExecutor, request WorkerRequest, execution WorkerExecution) WorkerExecution {
	workerContext := parent
	cancel := func() {}
	if request.Role == WorkerRoleDelegated {
		workerContext, cancel = context.WithTimeout(parent, time.Duration(request.MaxDurationSeconds)*r.durationUnit)
	}
	defer cancel()

	workerResult, workerErr := executeWorkerSafely(workerContext, executor, request)
	execution.Result = workerResult
	execution.Err = workerErr
	switch {
	case parent.Err() != nil:
		execution.Status = ExecutionCanceled
		execution.Err = parent.Err()
	case workerContext.Err() == context.DeadlineExceeded:
		execution.Status = ExecutionTimedOut
		execution.Err = fmt.Errorf("%w: worker %q at delegated index %d", ErrChildTimeout, request.Route.Worker, request.DelegatedIndex)
	case workerErr != nil:
		execution.Status = ExecutionFailed
	case request.Role == WorkerRoleDelegated && workerResult.TurnsUsed > request.MaxTurns:
		execution.Status = ExecutionFailed
		execution.Err = fmt.Errorf("%w: worker %q used %d turns, maximum %d", ErrTurnLimitExceeded, request.Route.Worker, workerResult.TurnsUsed, request.MaxTurns)
	default:
		execution.Status = ExecutionCompleted
	}
	return execution
}

func executeLeadSafely(ctx context.Context, executor LeadExecutor, request LeadRequest) (result LeadResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: lead: %v", ErrExecutorPanic, recovered)
		}
	}()
	return executor.ExecuteLead(ctx, request)
}

func executeWorkerSafely(ctx context.Context, executor WorkerExecutor, request WorkerRequest) (result WorkerResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: worker %q: %v", ErrExecutorPanic, request.Route.Worker, recovered)
		}
	}()
	return executor.ExecuteWorker(ctx, request)
}

func snapshotDecision(decision RoutingDecision) RoutingDecision {
	decision.Plan.Delegated = append([]DelegatedWorker(nil), decision.Plan.Delegated...)
	decision.Plan.Authorization.LeadCapabilities = append([]Capability(nil), decision.Plan.Authorization.LeadCapabilities...)
	decision.Plan.Authorization.AllowedWorkers = append([]Worker(nil), decision.Plan.Authorization.AllowedWorkers...)
	decision.Plan.Approval.Capabilities = append([]Capability(nil), decision.Plan.Approval.Capabilities...)
	return decision
}

func delegatedRoutes(delegated []DelegatedWorker) []WorkerRoute {
	routes := make([]WorkerRoute, len(delegated))
	for index := range delegated {
		routes[index] = delegated[index].Route
	}
	return routes
}

func newWorkerExecution(role WorkerRole, index int, route WorkerRoute, scope string, maxTurns uint16, maxDuration uint32) WorkerExecution {
	return WorkerExecution{
		Role:               role,
		DelegatedIndex:     index,
		Route:              route,
		Scope:              scope,
		MaxTurns:           maxTurns,
		MaxDurationSeconds: maxDuration,
	}
}

func lifecycleForWorker(execution WorkerExecution) LifecycleKind {
	switch execution.Status {
	case ExecutionCompleted:
		return LifecycleWorkerCompleted
	case ExecutionCanceled:
		return LifecycleWorkerCanceled
	case ExecutionTimedOut:
		return LifecycleWorkerTimedOut
	default:
		return LifecycleWorkerFailed
	}
}

func workerLifecycleEvent(kind LifecycleKind, lead LeadHarness, execution WorkerExecution, errText string) LifecycleEvent {
	return LifecycleEvent{
		Kind:               kind,
		Actor:              ActorWorker,
		Lead:               lead,
		Worker:             execution.Route.Worker,
		WorkerRole:         execution.Role,
		DelegatedIndex:     execution.DelegatedIndex,
		Scope:              execution.Scope,
		MaxTurns:           execution.MaxTurns,
		MaxDurationSeconds: execution.MaxDurationSeconds,
		Error:              errText,
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
