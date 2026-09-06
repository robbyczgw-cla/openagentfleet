package httpapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

func TestRoutinePreviewCountsAndRejections(t *testing.T) {
	server, _, _ := openRoutineWorkspace(t)
	handler := server.Handler()

	defaulted := decodeRoutinePreview(t, handler, `{"cron_expression":"0 9 * * *","time_zone":"UTC"}`)
	if len(defaulted.Occurrences) != defaultRoutinePreviewOccurrences {
		t.Fatalf("default occurrence count = %d, want %d", len(defaulted.Occurrences), defaultRoutinePreviewOccurrences)
	}
	if defaulted.TimeZone != "UTC" {
		t.Fatalf("echoed time zone = %q", defaulted.TimeZone)
	}

	capped := decodeRoutinePreview(t, handler, `{"cron_expression":"0 9 * * *","time_zone":"UTC","count":32}`)
	if len(capped.Occurrences) != maxRoutinePreviewOccurrences {
		t.Fatalf("capped occurrence count = %d", len(capped.Occurrences))
	}

	// Preview computes times only. Nothing about it may create a routine, so a
	// rejected request must never leave state behind either.
	rejections := []struct {
		name string
		body string
	}{
		{name: "count above cap", body: `{"cron_expression":"0 9 * * *","time_zone":"UTC","count":33}`},
		{name: "negative count", body: `{"cron_expression":"0 9 * * *","time_zone":"UTC","count":-1}`},
		{name: "short cron", body: `{"cron_expression":"0 9 * *","time_zone":"UTC"}`},
		{name: "unparsable cron field", body: `{"cron_expression":"0 99 * * *","time_zone":"UTC"}`},
		{name: "unknown zone", body: `{"cron_expression":"0 9 * * *","time_zone":"Mars/Olympus"}`},
		{name: "missing zone", body: `{"cron_expression":"0 9 * * *","time_zone":""}`},
		{name: "bad after", body: `{"cron_expression":"0 9 * * *","time_zone":"UTC","after":"tomorrow"}`},
		{name: "unknown field", body: `{"cron_expression":"0 9 * * *","time_zone":"UTC","enabled":true}`},
		{name: "empty body", body: ``},
	}
	for _, testCase := range rejections {
		response := performRequest(handler, http.MethodPost, "/api/routines/preview", testCase.body, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s, want 400", testCase.name, response.Code, response.Body.String())
		}
	}

	listed := performRequest(handler, http.MethodGet, "/api/routines", "", "")
	if !strings.Contains(listed.Body.String(), `"routines":[]`) {
		t.Fatalf("preview created routines: %s", listed.Body.String())
	}
}

func TestRoutinePreviewFollowsZoneAndWeekdayRules(t *testing.T) {
	server, _, _ := openRoutineWorkspace(t)
	handler := server.Handler()
	vienna, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Fatal(err)
	}

	// 09:00 local across the European spring-forward. The wall clock stays at
	// 09:00 while the UTC instant moves back an hour, which is exactly what the
	// calendar has to render correctly.
	daily := decodeRoutinePreview(t, handler, `{
		"cron_expression":"0 9 * * *",
		"time_zone":"Europe/Vienna",
		"after":"2027-03-26T12:00:00Z",
		"count":4
	}`)
	for index, occurrence := range daily.Occurrences {
		parsed, parseErr := time.Parse(time.RFC3339, occurrence)
		if parseErr != nil {
			t.Fatalf("occurrence %d = %q: %v", index, occurrence, parseErr)
		}
		if local := parsed.In(vienna); local.Hour() != 9 || local.Minute() != 0 {
			t.Fatalf("occurrence %d local time = %s, want 09:00 Vienna", index, local)
		}
	}
	first, _ := time.Parse(time.RFC3339, daily.Occurrences[0])
	last, _ := time.Parse(time.RFC3339, daily.Occurrences[3])
	if first.UTC().Hour() != 8 || last.UTC().Hour() != 7 {
		t.Fatalf("daylight saving shift not applied: %s .. %s", daily.Occurrences[0], daily.Occurrences[3])
	}

	weekdays := decodeRoutinePreview(t, handler, `{
		"cron_expression":"30 6 * * 1-5",
		"time_zone":"Europe/Vienna",
		"after":"2027-06-04T12:00:00Z",
		"count":6
	}`)
	previous := time.Time{}
	for index, occurrence := range weekdays.Occurrences {
		parsed, parseErr := time.Parse(time.RFC3339, occurrence)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		local := parsed.In(vienna)
		if local.Weekday() == time.Saturday || local.Weekday() == time.Sunday {
			t.Fatalf("weekday schedule produced %s", local)
		}
		if local.Hour() != 6 || local.Minute() != 30 {
			t.Fatalf("occurrence %d local time = %s, want 06:30", index, local)
		}
		if !previous.IsZero() && !parsed.After(previous) {
			t.Fatalf("occurrences are not strictly increasing at %d: %v", index, weekdays.Occurrences)
		}
		previous = parsed
	}
}

func TestRoutineEditRequiresPauseAndClearsStaleDueTime(t *testing.T) {
	server, instance, botID := openRoutineWorkspace(t)
	handler := server.Handler()
	now := time.Date(2027, time.March, 4, 11, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }

	routine := createEnabledRoutine(t, instance, botID, now.Add(time.Hour), domain.RoutineApprovalOnRisk)
	edit := `{
		"name":"Morning standup notes",
		"description":"Summarize yesterday's merged work.",
		"cron_expression":"30 6 * * 1-5",
		"time_zone":"Europe/Vienna"
	}`

	// An enabled routine can be claimed by the scheduler at any moment, so the
	// workspace must pause it before rewriting what it runs.
	blocked := performRequest(handler, http.MethodPatch, "/api/routines/"+routine.ID, edit, "")
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "paused or disabled") {
		t.Fatalf("edit while enabled = %d %s", blocked.Code, blocked.Body.String())
	}

	if _, err := instance.PauseRoutine(t.Context(), routine.ID, "editing"); err != nil {
		t.Fatal(err)
	}
	applied := performRequest(handler, http.MethodPatch, "/api/routines/"+routine.ID, edit, "")
	if applied.Code != http.StatusOK {
		t.Fatalf("edit while paused = %d %s", applied.Code, applied.Body.String())
	}
	updated := decodeRoutineEnvelope(t, applied.Body.Bytes())
	if updated.Name != "Morning standup notes" || updated.CronExpression != "30 6 * * 1-5" || updated.TimeZone != "Europe/Vienna" {
		t.Fatalf("edited routine = %#v", updated)
	}
	if updated.Status != domain.RoutineStatusPaused {
		t.Fatalf("edit changed lifecycle to %q", updated.Status)
	}
	// The stored due time belonged to the previous cadence. Leaving it in place
	// would fire the edited routine on the old schedule exactly once.
	if updated.NextRunAt != "" || updated.OccurrenceKey != "" {
		t.Fatalf("stale due time survived the edit: %#v", updated)
	}

	history := performRequest(handler, http.MethodGet, "/api/routines/"+routine.ID+"/history", "", "")
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), "routine.updated") {
		t.Fatalf("history after edit = %d %s", history.Code, history.Body.String())
	}

	// Enabling recomputes from the edited expression rather than reusing the
	// cleared value. 06:30 Vienna on the 4th has already passed at 12:00 local,
	// so the next weekday occurrence is Friday the 5th at 05:30 UTC.
	resumed := performRequest(handler, http.MethodPost, "/api/routines/"+routine.ID+"/enable", "", "")
	if resumed.Code != http.StatusOK {
		t.Fatalf("enable after edit = %d %s", resumed.Code, resumed.Body.String())
	}
	enabled := decodeRoutineEnvelope(t, resumed.Body.Bytes())
	nextRun, err := time.Parse(time.RFC3339Nano, enabled.NextRunAt)
	if err != nil {
		t.Fatalf("next run after edit = %q: %v", enabled.NextRunAt, err)
	}
	if !nextRun.Equal(time.Date(2027, time.March, 5, 5, 30, 0, 0, time.UTC)) {
		t.Fatalf("next run = %s, want 2027-03-05T05:30:00Z", nextRun)
	}
}

func TestRoutineEditRejectsBadRequests(t *testing.T) {
	server, instance, botID := openRoutineWorkspace(t)
	handler := server.Handler()
	now := time.Date(2027, time.March, 4, 11, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }

	routine := createEnabledRoutine(t, instance, botID, now.Add(time.Hour), domain.RoutineApprovalOnRisk)
	if _, err := instance.PauseRoutine(t.Context(), routine.ID, "editing"); err != nil {
		t.Fatal(err)
	}

	rejections := []struct {
		name   string
		path   string
		body   string
		status int
	}{
		{
			name:   "unknown routine",
			path:   "/api/routines/routine-missing",
			body:   `{"name":"x","description":"","cron_expression":"0 9 * * *","time_zone":"UTC"}`,
			status: http.StatusNotFound,
		},
		{
			name:   "invalid cron",
			path:   "/api/routines/" + routine.ID,
			body:   `{"name":"x","description":"","cron_expression":"0 9 * *","time_zone":"UTC"}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "invalid zone",
			path:   "/api/routines/" + routine.ID,
			body:   `{"name":"x","description":"","cron_expression":"0 9 * * *","time_zone":"Mars/Olympus"}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "blank name",
			path:   "/api/routines/" + routine.ID,
			body:   `{"name":"   ","description":"","cron_expression":"0 9 * * *","time_zone":"UTC"}`,
			status: http.StatusBadRequest,
		},
		{
			// The edit path must not become a way to re-point a routine at a
			// different agent, harness or approval policy.
			name:   "reassigning the owner",
			path:   "/api/routines/" + routine.ID,
			body:   `{"name":"x","description":"","cron_expression":"0 9 * * *","time_zone":"UTC","bot_id":"other"}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "widening approval policy",
			path:   "/api/routines/" + routine.ID,
			body:   `{"name":"x","description":"","cron_expression":"0 9 * * *","time_zone":"UTC","approval_policy":"never"}`,
			status: http.StatusBadRequest,
		},
	}
	for _, testCase := range rejections {
		response := performRequest(handler, http.MethodPatch, testCase.path, testCase.body, "")
		if response.Code != testCase.status {
			t.Fatalf("%s = %d %s, want %d", testCase.name, response.Code, response.Body.String(), testCase.status)
		}
	}

	unchanged, err := instance.GetRoutine(t.Context(), routine.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Name != routine.Name || unchanged.BotID != routine.BotID || unchanged.ApprovalPolicy != routine.ApprovalPolicy {
		t.Fatalf("rejected edits mutated the routine: %#v", unchanged)
	}
}

func TestRoutineEditRefusesWhileTestRunHoldsLease(t *testing.T) {
	server, instance, botID := openRoutineWorkspace(t)
	handler := server.Handler()
	now := time.Date(2027, time.March, 4, 11, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }

	routine := createEnabledRoutine(t, instance, botID, now.Add(time.Hour), domain.RoutineApprovalOnRisk)
	if _, err := instance.PauseRoutine(t.Context(), routine.ID, "editing"); err != nil {
		t.Fatal(err)
	}
	// A test run is the one way a paused routine still owns an in-flight
	// occurrence. Editing under it would redefine work already running.
	claimed, err := instance.ClaimTestRoutineRun(t.Context(), domain.RoutineClaim{
		RoutineID:      routine.ID,
		LeaseOwner:     "botd-test",
		LeaseDuration:  30 * time.Second,
		IdempotencyKey: id.New("claim"),
		Now:            now,
	})
	if err != nil {
		t.Fatal(err)
	}
	blocked := performRequest(handler, http.MethodPatch, "/api/routines/"+routine.ID,
		`{"name":"x","description":"","cron_expression":"0 9 * * *","time_zone":"UTC"}`, "")
	if blocked.Code != http.StatusConflict {
		t.Fatalf("edit during test run = %d %s", blocked.Code, blocked.Body.String())
	}

	if _, err := instance.FinishRoutineRun(t.Context(), domain.RoutineFinish{
		RunID:      claimed.ID,
		LeaseOwner: claimed.LeaseOwner,
		LeaseToken: claimed.LeaseToken,
		State:      domain.RoutineLedgerCompleted,
		Now:        now.Add(10 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	allowed := performRequest(handler, http.MethodPatch, "/api/routines/"+routine.ID,
		`{"name":"Renamed","description":"","cron_expression":"0 9 * * *","time_zone":"UTC"}`, "")
	if allowed.Code != http.StatusOK {
		t.Fatalf("edit after test run finished = %d %s", allowed.Code, allowed.Body.String())
	}
}

type routinePreviewResponse struct {
	Occurrences []string `json:"occurrences"`
	TimeZone    string   `json:"time_zone"`
}

func decodeRoutinePreview(t *testing.T, handler http.Handler, body string) routinePreviewResponse {
	t.Helper()
	response := performRequest(handler, http.MethodPost, "/api/routines/preview", body, "")
	if response.Code != http.StatusOK {
		t.Fatalf("preview = %d %s", response.Code, response.Body.String())
	}
	var decoded routinePreviewResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	return decoded
}

func decodeRoutineEnvelope(t *testing.T, body []byte) domain.Routine {
	t.Helper()
	var decoded struct {
		Routine domain.Routine `json:"routine"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode routine: %v", err)
	}
	return decoded.Routine
}

func openRoutineWorkspace(t *testing.T) (*Server, *store.Store, string) {
	t.Helper()
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Store: instance, routineLeaseOwner: "botd-test"}, instance, conversation.BotID
}
