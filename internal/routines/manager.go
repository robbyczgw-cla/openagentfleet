package routines

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrRoutineNotFound    = errors.New("routine not found")
	ErrRunNotFound        = errors.New("routine run not found")
	ErrInvalidRoutine     = errors.New("invalid routine")
	ErrInvalidTransition  = errors.New("invalid routine state transition")
	ErrRoutinePaused      = errors.New("routine is paused")
	ErrNeedsAttention     = errors.New("routine needs attention")
	ErrNotDue             = errors.New("routine is not due")
	ErrRunActive          = errors.New("routine already has an active run")
	ErrRunNotReady        = errors.New("routine run is not ready")
	ErrApprovalRequired   = errors.New("human approval is required")
	ErrApprovalIDRequired = errors.New("human approval id is required")
	ErrRunAlreadyFinished = errors.New("routine run is already finished")
)

// Clock is the only time dependency of the manager. Injecting it keeps
// scheduling deterministic and makes persistence/API integration independent of
// wall-clock globals.
type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

// Manager is an in-memory, concurrency-safe routine core. It owns records and
// lifecycle events only. It deliberately has no executor callback, process
// launcher, HTTP client, or model dependency.
type Manager struct {
	mu sync.RWMutex

	clock Clock

	routines map[string]Routine
	runs     map[string]Run
	active   map[string]string
	history  map[string][]Event

	routineSequence uint64
	runSequence     uint64
	eventSequence   uint64
}

func NewManager(clock Clock) *Manager {
	if clock == nil {
		clock = SystemClock{}
	}
	return &Manager{
		clock:    clock,
		routines: make(map[string]Routine),
		runs:     make(map[string]Run),
		active:   make(map[string]string),
		history:  make(map[string][]Event),
	}
}

func (m *Manager) Create(input CreateInput) (Routine, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Routine{}, fmt.Errorf("%w: name is required", ErrInvalidRoutine)
	}
	if err := input.Schedule.Validate(); err != nil {
		return Routine{}, fmt.Errorf("%w: %v", ErrInvalidRoutine, err)
	}
	source := normalizeSource(input.Source)
	if !source.Valid() {
		return Routine{}, fmt.Errorf("%w: a conversation or skill source is required", ErrInvalidRoutine)
	}

	mode := input.Mode
	if mode == "" {
		mode = ModeBackground
	}
	if mode != ModeBackground && mode != ModeAttended {
		return Routine{}, fmt.Errorf("%w: unsupported execution mode %q", ErrInvalidRoutine, mode)
	}

	now := m.clock.Now()
	nextRun, err := input.Schedule.NextAfter(now)
	if err != nil {
		return Routine{}, fmt.Errorf("%w: %v", ErrInvalidRoutine, err)
	}

	// This normalization is a safety invariant, not a UI convention. A future
	// scheduler cannot accidentally turn attended/browser/desktop work into an
	// unattended run because the caller omitted a boolean.
	approvalRequired := input.ApprovalRequired || mode == ModeAttended || input.BrowserUse || input.DesktopUse

	m.mu.Lock()
	defer m.mu.Unlock()
	m.routineSequence++
	id := fmt.Sprintf("routine-%06d", m.routineSequence)
	routine := Routine{
		ID:               id,
		Name:             name,
		Schedule:         input.Schedule,
		Mode:             mode,
		Source:           source,
		ApprovalRequired: approvalRequired,
		BrowserUse:       input.BrowserUse,
		DesktopUse:       input.DesktopUse,
		State:            StateEnabled,
		NextRunAt:        nextRun,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	m.routines[id] = routine
	m.recordLocked(routine.ID, "", EventCreated, now, "routine created", "", nil)
	return routine, nil
}

func normalizeSource(source SourceReference) SourceReference {
	source.ConversationID = strings.TrimSpace(source.ConversationID)
	source.SkillID = strings.TrimSpace(source.SkillID)
	source.SkillVersion = strings.TrimSpace(source.SkillVersion)
	return source
}

func (m *Manager) Get(id string) (Routine, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	routine, ok := m.routines[id]
	if !ok {
		return Routine{}, ErrRoutineNotFound
	}
	return routine, nil
}

func (m *Manager) List() []Routine {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Routine, 0, len(m.routines))
	for _, routine := range m.routines {
		items = append(items, routine)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (m *Manager) Pause(id, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	routine, err := m.routineLocked(id)
	if err != nil {
		return err
	}
	if routine.State == StatePaused {
		return fmt.Errorf("%w: routine is already paused", ErrInvalidTransition)
	}
	if routine.State == StateNeedsAttention {
		return fmt.Errorf("%w: resolve attention before pausing", ErrInvalidTransition)
	}
	if activeID := m.active[id]; activeID != "" {
		active := m.runs[activeID]
		if active.State == RunRunning {
			return ErrRunActive
		}
		m.cancelPendingLocked(&active, "routine paused")
	}
	now := m.clock.Now()
	routine.State = StatePaused
	routine.UpdatedAt = now
	m.routines[id] = routine
	m.recordLocked(id, "", EventPaused, now, strings.TrimSpace(reason), "", nil)
	return nil
}

func (m *Manager) Resume(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	routine, err := m.routineLocked(id)
	if err != nil {
		return err
	}
	if routine.State != StatePaused {
		return fmt.Errorf("%w: only paused routines can be resumed", ErrInvalidTransition)
	}
	if routine.AttentionReason != "" {
		return ErrNeedsAttention
	}
	now := m.clock.Now()
	next, err := routine.Schedule.NextAfter(now)
	if err != nil {
		return err
	}
	routine.State = StateEnabled
	routine.NextRunAt = next
	routine.UpdatedAt = now
	m.routines[id] = routine
	m.recordLocked(id, "", EventResumed, now, "routine resumed", "", nil)
	return nil
}

func (m *Manager) MarkNeedsAttention(id, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	routine, err := m.routineLocked(id)
	if err != nil {
		return err
	}
	if activeID := m.active[id]; activeID != "" {
		active := m.runs[activeID]
		if active.State == RunRunning {
			return ErrRunActive
		}
		m.cancelPendingLocked(&active, "routine needs attention")
	}
	now := m.clock.Now()
	message := strings.TrimSpace(reason)
	if message == "" {
		message = "manual attention required"
	}
	routine.State = StateNeedsAttention
	routine.AttentionReason = message
	routine.UpdatedAt = now
	m.routines[id] = routine
	m.recordLocked(id, "", EventNeedsAttention, now, message, "", nil)
	return nil
}

func (m *Manager) ResolveNeedsAttention(id, note string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	routine, err := m.routineLocked(id)
	if err != nil {
		return err
	}
	if routine.State != StateNeedsAttention {
		return fmt.Errorf("%w: routine is not waiting for attention", ErrInvalidTransition)
	}
	if activeID := m.active[id]; activeID != "" {
		return ErrRunActive
	}
	now := m.clock.Now()
	next, err := routine.Schedule.NextAfter(now)
	if err != nil {
		return err
	}
	routine.State = StateEnabled
	routine.AttentionReason = ""
	routine.NextRunAt = next
	routine.UpdatedAt = now
	m.routines[id] = routine
	m.recordLocked(id, "", EventAttentionResolved, now, strings.TrimSpace(note), "", nil)
	return nil
}

// Due returns inspectable due records only. It never creates a run, changes a
// state, invokes a callback, or performs any external action.
func (m *Manager) Due() []DueRoutine {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := m.clock.Now()
	items := make([]DueRoutine, 0)
	for id, routine := range m.routines {
		if routine.State != StateEnabled || routine.NextRunAt.After(now) || m.active[id] != "" {
			continue
		}
		items = append(items, DueRoutine{Routine: routine, ScheduledFor: routine.NextRunAt})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ScheduledFor.Equal(items[j].ScheduledFor) {
			return items[i].Routine.ID < items[j].Routine.ID
		}
		return items[i].ScheduledFor.Before(items[j].ScheduledFor)
	})
	return items
}

// RequestRun claims the current due occurrence as data. For approval-required
// work it stops in AwaitingApproval and moves the routine to needs_attention;
// it never starts the task.
func (m *Manager) RequestRun(id string) (Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	routine, err := m.routineLocked(id)
	if err != nil {
		return Run{}, err
	}
	if activeID := m.active[id]; activeID != "" {
		return m.runs[activeID], nil
	}
	switch routine.State {
	case StatePaused:
		return Run{}, ErrRoutinePaused
	case StateNeedsAttention:
		return Run{}, ErrNeedsAttention
	case StateEnabled:
	default:
		return Run{}, ErrInvalidTransition
	}
	now := m.clock.Now()
	if routine.NextRunAt.After(now) {
		return Run{}, ErrNotDue
	}

	m.runSequence++
	run := Run{
		ID:           fmt.Sprintf("run-%06d", m.runSequence),
		RoutineID:    id,
		ScheduledFor: routine.NextRunAt,
		RequestedAt:  now,
		State:        RunReady,
	}
	m.runs[run.ID] = run
	m.active[id] = run.ID
	m.recordLocked(id, run.ID, EventDue, now, "scheduled occurrence claimed", "", nil)

	if routine.RequiresHumanApproval() {
		run.State = RunAwaitingApproval
		m.runs[run.ID] = run
		routine.State = StateNeedsAttention
		routine.AttentionReason = "human approval required before execution"
		routine.UpdatedAt = now
		m.routines[id] = routine
		m.recordLocked(id, run.ID, EventApprovalRequested, now, routine.AttentionReason, "", nil)
		return run, nil
	}

	m.recordLocked(id, run.ID, EventRunReady, now, "run is ready for an external executor", "", nil)
	return run, nil
}

// ApproveRun is the explicit human gate for an approval-required occurrence.
// An opaque approval ID is required so a future API can bind this to its own
// approval record/audit trail.
func (m *Manager) ApproveRun(runID, approvalID string) (Run, error) {
	approvalID = strings.TrimSpace(approvalID)
	if approvalID == "" {
		return Run{}, ErrApprovalIDRequired
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[runID]
	if !ok {
		return Run{}, ErrRunNotFound
	}
	if run.State != RunAwaitingApproval {
		return Run{}, fmt.Errorf("%w: run is not awaiting approval", ErrInvalidTransition)
	}
	routine, err := m.routineLocked(run.RoutineID)
	if err != nil {
		return Run{}, err
	}
	now := m.clock.Now()
	run.State = RunReady
	run.ApprovalID = approvalID
	m.runs[runID] = run
	routine.State = StateEnabled
	routine.AttentionReason = ""
	routine.UpdatedAt = now
	m.routines[routine.ID] = routine
	m.recordLocked(routine.ID, runID, EventApprovalGranted, now, "human approval granted; external executor may start", approvalID, nil)
	m.recordLocked(routine.ID, runID, EventRunReady, now, "approved run is ready for an external executor", approvalID, nil)
	return run, nil
}

// StartRun only advances the data state after all gates are satisfied. It does
// not run commands, call models, use the network, or control a browser/desktop.
func (m *Manager) StartRun(runID string) (Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[runID]
	if !ok {
		return Run{}, ErrRunNotFound
	}
	if run.State == RunAwaitingApproval {
		return Run{}, ErrApprovalRequired
	}
	if run.State != RunReady {
		return Run{}, fmt.Errorf("%w: current state is %s", ErrRunNotReady, run.State)
	}
	routine, err := m.routineLocked(run.RoutineID)
	if err != nil {
		return Run{}, err
	}
	if routine.State == StatePaused {
		return Run{}, ErrRoutinePaused
	}
	if routine.State == StateNeedsAttention {
		return Run{}, ErrNeedsAttention
	}
	if routine.RequiresHumanApproval() && run.ApprovalID == "" {
		return Run{}, ErrApprovalRequired
	}
	now := m.clock.Now()
	run.State = RunRunning
	run.StartedAt = now
	m.runs[runID] = run
	m.recordLocked(routine.ID, runID, EventRunStarted, now, "external executor may perform work", run.ApprovalID, nil)
	return run, nil
}

// FinishRun records an external executor's outcome and computes the next wall-
// clock occurrence. It still performs no execution itself.
func (m *Manager) FinishRun(runID string, outcome RunOutcome, message string) (Run, error) {
	if outcome != OutcomeSucceeded && outcome != OutcomeFailed && outcome != OutcomeSkipped {
		return Run{}, fmt.Errorf("%w: unsupported outcome %q", ErrInvalidTransition, outcome)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[runID]
	if !ok {
		return Run{}, ErrRunNotFound
	}
	if run.State != RunRunning {
		if run.State == RunSucceeded || run.State == RunFailed || run.State == RunSkipped {
			return Run{}, ErrRunAlreadyFinished
		}
		return Run{}, fmt.Errorf("%w: current state is %s", ErrInvalidTransition, run.State)
	}
	routine, err := m.routineLocked(run.RoutineID)
	if err != nil {
		return Run{}, err
	}
	now := m.clock.Now()
	message = strings.TrimSpace(message)
	run.FinishedAt = now
	run.OutcomeReason = message
	switch outcome {
	case OutcomeSucceeded:
		run.State = RunSucceeded
	case OutcomeFailed:
		run.State = RunFailed
	case OutcomeSkipped:
		run.State = RunSkipped
	}
	m.runs[runID] = run
	delete(m.active, routine.ID)

	next, nextErr := routine.Schedule.NextAfter(now)
	if nextErr != nil {
		return Run{}, nextErr
	}
	routine.NextRunAt = next
	routine.UpdatedAt = now
	switch outcome {
	case OutcomeSucceeded, OutcomeSkipped:
		routine.State = StateEnabled
		routine.AttentionReason = ""
	case OutcomeFailed:
		routine.State = StateNeedsAttention
		if message == "" {
			message = "run failed; human attention required"
			run.OutcomeReason = message
			m.runs[runID] = run
		}
		routine.AttentionReason = message
	}
	m.routines[routine.ID] = routine
	eventType := EventRunSucceeded
	if outcome == OutcomeFailed {
		eventType = EventRunFailed
	} else if outcome == OutcomeSkipped {
		eventType = EventRunSkipped
	}
	m.recordLocked(routine.ID, runID, eventType, now, message, run.ApprovalID, nil)
	if outcome == OutcomeFailed {
		m.recordLocked(routine.ID, runID, EventNeedsAttention, now, routine.AttentionReason, run.ApprovalID, nil)
	}
	return run, nil
}

func (m *Manager) GetRun(id string) (Run, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	run, ok := m.runs[id]
	if !ok {
		return Run{}, ErrRunNotFound
	}
	return run, nil
}

func (m *Manager) ListRuns(routineID string) []Run {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]Run, 0)
	for _, run := range m.runs {
		if routineID == "" || run.RoutineID == routineID {
			items = append(items, run)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (m *Manager) History(routineID string) []Event {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := append([]Event(nil), m.history[routineID]...)
	for i := range items {
		items[i].Metadata = copyMetadata(items[i].Metadata)
	}
	return items
}

func (m *Manager) routineLocked(id string) (Routine, error) {
	routine, ok := m.routines[id]
	if !ok {
		return Routine{}, ErrRoutineNotFound
	}
	return routine, nil
}

func (m *Manager) cancelPendingLocked(run *Run, message string) {
	if run.State != RunAwaitingApproval && run.State != RunReady {
		return
	}
	now := m.clock.Now()
	run.State = RunSkipped
	run.FinishedAt = now
	run.OutcomeReason = message
	m.runs[run.ID] = *run
	delete(m.active, run.RoutineID)
	m.recordLocked(run.RoutineID, run.ID, EventPendingRunCancelled, now, message, run.ApprovalID, nil)
}

func (m *Manager) recordLocked(routineID, runID string, eventType EventType, at time.Time, message, approvalID string, metadata map[string]string) {
	m.eventSequence++
	event := Event{
		ID:         fmt.Sprintf("event-%06d", m.eventSequence),
		RoutineID:  routineID,
		RunID:      runID,
		Type:       eventType,
		At:         at,
		Message:    strings.TrimSpace(message),
		ApprovalID: strings.TrimSpace(approvalID),
		Metadata:   copyMetadata(metadata),
	}
	m.history[routineID] = append(m.history[routineID], event)
}

func copyMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	copyOf := make(map[string]string, len(metadata))
	for key, value := range metadata {
		copyOf[key] = value
	}
	return copyOf
}
