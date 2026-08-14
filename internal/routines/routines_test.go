package routines

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

type testClock struct {
	now time.Time
}

func (c *testClock) Now() time.Time { return c.now }

func (c *testClock) Set(now time.Time) { c.now = now }

func mustSchedule(t *testing.T, expression string) Schedule {
	t.Helper()
	schedule, err := ParseSchedule(expression)
	if err != nil {
		t.Fatalf("ParseSchedule(%q): %v", expression, err)
	}
	return schedule
}

func mustRoutine(t *testing.T, manager *Manager, clock *testClock, input CreateInput) Routine {
	t.Helper()
	routine, err := manager.Create(input)
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	if routine.CreatedAt != clock.Now() {
		t.Fatalf("created at %v, want %v", routine.CreatedAt, clock.Now())
	}
	return routine
}

func TestParseScheduleAcceptsOnlyBoundedGrammar(t *testing.T) {
	tests := []struct {
		expression string
		canonical  string
	}{
		{expression: "hourly", canonical: "hourly"},
		{expression: "hourly :15", canonical: "hourly :15"},
		{expression: "hourly at :05", canonical: "hourly :05"},
		{expression: "daily 09:30", canonical: "daily 09:30"},
		{expression: "daily at 09:30", canonical: "daily 09:30"},
		{expression: "weekly monday 09:30", canonical: "weekly mon 09:30"},
		{expression: "weekly fri at 23:59", canonical: "weekly fri 23:59"},
	}
	for _, test := range tests {
		t.Run(test.expression, func(t *testing.T) {
			schedule, err := ParseSchedule(test.expression)
			if err != nil {
				t.Fatalf("ParseSchedule(): %v", err)
			}
			if got := schedule.String(); got != test.canonical {
				t.Fatalf("canonical schedule = %q, want %q", got, test.canonical)
			}
			if err := schedule.Validate(); err != nil {
				t.Fatalf("Validate(): %v", err)
			}
		})
	}

	invalid := []string{
		"",
		"@hourly",
		"*/5 * * * *",
		"monthly 09:30",
		"hourly 15",
		"hourly :60",
		"hourly :5",
		"daily 24:00",
		"daily 9:30",
		"daily 09:30:00",
		"weekly funday 09:30",
		"weekly mon 09:30 extra",
		"weekly mon at",
	}
	for _, expression := range invalid {
		t.Run("reject_"+expression, func(t *testing.T) {
			if _, err := ParseSchedule(expression); !errors.Is(err, ErrInvalidSchedule) {
				t.Fatalf("ParseSchedule(%q) error = %v, want ErrInvalidSchedule", expression, err)
			}
		})
	}
}

func TestNextRunUsesStrictFutureWallClockBounds(t *testing.T) {
	location := time.FixedZone("local", 2*60*60)

	hourly := mustSchedule(t, "hourly :15")
	for _, test := range []struct {
		name      string
		reference time.Time
		want      time.Time
	}{
		{
			name:      "before minute",
			reference: time.Date(2026, time.August, 12, 10, 14, 59, 0, location),
			want:      time.Date(2026, time.August, 12, 10, 15, 0, 0, location),
		},
		{
			name:      "exact minute rolls forward",
			reference: time.Date(2026, time.August, 12, 10, 15, 0, 0, location),
			want:      time.Date(2026, time.August, 12, 11, 15, 0, 0, location),
		},
		{
			name:      "after minute",
			reference: time.Date(2026, time.August, 12, 10, 15, 1, 0, location),
			want:      time.Date(2026, time.August, 12, 11, 15, 0, 0, location),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := hourly.NextAfter(test.reference)
			if err != nil {
				t.Fatalf("NextAfter(): %v", err)
			}
			if !got.Equal(test.want) {
				t.Fatalf("next run = %v, want %v", got, test.want)
			}
			if !got.After(test.reference) {
				t.Fatalf("next run %v is not strictly after %v", got, test.reference)
			}
		})
	}

	daily := mustSchedule(t, "daily 09:30")
	got, err := daily.NextAfter(time.Date(2026, time.August, 12, 10, 0, 0, 0, location))
	if err != nil {
		t.Fatalf("daily NextAfter(): %v", err)
	}
	want := time.Date(2026, time.August, 13, 9, 30, 0, 0, location)
	if !got.Equal(want) {
		t.Fatalf("daily next run = %v, want %v", got, want)
	}

	weekly := mustSchedule(t, "weekly wed 11:00")
	got, err = weekly.NextAfter(time.Date(2026, time.August, 12, 11, 0, 0, 0, location))
	if err != nil {
		t.Fatalf("weekly NextAfter(): %v", err)
	}
	want = time.Date(2026, time.August, 19, 11, 0, 0, 0, location)
	if !got.Equal(want) {
		t.Fatalf("weekly exact-day next run = %v, want %v", got, want)
	}

	clock := &testClock{now: time.Date(2026, time.August, 12, 10, 0, 0, 0, location)}
	fromClock, err := daily.NextRun(clock)
	if err != nil {
		t.Fatalf("NextRun(clock): %v", err)
	}
	wantFromClock := time.Date(2026, time.August, 13, 9, 30, 0, 0, location)
	if !fromClock.Equal(wantFromClock) {
		t.Fatalf("NextRun(clock) = %v, want %v", fromClock, wantFromClock)
	}
	if _, err := daily.NextRun(nil); err == nil {
		t.Fatal("NextRun(nil) unexpectedly succeeded")
	}
}

func TestCreateRequiresSourceAndNormalizesSafetyGate(t *testing.T) {
	location := time.FixedZone("local", 2*60*60)
	clock := &testClock{now: time.Date(2026, time.August, 12, 10, 0, 0, 0, location)}
	manager := NewManager(clock)

	if _, err := manager.Create(CreateInput{
		Name:     "missing source",
		Schedule: mustSchedule(t, "daily 12:00"),
	}); !errors.Is(err, ErrInvalidRoutine) {
		t.Fatalf("Create() error = %v, want ErrInvalidRoutine", err)
	}
	if _, err := manager.Create(CreateInput{
		Name:     "blank source",
		Schedule: mustSchedule(t, "daily 12:00"),
		Source:   SourceReference{ConversationID: "   ", SkillID: "\t"},
	}); !errors.Is(err, ErrInvalidRoutine) {
		t.Fatalf("Create(blank source) error = %v, want ErrInvalidRoutine", err)
	}

	routine := mustRoutine(t, manager, clock, CreateInput{
		Name:       "Review authenticated build",
		Schedule:   mustSchedule(t, "daily 12:00"),
		Mode:       ModeAttended,
		Source:     SourceReference{ConversationID: " conv-42 ", SkillID: "skill-ci"},
		BrowserUse: true,
	})
	if routine.State != StateEnabled {
		t.Fatalf("initial state = %q, want enabled", routine.State)
	}
	if !routine.ApprovalRequired || !routine.RequiresHumanApproval() {
		t.Fatal("attended/browser routine did not receive the enforced approval gate")
	}
	if routine.Source.ConversationID != "conv-42" || routine.Source.SkillID != "skill-ci" {
		t.Fatalf("source was not normalized: %#v", routine.Source)
	}
	wantNext := time.Date(2026, time.August, 12, 12, 0, 0, 0, location)
	if !routine.NextRunAt.Equal(wantNext) {
		t.Fatalf("next run = %v, want %v", routine.NextRunAt, wantNext)
	}
}

func TestBackgroundRunLifecycleAndStateTransitions(t *testing.T) {
	location := time.FixedZone("local", 2*60*60)
	clock := &testClock{now: time.Date(2026, time.August, 12, 8, 0, 0, 0, location)}
	manager := NewManager(clock)
	routine := mustRoutine(t, manager, clock, CreateInput{
		Name:     "Daily report",
		Schedule: mustSchedule(t, "daily 09:00"),
		Source:   SourceReference{SkillID: "daily-report"},
	})
	clock.Set(time.Date(2026, time.August, 12, 10, 0, 0, 0, location))

	due := manager.Due()
	if len(due) != 1 || due[0].Routine.ID != routine.ID {
		t.Fatalf("Due() = %#v, want one due routine", due)
	}
	run, err := manager.RequestRun(routine.ID)
	if err != nil {
		t.Fatalf("RequestRun(): %v", err)
	}
	if run.State != RunReady {
		t.Fatalf("background request state = %q, want ready", run.State)
	}
	if got := manager.Due(); len(got) != 0 {
		t.Fatalf("Due() while a run is claimed = %#v, want empty", got)
	}

	run, err = manager.StartRun(run.ID)
	if err != nil {
		t.Fatalf("StartRun(): %v", err)
	}
	if run.State != RunRunning || run.StartedAt.IsZero() {
		t.Fatalf("started run = %#v", run)
	}
	clock.Set(time.Date(2026, time.August, 12, 10, 2, 0, 0, location))
	run, err = manager.FinishRun(run.ID, OutcomeSucceeded, "report generated")
	if err != nil {
		t.Fatalf("FinishRun(): %v", err)
	}
	if run.State != RunSucceeded || run.FinishedAt.IsZero() {
		t.Fatalf("finished run = %#v", run)
	}
	updated, err := manager.Get(routine.ID)
	if err != nil {
		t.Fatalf("Get(): %v", err)
	}
	if updated.State != StateEnabled || !updated.NextRunAt.Equal(time.Date(2026, time.August, 13, 9, 0, 0, 0, location)) {
		t.Fatalf("updated routine = %#v", updated)
	}

	if err := manager.Pause(routine.ID, "quiet hours"); err != nil {
		t.Fatalf("Pause(): %v", err)
	}
	paused, _ := manager.Get(routine.ID)
	if paused.State != StatePaused {
		t.Fatalf("paused state = %q", paused.State)
	}
	if got := manager.Due(); len(got) != 0 {
		t.Fatalf("Due() while paused = %#v, want empty", got)
	}
	if err := manager.Resume(routine.ID); err != nil {
		t.Fatalf("Resume(): %v", err)
	}
	resumed, _ := manager.Get(routine.ID)
	if resumed.State != StateEnabled || !resumed.NextRunAt.After(clock.Now()) {
		t.Fatalf("resumed routine = %#v", resumed)
	}

	history := manager.History(routine.ID)
	wantEvents := []EventType{
		EventCreated,
		EventDue,
		EventRunReady,
		EventRunStarted,
		EventRunSucceeded,
		EventPaused,
		EventResumed,
	}
	gotEvents := make([]EventType, 0, len(history))
	for _, event := range history {
		gotEvents = append(gotEvents, event.Type)
	}
	if !reflect.DeepEqual(gotEvents, wantEvents) {
		t.Fatalf("event history = %#v, want %#v", gotEvents, wantEvents)
	}
}

func TestApprovalGatePreventsAutoRunForAttendedComputerWork(t *testing.T) {
	location := time.FixedZone("local", 2*60*60)
	clock := &testClock{now: time.Date(2026, time.August, 12, 9, 30, 0, 0, location)}
	manager := NewManager(clock)
	routine := mustRoutine(t, manager, clock, CreateInput{
		Name:       "Open browser and review CI",
		Schedule:   mustSchedule(t, "hourly"),
		Mode:       ModeAttended,
		Source:     SourceReference{ConversationID: "conv-computer"},
		DesktopUse: true,
	})
	clock.Set(time.Date(2026, time.August, 12, 10, 0, 0, 0, location))

	// Inspecting due work is read-only: it neither creates a run nor starts one.
	if due := manager.Due(); len(due) != 1 {
		t.Fatalf("Due() = %#v, want one item", due)
	}
	if runs := manager.ListRuns(routine.ID); len(runs) != 0 {
		t.Fatalf("Due() created runs: %#v", runs)
	}

	run, err := manager.RequestRun(routine.ID)
	if err != nil {
		t.Fatalf("RequestRun(): %v", err)
	}
	if run.State != RunAwaitingApproval {
		t.Fatalf("approval-required run state = %q, want awaiting_approval", run.State)
	}
	blocked, _ := manager.Get(routine.ID)
	if blocked.State != StateNeedsAttention || blocked.AttentionReason == "" {
		t.Fatalf("routine did not stop for attention: %#v", blocked)
	}
	if _, err := manager.StartRun(run.ID); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("StartRun() error = %v, want ErrApprovalRequired", err)
	}
	if _, err := manager.ApproveRun(run.ID, " "); !errors.Is(err, ErrApprovalIDRequired) {
		t.Fatalf("ApproveRun(blank) error = %v, want ErrApprovalIDRequired", err)
	}
	for _, event := range manager.History(routine.ID) {
		if event.Type == EventRunStarted {
			t.Fatal("run started before human approval")
		}
	}

	run, err = manager.ApproveRun(run.ID, "human-approval-42")
	if err != nil {
		t.Fatalf("ApproveRun(): %v", err)
	}
	if run.State != RunReady || run.ApprovalID != "human-approval-42" {
		t.Fatalf("approved run = %#v", run)
	}
	ready, _ := manager.Get(routine.ID)
	if ready.State != StateEnabled {
		t.Fatalf("routine after approval = %q, want enabled", ready.State)
	}
	if _, err := manager.StartRun(run.ID); err != nil {
		t.Fatalf("StartRun(after approval): %v", err)
	}
	clock.Set(clock.Now().Add(2 * time.Minute))
	if _, err := manager.FinishRun(run.ID, OutcomeSucceeded, "human completed handoff"); err != nil {
		t.Fatalf("FinishRun(): %v", err)
	}
}

func TestFailureNeedsAttentionAndRequiresExplicitResolution(t *testing.T) {
	location := time.FixedZone("local", 2*60*60)
	clock := &testClock{now: time.Date(2026, time.August, 12, 9, 30, 0, 0, location)}
	manager := NewManager(clock)
	routine := mustRoutine(t, manager, clock, CreateInput{
		Name:     "Failing background check",
		Schedule: mustSchedule(t, "hourly"),
		Source:   SourceReference{SkillID: "check"},
	})
	clock.Set(time.Date(2026, time.August, 12, 10, 0, 0, 0, location))
	run, err := manager.RequestRun(routine.ID)
	if err != nil {
		t.Fatalf("RequestRun(): %v", err)
	}
	if _, err := manager.StartRun(run.ID); err != nil {
		t.Fatalf("StartRun(): %v", err)
	}
	clock.Set(clock.Now().Add(time.Minute))
	if _, err := manager.FinishRun(run.ID, OutcomeFailed, "CI token expired"); err != nil {
		t.Fatalf("FinishRun(failed): %v", err)
	}

	updated, err := manager.Get(routine.ID)
	if err != nil {
		t.Fatalf("Get(): %v", err)
	}
	if updated.State != StateNeedsAttention || updated.AttentionReason != "CI token expired" {
		t.Fatalf("failed routine = %#v", updated)
	}
	if due := manager.Due(); len(due) != 0 {
		t.Fatalf("failed routine was still due: %#v", due)
	}
	if _, err := manager.RequestRun(routine.ID); !errors.Is(err, ErrNeedsAttention) {
		t.Fatalf("RequestRun(needs attention) error = %v, want ErrNeedsAttention", err)
	}
	if err := manager.ResolveNeedsAttention(routine.ID, "token refreshed"); err != nil {
		t.Fatalf("ResolveNeedsAttention(): %v", err)
	}
	resolved, _ := manager.Get(routine.ID)
	if resolved.State != StateEnabled || resolved.AttentionReason != "" || !resolved.NextRunAt.After(clock.Now()) {
		t.Fatalf("resolved routine = %#v", resolved)
	}

	history := manager.History(routine.ID)
	seenAttention := false
	for _, event := range history {
		if event.Type == EventNeedsAttention && event.Message == "CI token expired" {
			seenAttention = true
		}
	}
	if !seenAttention {
		t.Fatalf("failure did not append a needs-attention event: %#v", history)
	}
}

func TestPauseCancelsUnstartedRunAndDoesNotAutoResumeIt(t *testing.T) {
	clock := &testClock{now: time.Date(2026, time.August, 12, 9, 30, 0, 0, time.UTC)}
	manager := NewManager(clock)
	routine := mustRoutine(t, manager, clock, CreateInput{
		Name:     "Pending work",
		Schedule: mustSchedule(t, "hourly"),
		Source:   SourceReference{ConversationID: "conv-pending"},
	})
	clock.Set(time.Date(2026, time.August, 12, 10, 0, 0, 0, time.UTC))
	run, err := manager.RequestRun(routine.ID)
	if err != nil {
		t.Fatalf("RequestRun(): %v", err)
	}
	if err := manager.Pause(routine.ID, "user paused"); err != nil {
		t.Fatalf("Pause(): %v", err)
	}
	cancelled, err := manager.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun(): %v", err)
	}
	if cancelled.State != RunSkipped {
		t.Fatalf("cancelled run = %#v, want skipped", cancelled)
	}
	if _, err := manager.StartRun(run.ID); !errors.Is(err, ErrRunNotReady) {
		t.Fatalf("StartRun(cancelled) error = %v, want ErrRunNotReady", err)
	}
	if err := manager.Resume(routine.ID); err != nil {
		t.Fatalf("Resume(): %v", err)
	}
	if runs := manager.ListRuns(routine.ID); len(runs) != 1 {
		t.Fatalf("runs after resume = %#v, want only cancelled occurrence", runs)
	}
	if due := manager.Due(); len(due) != 0 {
		t.Fatalf("resume unexpectedly resurrected old run: %#v", due)
	}
}
