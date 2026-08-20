package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/skillworkshop"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

func TestSkillInspectAndRoutineAPIRequireEnabledSkill(t *testing.T) {
	root := t.TempDir()
	instance, err := store.Open(filepath.Join(root, "botd.sqlite"))
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
	workshop, err := skillworkshop.New(filepath.Join(root, "workshop"))
	if err != nil {
		t.Fatal(err)
	}
	enabledRoot := filepath.Join(root, "enabled")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	serverValue := &Server{
		Store:             instance,
		Workshop:          workshop,
		EnabledSkillsRoot: enabledRoot,
		HarnessWorkdir:    workspace,
	}
	handler := serverValue.Handler()

	draft, err := workshop.Create(skillworkshop.DraftInput{
		Name:        "Release notes",
		Description: "Summarize merged changes for a release.",
		SourceTask:  "Write weekly notes",
	})
	if err != nil {
		t.Fatal(err)
	}
	inspected := performRequest(handler, http.MethodGet, "/api/skills/"+draft.ID, "", "")
	if inspected.Code != http.StatusOK || !strings.Contains(inspected.Body.String(), `"state":"draft"`) || !strings.Contains(inspected.Body.String(), `"auto_enabled":false`) {
		t.Fatalf("inspect draft = %d %s", inspected.Code, inspected.Body.String())
	}

	nextRun := time.Date(2027, time.October, 25, 9, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	blocked := performRequest(handler, http.MethodPost, "/api/routines", `{
		"bot_id":"`+conversation.BotID+`",
		"name":"Weekly notes",
		"skill_id":"`+draft.ID+`",
		"kind":"cron",
		"cron_expression":"0 9 * * 1",
		"time_zone":"Europe/Vienna",
		"lead_harness":"grok_build",
		"worker":"claude",
		"retry":{"max_attempts":1,"backoff_seconds":0},
		"next_run_at":"`+nextRun+`"
	}`, "")
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "skill is not enabled") {
		t.Fatalf("routine before enable = %d %s", blocked.Code, blocked.Body.String())
	}

	if _, err := workshop.RecordSecurityReview(draft.ID, skillworkshop.SecurityReviewInput{Reviewer: "local-user", Approved: true, Notes: "Bounded local workflow"}); err != nil {
		t.Fatal(err)
	}
	if _, err := workshop.MarkSafeTest(draft.ID, skillworkshop.SafeTestInput{Runner: "safe-fixture", Passed: true, Evidence: "Validated with non-production data"}); err != nil {
		t.Fatal(err)
	}
	if _, err := workshop.Enable(draft.ID, enabledRoot); err != nil {
		t.Fatal(err)
	}

	listed := performRequest(handler, http.MethodGet, "/api/skills", "", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"source":"enabled"`) {
		t.Fatalf("list enabled skills = %d %s", listed.Code, listed.Body.String())
	}

	created := performRequest(handler, http.MethodPost, "/api/routines", `{
		"bot_id":"`+conversation.BotID+`",
		"name":"Weekly notes",
		"skill_id":"`+draft.ID+`",
		"kind":"cron",
		"cron_expression":"0 9 * * 1",
		"time_zone":"Europe/Vienna",
		"lead_harness":"grok_build",
		"worker":"claude",
		"retry":{"max_attempts":1,"backoff_seconds":0},
		"next_run_at":"`+nextRun+`"
	}`, "")
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"status":"disabled"`) || !strings.Contains(created.Body.String(), "skill:"+draft.ID) {
		t.Fatalf("create routine = %d %s", created.Code, created.Body.String())
	}
	var payload struct {
		Routine domain.Routine `json:"routine"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}

	enabled := performRequest(handler, http.MethodPost, "/api/routines/"+payload.Routine.ID+"/enable", "", "")
	if enabled.Code != http.StatusOK || !strings.Contains(enabled.Body.String(), `"status":"enabled"`) {
		t.Fatalf("enable routine = %d %s", enabled.Code, enabled.Body.String())
	}
	listedRoutines := performRequest(handler, http.MethodGet, "/api/routines?status=enabled", "", "")
	if listedRoutines.Code != http.StatusOK || !strings.Contains(listedRoutines.Body.String(), payload.Routine.ID) {
		t.Fatalf("list routines = %d %s", listedRoutines.Code, listedRoutines.Body.String())
	}
}
