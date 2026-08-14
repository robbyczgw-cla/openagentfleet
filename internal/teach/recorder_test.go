package teach

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	now time.Time
}

func (clock *fakeClock) Now() time.Time {
	return clock.now
}

func newTestRecorder(t *testing.T, clock *fakeClock) *Recorder {
	t.Helper()
	recorder, err := New(Config{
		Root: filepath.Join(t.TempDir(), "teach-traces"),
		Now:  clock.Now,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return recorder
}

func TestRecorderLifecyclePersistsSafeNormalizedTrace(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, time.August, 12, 10, 0, 0, 0, time.FixedZone("CEST", 2*60*60))}
	recorder := newTestRecorder(t, clock)

	status, err := recorder.Start("Open the failed CI job and download its artifact")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if status.State != StateRecording || status.ID == "" || !status.DeadlineAt.Equal(clock.now.UTC().Add(MaxRecordingDuration)) {
		t.Fatalf("unexpected start status: %#v", status)
	}
	clock.now = clock.now.Add(time.Second)
	_, err = recorder.Record(Action{
		Surface: SurfaceBrowser,
		Type:    ActionNavigate,
		URL:     "https://github.com/acme/repo/actions/runs/42",
		Screenshot: &ScreenshotMetadata{
			ID:         "browser-0001",
			CapturedAt: clock.now,
			Width:      1440,
			Height:     900,
		},
	})
	if err != nil {
		t.Fatalf("Record(navigate) error = %v", err)
	}
	clock.now = clock.now.Add(time.Second)
	_, err = recorder.Record(Action{Surface: SurfaceDesktop, Type: ActionOpen, Target: "terminal"})
	if err != nil {
		t.Fatalf("Record(open) error = %v", err)
	}

	trace, err := recorder.Stop()
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if trace.State != StateStopped || trace.ID != status.ID || len(trace.Steps) != 2 || trace.SavedAt == nil || !trace.SavedAt.Equal(clock.now) {
		t.Fatalf("unexpected trace: %#v", trace)
	}
	if trace.Steps[0].Screenshot == nil || trace.Steps[0].Screenshot.ID != "browser-0001" {
		t.Fatalf("screenshot metadata was not preserved: %#v", trace.Steps[0])
	}

	path, err := recorder.tracePathForID(trace.ID)
	if err != nil {
		t.Fatalf("tracePathForID() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(trace) error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("trace mode = %o, want 600", got)
	}
	rootInfo, err := os.Stat(recorder.Root())
	if err != nil {
		t.Fatalf("Stat(root) error = %v", err)
	}
	if got := rootInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("root mode = %o, want 700", got)
	}

	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(trace) error = %v", err)
	}
	var stored Trace
	if err := json.Unmarshal(encoded, &stored); err != nil {
		t.Fatalf("stored trace JSON is invalid: %v", err)
	}
	if stored.Goal != trace.Goal || stored.State != StateStopped || len(stored.Steps) != 2 {
		t.Fatalf("stored trace mismatch: %#v", stored)
	}
}

func TestSensitiveAndPausedActionsNeverRetainTypedText(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, time.August, 12, 11, 0, 0, 0, time.UTC)}
	recorder := newTestRecorder(t, clock)
	if _, err := recorder.Start("Sign in, then collect the build status"); err != nil {
		t.Fatal(err)
	}

	secret := "correct-horse-battery-staple"
	step, err := recorder.Record(Action{
		Surface:    SurfaceBrowser,
		Type:       ActionTypeText,
		Target:     "password-input-" + secret,
		Text:       secret,
		Sensitive:  true,
		Screenshot: &ScreenshotMetadata{ID: "secret-screen", CapturedAt: clock.now, Width: 100, Height: 100},
	})
	if err != nil {
		t.Fatalf("Record(sensitive) error = %v", err)
	}
	if !step.Redacted || !step.Omitted || step.Text != "" || step.Target != "" || step.Screenshot != nil {
		t.Fatalf("sensitive action retained data: %#v", step)
	}

	if _, err := recorder.PauseForSecret(); err != nil {
		t.Fatalf("PauseForSecret() error = %v", err)
	}
	pausedSecret := "one-time-code-991122"
	step, err = recorder.Record(Action{
		Surface:    SurfaceDesktop,
		Type:       ActionTypeText,
		Target:     "otp-" + pausedSecret,
		Text:       pausedSecret,
		Screenshot: &ScreenshotMetadata{ID: "paused-screen", CapturedAt: clock.now, Width: 100, Height: 100},
	})
	if err != nil {
		t.Fatalf("Record(paused) error = %v", err)
	}
	if !step.Redacted || !step.Omitted || step.Text != "" || step.Target != "" || step.Screenshot != nil {
		t.Fatalf("paused action retained data: %#v", step)
	}
	if _, err := recorder.Resume(); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	trace, err := recorder.Stop()
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if len(trace.Paused) != 1 || trace.Paused[0].EndedAt == nil {
		t.Fatalf("pause boundary was not recorded safely: %#v", trace.Paused)
	}

	path, _ := recorder.tracePathForID(trace.ID)
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secret, pausedSecret, "password-input", "otp-", "secret-screen", "paused-screen"} {
		if strings.Contains(string(stored), forbidden) {
			t.Fatalf("stored trace leaked %q: %s", forbidden, stored)
		}
	}
}

func TestExpiryFinalizesTraceAndRejectsLaterTransitions(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)}
	recorder := newTestRecorder(t, clock)
	if _, err := recorder.Start("Watch the deployment until it completes"); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(MaxRecordingDuration)
	if _, err := recorder.Record(Action{Surface: SurfaceBrowser, Type: ActionClick, Target: "continue"}); !errors.Is(err, ErrRecordingExpired) {
		t.Fatalf("Record() error = %v, want ErrRecordingExpired", err)
	}
	status, err := recorder.Status()
	if err != nil {
		t.Fatalf("Status() after expiry error = %v", err)
	}
	if status.State != StateStopped || !status.Expired || !status.Saved {
		t.Fatalf("expiry status = %#v", status)
	}
	if _, err := recorder.Resume(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Resume() after expiry error = %v, want ErrInvalidTransition", err)
	}
	trace, err := recorder.Trace()
	if err != nil {
		t.Fatalf("Trace() error = %v", err)
	}
	if !trace.Expired || trace.EndedAt == nil || !trace.EndedAt.Equal(clock.now) || len(trace.Steps) != 0 {
		t.Fatalf("expired trace = %#v", trace)
	}
	path, _ := recorder.tracePathForID(trace.ID)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expired trace was not persisted: %v", err)
	}
}

func TestTransitionsAndDiscard(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, time.August, 12, 13, 0, 0, 0, time.UTC)}
	recorder := newTestRecorder(t, clock)
	if _, err := recorder.Trace(); !errors.Is(err, ErrNoTrace) {
		t.Fatalf("Trace() idle error = %v, want ErrNoTrace", err)
	}
	if _, err := recorder.PauseForSecret(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("PauseForSecret() idle error = %v", err)
	}
	if _, err := recorder.Start(" "); !errors.Is(err, ErrGoalRequired) {
		t.Fatalf("Start(empty) error = %v", err)
	}
	if _, err := recorder.Start("Download a release artifact"); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Start("second task"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Start() twice error = %v", err)
	}
	trace, err := recorder.Stop()
	if err != nil {
		t.Fatal(err)
	}
	path, _ := recorder.tracePathForID(trace.ID)
	if _, err := recorder.Discard(); err != nil {
		t.Fatalf("Discard() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discarded path stat error = %v, want not exist", err)
	}
	status, err := recorder.Status()
	if err != nil || status.State != StateDiscarded || status.ID != "" || status.Goal != "" || status.StepCount != 0 {
		t.Fatalf("discard status = %#v, %v", status, err)
	}
	if _, err := recorder.Trace(); !errors.Is(err, ErrDiscarded) {
		t.Fatalf("Trace() discarded error = %v", err)
	}
}

func TestConcurrentRecordingProducesOneSafeSequence(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, time.August, 12, 14, 0, 0, 0, time.UTC)}
	recorder := newTestRecorder(t, clock)
	if _, err := recorder.Start("Collect every visible test result"); err != nil {
		t.Fatal(err)
	}

	const workers = 48
	errorsByWorker := make(chan error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			_, err := recorder.Record(Action{Surface: SurfaceBrowser, Type: ActionClick, Target: fmt.Sprintf("row-%d", index)})
			errorsByWorker <- err
		}(index)
	}
	group.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Fatalf("concurrent Record() error = %v", err)
		}
	}
	trace, err := recorder.Stop()
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Steps) != workers {
		t.Fatalf("step count = %d, want %d", len(trace.Steps), workers)
	}
	sequences := make([]int, 0, workers)
	for _, step := range trace.Steps {
		sequences = append(sequences, step.Sequence)
	}
	sort.Ints(sequences)
	for index, sequence := range sequences {
		if sequence != index+1 {
			t.Fatalf("sequences = %v, want contiguous 1..%d", sequences, workers)
		}
	}
}

func TestPathValidationAndDurationBound(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, time.August, 12, 15, 0, 0, 0, time.UTC)}
	root := filepath.Join(t.TempDir(), "teach-traces")
	if _, err := New(Config{Root: root, Now: clock.Now, MaxDuration: MaxRecordingDuration + time.Nanosecond}); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("New(long duration) error = %v", err)
	}
	recorder, err := New(Config{Root: root, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../escape", "teach-../escape", "teach-0123456789abcdef0123456789abcde/", "teach-0123456789abcdef0123456789abcdef.json"} {
		if _, err := recorder.tracePathForID(id); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("tracePathForID(%q) error = %v, want ErrInvalidID", id, err)
		}
	}
	if _, err := New(Config{Root: "", Now: clock.Now}); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("New(empty root) error = %v", err)
	}
}
