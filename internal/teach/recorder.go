// Package teach records an explicitly user-controlled computer demonstration.
//
// It is deliberately transport-agnostic: browser, desktop, VNC, or CDP
// integrations normalize their actions before passing them here. The recorder
// never accepts screenshot bytes and never retains fields from a sensitive or
// paused action.
package teach

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// MaxRecordingDuration is the longest demonstration OpenAgentFleet will retain
	// before automatically stopping and persisting its safe trace.
	MaxRecordingDuration = 10 * time.Minute

	traceVersion  = 1
	maxGoalBytes  = 8 * 1024
	maxFieldBytes = 8 * 1024
)

var (
	// ErrInvalidRoot means trace storage is not a usable, locally-owned root.
	ErrInvalidRoot = errors.New("invalid teach trace root")
	// ErrInvalidID means an identifier is unsafe for use as a trace filename.
	ErrInvalidID = errors.New("invalid teach trace ID")
	// ErrGoalRequired means a teaching session has no meaningful user goal.
	ErrGoalRequired = errors.New("teach task goal is required")
	// ErrInvalidAction means an action cannot be normalized safely.
	ErrInvalidAction = errors.New("invalid teach action")
	// ErrInvalidTransition means an operation is not valid in the current state.
	ErrInvalidTransition = errors.New("invalid teach recorder state transition")
	// ErrRecordingExpired means the ten-minute recording window elapsed.
	ErrRecordingExpired = errors.New("teach recording window expired")
	// ErrNoTrace means no trace exists for the requested operation.
	ErrNoTrace = errors.New("teach trace does not exist")
	// ErrDiscarded means the trace was intentionally removed.
	ErrDiscarded = errors.New("teach trace was discarded")
)

// State is the explicit lifecycle of one user-taught task.
type State string

const (
	StateIdle      State = "idle"
	StateRecording State = "recording"
	StatePaused    State = "paused"
	StateStopped   State = "stopped"
	StateDiscarded State = "discarded"
)

// Surface identifies the computer-control surface that produced an action.
type Surface string

const (
	SurfaceBrowser Surface = "browser"
	SurfaceDesktop Surface = "desktop"
)

// ActionType is intentionally small and shared by browser and desktop
// adapters. New adapters should normalize richer events into these primitives.
type ActionType string

const (
	ActionNavigate ActionType = "navigate"
	ActionClick    ActionType = "click"
	ActionTypeText ActionType = "type"
	ActionPress    ActionType = "press"
	ActionScroll   ActionType = "scroll"
	ActionOpen     ActionType = "open"
)

// Point identifies a pointer coordinate when an adapter has no stable target.
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Scroll is a normalized scroll delta.
type Scroll struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// ScreenshotMetadata intentionally contains only metadata. Image bytes and
// image paths are not accepted by this package.
type ScreenshotMetadata struct {
	ID         string    `json:"id"`
	CapturedAt time.Time `json:"captured_at"`
	Width      int       `json:"width"`
	Height     int       `json:"height"`
	SHA256     string    `json:"sha256,omitempty"`
}

// Action is one adapter-provided, pre-normalization event. Sensitive marks an
// action as containing a secret (for example a password or OTP). Such an
// action is represented only by an omitted/redacted marker in the trace.
type Action struct {
	Surface    Surface
	Type       ActionType
	Target     string
	URL        string
	Text       string
	Key        string
	Point      *Point
	Scroll     *Scroll
	Screenshot *ScreenshotMetadata
	Sensitive  bool
}

// Step is safe, portable input for a later skill-draft workflow. A redacted
// step contains only timing, surface, action category, and redaction markers.
type Step struct {
	Sequence   int                 `json:"sequence"`
	RecordedAt time.Time           `json:"recorded_at"`
	Surface    Surface             `json:"surface"`
	Action     ActionType          `json:"action"`
	Target     string              `json:"target,omitempty"`
	URL        string              `json:"url,omitempty"`
	Text       string              `json:"text,omitempty"`
	Key        string              `json:"key,omitempty"`
	Point      *Point              `json:"point,omitempty"`
	Scroll     *Scroll             `json:"scroll,omitempty"`
	Screenshot *ScreenshotMetadata `json:"screenshot,omitempty"`
	Omitted    bool                `json:"omitted,omitempty"`
	Redacted   bool                `json:"redacted,omitempty"`
}

// Pause marks a human-controlled secret-entry boundary. It deliberately has
// no description, payload, or action text.
type Pause struct {
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// Trace is the durable, safe teaching artifact. It can later be transformed
// into a reviewed SKILL.md draft without exposing raw computer input.
type Trace struct {
	Version    int        `json:"version"`
	ID         string     `json:"id"`
	Goal       string     `json:"goal"`
	State      State      `json:"state"`
	StartedAt  time.Time  `json:"started_at"`
	DeadlineAt time.Time  `json:"deadline_at"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	Expired    bool       `json:"expired,omitempty"`
	Paused     []Pause    `json:"pauses,omitempty"`
	Steps      []Step     `json:"steps"`
	SavedAt    *time.Time `json:"saved_at,omitempty"`
}

// Status is a lightweight snapshot suitable for a timer or computer overlay.
// It contains no action payloads or filesystem path.
type Status struct {
	State      State      `json:"state"`
	ID         string     `json:"id,omitempty"`
	Goal       string     `json:"goal,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	PausedAt   *time.Time `json:"paused_at,omitempty"`
	DeadlineAt *time.Time `json:"deadline_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	Expired    bool       `json:"expired,omitempty"`
	StepCount  int        `json:"step_count"`
	Saved      bool       `json:"saved"`
}

// Config controls one recorder. Root must be a caller-selected local root.
// A nil Now uses time.Now. MaxDuration defaults to ten minutes and can only
// shorten that window, never extend it.
type Config struct {
	Root        string
	Now         func() time.Time
	MaxDuration time.Duration
}

// Recorder is safe for concurrent calls. A Recorder handles exactly one task
// lifecycle; create a fresh recorder after Stop or Discard.
type Recorder struct {
	mu          sync.Mutex
	root        string
	now         func() time.Time
	maxDuration time.Duration

	state      State
	id         string
	goal       string
	startedAt  time.Time
	deadlineAt time.Time
	endedAt    *time.Time
	pausedAt   *time.Time
	pauses     []Pause
	steps      []Step
	expired    bool
	saved      bool
	savedAt    *time.Time
	saveErr    error
}

// New opens or creates a restrictive local trace root.
func New(config Config) (*Recorder, error) {
	root, err := normalizeRoot(config.Root)
	if err != nil {
		return nil, err
	}
	maxDuration := config.MaxDuration
	if maxDuration == 0 {
		maxDuration = MaxRecordingDuration
	}
	if maxDuration <= 0 || maxDuration > MaxRecordingDuration {
		return nil, fmt.Errorf("%w: duration must be greater than zero and at most %s", ErrInvalidAction, MaxRecordingDuration)
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Recorder{
		root:        root,
		now:         now,
		maxDuration: maxDuration,
		state:       StateIdle,
	}, nil
}

// Root returns the resolved caller-provided local storage root.
func (r *Recorder) Root() string {
	return r.root
}

// Start starts the one permitted recording lifecycle with the explicit task
// goal entered by the user.
func (r *Recorder) Start(goal string) (Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state != StateIdle {
		return r.statusLocked(), transitionError("start", r.state)
	}
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return r.statusLocked(), ErrGoalRequired
	}
	if len(goal) > maxGoalBytes {
		return r.statusLocked(), fmt.Errorf("%w: goal exceeds %d bytes", ErrGoalRequired, maxGoalBytes)
	}
	id, err := newTraceID()
	if err != nil {
		return r.statusLocked(), err
	}
	now := r.nowUTC()
	r.state = StateRecording
	r.id = id
	r.goal = goal
	r.startedAt = now
	r.deadlineAt = now.Add(r.maxDuration)
	r.endedAt = nil
	r.pausedAt = nil
	r.pauses = nil
	r.steps = nil
	r.expired = false
	r.saved = false
	r.savedAt = nil
	r.saveErr = nil
	return r.statusLocked(), nil
}

// PauseForSecret starts a strict redaction boundary. While paused, actions may
// be represented only as redacted timeline markers; their original payloads
// are never retained.
func (r *Recorder) PauseForSecret() (Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state != StateRecording {
		return r.statusLocked(), transitionError("pause", r.state)
	}
	now := r.nowUTC()
	if err := r.expireLocked(now); err != nil {
		return r.statusLocked(), err
	}
	r.state = StatePaused
	r.pausedAt = timePointer(now)
	r.pauses = append(r.pauses, Pause{StartedAt: now})
	return r.statusLocked(), nil
}

// Resume ends a secret-entry boundary and resumes normal normalized recording.
func (r *Recorder) Resume() (Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state != StatePaused {
		return r.statusLocked(), transitionError("resume", r.state)
	}
	now := r.nowUTC()
	if err := r.expireLocked(now); err != nil {
		return r.statusLocked(), err
	}
	r.closePauseLocked(now)
	r.state = StateRecording
	return r.statusLocked(), nil
}

// Record normalizes one browser or desktop action. Sensitive actions and all
// actions observed while paused become redacted markers with no copied input.
func (r *Recorder) Record(action Action) (Step, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state != StateRecording && r.state != StatePaused {
		return Step{}, transitionError("record", r.state)
	}
	now := r.nowUTC()
	if err := r.expireLocked(now); err != nil {
		return Step{}, err
	}
	if err := validateActionKind(action); err != nil {
		return Step{}, err
	}
	step := Step{
		Sequence:   len(r.steps) + 1,
		RecordedAt: now,
		Surface:    action.Surface,
		Action:     action.Type,
	}
	if r.state == StatePaused || action.Sensitive {
		step.Omitted = true
		step.Redacted = true
		r.steps = append(r.steps, step)
		return step, nil
	}
	if err := normalizeAction(&step, action); err != nil {
		return Step{}, err
	}
	r.steps = append(r.steps, step)
	return cloneStep(step), nil
}

// Stop finishes the recording and writes one 0600 JSON trace below
// the configured 0700 root. If the timer has elapsed, the trace is still
// finalized safely but ErrRecordingExpired is returned.
func (r *Recorder) Stop() (Trace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state != StateRecording && r.state != StatePaused {
		if r.state == StateStopped && !r.saved {
			err := r.persistLocked(r.nowUTC())
			return r.traceLocked(), err
		}
		return Trace{}, transitionError("stop", r.state)
	}
	now := r.nowUTC()
	if err := r.expireLocked(now); err != nil {
		return r.traceLocked(), err
	}
	r.finishLocked(now, false)
	if err := r.persistLocked(now); err != nil {
		return r.traceLocked(), err
	}
	return r.traceLocked(), nil
}

// Discard securely removes the saved trace, if any, and clears all recorded
// task data from the recorder. It is valid from active and stopped states.
func (r *Recorder) Discard() (Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state == StateIdle || r.state == StateDiscarded {
		return r.statusLocked(), transitionError("discard", r.state)
	}
	if r.saved {
		path, err := r.tracePathForID(r.id)
		if err != nil {
			return r.statusLocked(), err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return r.statusLocked(), fmt.Errorf("discard trace: %w", err)
		}
	}
	r.state = StateDiscarded
	r.id = ""
	r.goal = ""
	r.startedAt = time.Time{}
	r.deadlineAt = time.Time{}
	r.endedAt = nil
	r.pausedAt = nil
	r.pauses = nil
	r.steps = nil
	r.expired = false
	r.saved = false
	r.savedAt = nil
	r.saveErr = nil
	return r.statusLocked(), nil
}

// Status returns a snapshot and enforces the ten-minute limit even when no
// action has arrived. The first poll after expiry returns ErrRecordingExpired.
func (r *Recorder) Status() (Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	err := r.expireLocked(r.nowUTC())
	return r.statusLocked(), err
}

// Trace returns a deep copy of the safe trace. It never returns an in-memory
// reference to a caller's Action payload.
func (r *Recorder) Trace() (Trace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state == StateIdle {
		return Trace{}, ErrNoTrace
	}
	if r.state == StateDiscarded {
		return Trace{}, ErrDiscarded
	}
	err := r.expireLocked(r.nowUTC())
	if r.saveErr != nil {
		err = errors.Join(err, r.saveErr)
	}
	return r.traceLocked(), err
}

func (r *Recorder) expireLocked(now time.Time) error {
	if (r.state != StateRecording && r.state != StatePaused) || now.Before(r.deadlineAt) {
		return nil
	}
	r.finishLocked(now, true)
	if err := r.persistLocked(now); err != nil {
		return errors.Join(ErrRecordingExpired, err)
	}
	return ErrRecordingExpired
}

func (r *Recorder) finishLocked(now time.Time, expired bool) {
	if r.state == StatePaused {
		r.closePauseLocked(now)
	}
	r.state = StateStopped
	r.endedAt = timePointer(now)
	r.expired = expired
}

func (r *Recorder) closePauseLocked(now time.Time) {
	if len(r.pauses) > 0 && r.pauses[len(r.pauses)-1].EndedAt == nil {
		r.pauses[len(r.pauses)-1].EndedAt = timePointer(now)
	}
	r.pausedAt = nil
}

func (r *Recorder) persistLocked(now time.Time) error {
	if r.saved {
		return nil
	}
	if r.state != StateStopped {
		return transitionError("persist", r.state)
	}
	if err := r.validateRootLocked(); err != nil {
		r.saveErr = err
		return err
	}
	path, err := r.tracePathForID(r.id)
	if err != nil {
		r.saveErr = err
		return err
	}
	savedAt := now.UTC()
	trace := r.traceLocked()
	trace.SavedAt = timePointer(savedAt)
	encoded, err := json.MarshalIndent(trace, "", "  ")
	if err != nil {
		r.saveErr = fmt.Errorf("encode trace: %w", err)
		return r.saveErr
	}
	encoded = append(encoded, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		r.saveErr = fmt.Errorf("create trace: %w", err)
		return r.saveErr
	}
	writeErr := func() error {
		if err := file.Chmod(0o600); err != nil {
			return fmt.Errorf("restrict trace permissions: %w", err)
		}
		if _, err := file.Write(encoded); err != nil {
			return fmt.Errorf("write trace: %w", err)
		}
		if err := file.Sync(); err != nil {
			return fmt.Errorf("sync trace: %w", err)
		}
		return nil
	}()
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		if writeErr != nil {
			r.saveErr = writeErr
		} else {
			r.saveErr = fmt.Errorf("close trace: %w", closeErr)
		}
		return r.saveErr
	}
	r.saved = true
	r.savedAt = timePointer(savedAt)
	r.saveErr = nil
	return nil
}

func (r *Recorder) validateRootLocked() error {
	info, err := os.Lstat(r.root)
	if err != nil {
		return fmt.Errorf("%w: inspect root: %v", ErrInvalidRoot, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: root is not a directory", ErrInvalidRoot)
	}
	if err := os.Chmod(r.root, 0o700); err != nil {
		return fmt.Errorf("%w: restrict root permissions: %v", ErrInvalidRoot, err)
	}
	return nil
}

func (r *Recorder) tracePathForID(id string) (string, error) {
	if !validTraceID(id) {
		return "", fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	candidate := filepath.Join(r.root, id+".json")
	relative, err := filepath.Rel(r.root, candidate)
	if err != nil || relative != id+".json" || filepath.Dir(candidate) != r.root {
		return "", fmt.Errorf("%w: trace path escapes root", ErrInvalidID)
	}
	return candidate, nil
}

func (r *Recorder) traceLocked() Trace {
	trace := Trace{
		Version:    traceVersion,
		ID:         r.id,
		Goal:       r.goal,
		State:      r.state,
		StartedAt:  r.startedAt,
		DeadlineAt: r.deadlineAt,
		EndedAt:    cloneTimePointer(r.endedAt),
		Expired:    r.expired,
		Paused:     clonePauses(r.pauses),
		Steps:      cloneSteps(r.steps),
		SavedAt:    cloneTimePointer(r.savedAt),
	}
	return trace
}

func (r *Recorder) statusLocked() Status {
	status := Status{
		State:     r.state,
		ID:        r.id,
		Goal:      r.goal,
		Expired:   r.expired,
		StepCount: len(r.steps),
		Saved:     r.saved,
	}
	if !r.startedAt.IsZero() {
		status.StartedAt = timePointer(r.startedAt)
		status.DeadlineAt = timePointer(r.deadlineAt)
	}
	status.PausedAt = cloneTimePointer(r.pausedAt)
	status.EndedAt = cloneTimePointer(r.endedAt)
	return status
}

func (r *Recorder) nowUTC() time.Time {
	return r.now().UTC()
}

func normalizeRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("%w: empty root", ErrInvalidRoot)
	}
	resolved, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("%w: resolve root: %v", ErrInvalidRoot, err)
	}
	resolved = filepath.Clean(resolved)
	if err := os.MkdirAll(resolved, 0o700); err != nil {
		return "", fmt.Errorf("%w: create root: %v", ErrInvalidRoot, err)
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return "", fmt.Errorf("%w: inspect root: %v", ErrInvalidRoot, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: root is not a directory", ErrInvalidRoot)
	}
	if err := os.Chmod(resolved, 0o700); err != nil {
		return "", fmt.Errorf("%w: restrict root permissions: %v", ErrInvalidRoot, err)
	}
	return resolved, nil
}

func validateActionKind(action Action) error {
	if action.Surface != SurfaceBrowser && action.Surface != SurfaceDesktop {
		return fmt.Errorf("%w: unsupported surface %q", ErrInvalidAction, action.Surface)
	}
	switch action.Type {
	case ActionNavigate:
		if action.Surface != SurfaceBrowser {
			return fmt.Errorf("%w: navigate is browser-only", ErrInvalidAction)
		}
	case ActionClick, ActionTypeText, ActionPress, ActionScroll, ActionOpen:
	default:
		return fmt.Errorf("%w: unsupported action %q", ErrInvalidAction, action.Type)
	}
	return nil
}

func normalizeAction(step *Step, action Action) error {
	switch action.Type {
	case ActionNavigate:
		url := strings.TrimSpace(action.URL)
		if url == "" || len(url) > maxFieldBytes {
			return fmt.Errorf("%w: navigate requires a bounded URL", ErrInvalidAction)
		}
		step.URL = url
	case ActionClick:
		target := strings.TrimSpace(action.Target)
		if len(target) > maxFieldBytes {
			return fmt.Errorf("%w: click target is too large", ErrInvalidAction)
		}
		if target == "" && action.Point == nil {
			return fmt.Errorf("%w: click requires target or point", ErrInvalidAction)
		}
		step.Target = target
		point, err := normalizePoint(action.Point)
		if err != nil {
			return err
		}
		step.Point = point
	case ActionTypeText:
		text := action.Text
		if text == "" || len(text) > maxFieldBytes {
			return fmt.Errorf("%w: type requires bounded text", ErrInvalidAction)
		}
		target := strings.TrimSpace(action.Target)
		if len(target) > maxFieldBytes {
			return fmt.Errorf("%w: type target is too large", ErrInvalidAction)
		}
		step.Target = target
		step.Text = text
	case ActionPress:
		key := strings.TrimSpace(action.Key)
		if key == "" || len(key) > maxFieldBytes {
			return fmt.Errorf("%w: press requires a bounded key", ErrInvalidAction)
		}
		step.Key = key
	case ActionScroll:
		if action.Scroll == nil {
			return fmt.Errorf("%w: scroll requires a delta", ErrInvalidAction)
		}
		step.Scroll = &Scroll{X: action.Scroll.X, Y: action.Scroll.Y}
	case ActionOpen:
		target := strings.TrimSpace(action.Target)
		url := strings.TrimSpace(action.URL)
		if len(target) > maxFieldBytes || len(url) > maxFieldBytes || (target == "" && url == "") {
			return fmt.Errorf("%w: open requires a bounded target or URL", ErrInvalidAction)
		}
		step.Target = target
		step.URL = url
	}
	screenshot, err := normalizeScreenshot(action.Screenshot)
	if err != nil {
		return err
	}
	step.Screenshot = screenshot
	return nil
}

func normalizePoint(point *Point) (*Point, error) {
	if point == nil {
		return nil, nil
	}
	const coordinateLimit = 100_000
	if point.X < -coordinateLimit || point.X > coordinateLimit || point.Y < -coordinateLimit || point.Y > coordinateLimit {
		return nil, fmt.Errorf("%w: point exceeds coordinate bounds", ErrInvalidAction)
	}
	return &Point{X: point.X, Y: point.Y}, nil
}

func normalizeScreenshot(screenshot *ScreenshotMetadata) (*ScreenshotMetadata, error) {
	if screenshot == nil {
		return nil, nil
	}
	if !validMetadataID(screenshot.ID) {
		return nil, fmt.Errorf("%w: unsafe screenshot metadata ID", ErrInvalidAction)
	}
	if screenshot.CapturedAt.IsZero() || screenshot.Width < 0 || screenshot.Height < 0 || screenshot.Width > 100_000 || screenshot.Height > 100_000 {
		return nil, fmt.Errorf("%w: invalid screenshot metadata", ErrInvalidAction)
	}
	if screenshot.SHA256 != "" && !validSHA256(screenshot.SHA256) {
		return nil, fmt.Errorf("%w: invalid screenshot hash", ErrInvalidAction)
	}
	return &ScreenshotMetadata{
		ID:         screenshot.ID,
		CapturedAt: screenshot.CapturedAt.UTC(),
		Width:      screenshot.Width,
		Height:     screenshot.Height,
		SHA256:     strings.ToLower(screenshot.SHA256),
	}, nil
}

func newTraceID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate teach trace ID: %w", err)
	}
	return "teach-" + hex.EncodeToString(bytes[:]), nil
}

func validTraceID(id string) bool {
	if len(id) != len("teach-")+32 || !strings.HasPrefix(id, "teach-") {
		return false
	}
	for _, character := range id[len("teach-"):] {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func validMetadataID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, character := range id {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') && !(character >= 'A' && character <= 'F') {
			return false
		}
	}
	return true
}

func transitionError(operation string, state State) error {
	return fmt.Errorf("%w: %s is not allowed from %s", ErrInvalidTransition, operation, state)
}

func timePointer(value time.Time) *time.Time {
	copy := value.UTC()
	return &copy
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	return timePointer(*value)
}

func clonePauses(pauses []Pause) []Pause {
	if len(pauses) == 0 {
		return nil
	}
	result := make([]Pause, len(pauses))
	for index, pause := range pauses {
		result[index] = Pause{StartedAt: pause.StartedAt, EndedAt: cloneTimePointer(pause.EndedAt)}
	}
	return result
}

func cloneSteps(steps []Step) []Step {
	if len(steps) == 0 {
		return []Step{}
	}
	result := make([]Step, len(steps))
	for index, step := range steps {
		result[index] = cloneStep(step)
	}
	return result
}

func cloneStep(step Step) Step {
	copy := step
	if step.Point != nil {
		copy.Point = &Point{X: step.Point.X, Y: step.Point.Y}
	}
	if step.Scroll != nil {
		copy.Scroll = &Scroll{X: step.Scroll.X, Y: step.Scroll.Y}
	}
	if step.Screenshot != nil {
		copy.Screenshot = &ScreenshotMetadata{
			ID:         step.Screenshot.ID,
			CapturedAt: step.Screenshot.CapturedAt,
			Width:      step.Screenshot.Width,
			Height:     step.Screenshot.Height,
			SHA256:     step.Screenshot.SHA256,
		}
	}
	return copy
}
