package httpapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/events"
	"github.com/robbyczgw-cla/openagentfleet/internal/skillworkshop"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

func TestNewSurfacesRejectEdgeCasesAndKeepIsolation(t *testing.T) {
	root := t.TempDir()
	instance, err := store.Open(filepath.Join(root, "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	workshop, err := skillworkshop.New(filepath.Join(root, "workshop"))
	if err != nil {
		t.Fatal(err)
	}
	seed, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	engineer, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Engineer", Title: "Code", Description: "Repo."})
	if err != nil {
		t.Fatal(err)
	}
	researcher, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Researcher", Title: "Research", Description: "Notes."})
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Outsider", Title: "Other", Description: "Not in group."})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{
		Store:             instance,
		Broker:            events.New(),
		Workshop:          workshop,
		EnabledSkillsRoot: filepath.Join(root, "enabled"),
		HarnessWorkdir:    filepath.Join(root, "workspace"),
		RemoteToken:       "controller",
	}
	handler := server.Handler()

	if got := performRequest(handler, http.MethodPost, "/api/host/status", "{}", "controller"); got.Code != http.StatusNotFound {
		t.Fatalf("POST host/status = %d %s", got.Code, got.Body.String())
	}
	host := performRequest(handler, http.MethodGet, "/api/host/status", "", "controller")
	if host.Code != http.StatusOK || !strings.Contains(host.Body.String(), `"role":"authority"`) {
		t.Fatalf("GET host/status = %d %s", host.Code, host.Body.String())
	}

	missingSkill := performRequest(handler, http.MethodGet, "/api/skills/missing-draft", "", "controller")
	if missingSkill.Code != http.StatusNotFound {
		t.Fatalf("inspect missing skill = %d %s", missingSkill.Code, missingSkill.Body.String())
	}

	unknownGroup := performRequest(handler, http.MethodGet, "/api/groups/grp-missing", "", "controller")
	if unknownGroup.Code != http.StatusNotFound {
		t.Fatalf("missing group = %d %s", unknownGroup.Code, unknownGroup.Body.String())
	}

	created := performRequest(handler, http.MethodPost, "/api/groups", `{"title":"Launch","agent_ids":["`+engineer.Bot.ID+`","`+researcher.Bot.ID+`"]}`, "controller")
	if created.Code != http.StatusCreated {
		t.Fatalf("create group = %d %s", created.Code, created.Body.String())
	}
	var groupPayload struct {
		Group domain.Group `json:"group"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &groupPayload); err != nil {
		t.Fatal(err)
	}
	gid := groupPayload.Group.ID

	empty := performRequest(handler, http.MethodPost, "/api/groups/"+gid+"/messages", `{"content":"   "}`, "controller")
	if empty.Code != http.StatusBadRequest {
		t.Fatalf("empty group content = %d %s", empty.Code, empty.Body.String())
	}
	outsiderMention := performRequest(handler, http.MethodPost, "/api/groups/"+gid+"/messages",
		`{"content":"hello","mention_bot_ids":["`+outsider.Bot.ID+`"]}`, "controller")
	if outsiderMention.Code != http.StatusBadRequest {
		t.Fatalf("outsider mention = %d %s", outsiderMention.Code, outsiderMention.Body.String())
	}
	noMentions := performRequest(handler, http.MethodPost, "/api/groups/"+gid+"/messages",
		`{"content":"status update only"}`, "controller")
	if noMentions.Code != http.StatusAccepted || !strings.Contains(noMentions.Body.String(), `"runs":[]`) {
		t.Fatalf("no mentions = %d %s", noMentions.Code, noMentions.Body.String())
	}
	dup := performRequest(handler, http.MethodPost, "/api/groups/"+gid+"/messages",
		`{"content":"@Engineer twice","mention_bot_ids":["`+engineer.Bot.ID+`","`+engineer.Bot.ID+`"]}`, "controller")
	if dup.Code != http.StatusAccepted {
		t.Fatalf("duplicate mention = %d %s", dup.Code, dup.Body.String())
	}
	var dupPayload struct {
		Runs []domain.GroupRun `json:"runs"`
	}
	if err := json.Unmarshal(dup.Body.Bytes(), &dupPayload); err != nil {
		t.Fatal(err)
	}
	if len(dupPayload.Runs) != 1 || dupPayload.Runs[0].BotID != engineer.Bot.ID || dupPayload.Runs[0].Status != domain.GroupRunStatusQueued {
		t.Fatalf("deduped runs = %#v", dupPayload.Runs)
	}

	canonical, err := instance.ListMessages(t.Context(), engineer.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != 0 {
		t.Fatalf("edge traffic leaked into canonical chat: %#v", canonical)
	}

	nextRun := time.Date(2027, time.October, 25, 9, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	unknownEnable := performRequest(handler, http.MethodPost, "/api/routines/routine-missing/enable", "", "controller")
	if unknownEnable.Code != http.StatusNotFound {
		t.Fatalf("enable missing routine = %d %s", unknownEnable.Code, unknownEnable.Body.String())
	}
	heartbeat := performRequest(handler, http.MethodPost, "/api/routines", `{
		"bot_id":"`+seed.BotID+`",
		"name":"Watch queue",
		"kind":"heartbeat",
		"time_zone":"Europe/Vienna",
		"heartbeat_interval_seconds":30,
		"lead_harness":"grok_build",
		"worker":"claude",
		"retry":{"max_attempts":1,"backoff_seconds":0},
		"next_run_at":"`+nextRun+`"
	}`, "controller")
	if heartbeat.Code != http.StatusCreated || !strings.Contains(heartbeat.Body.String(), `"status":"disabled"`) {
		t.Fatalf("heartbeat create = %d %s", heartbeat.Code, heartbeat.Body.String())
	}
	var heartbeatPayload struct {
		Routine domain.Routine `json:"routine"`
	}
	if err := json.Unmarshal(heartbeat.Body.Bytes(), &heartbeatPayload); err != nil {
		t.Fatal(err)
	}
	blocked := performRequest(handler, http.MethodPost, "/api/routines/"+heartbeatPayload.Routine.ID+"/enable", "", "controller")
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "heartbeat is not opted in") {
		t.Fatalf("heartbeat enable without opt-in = %d %s", blocked.Code, blocked.Body.String())
	}

	run, _, err := instance.CreateRunWithQueuedEvent(t.Context(), seed.ID, seed.BotID, "grok", "coordinate")
	if err != nil {
		t.Fatal(err)
	}
	wrong := performCollabRequest(handler, http.MethodGet, "/api/collaboration/agents", "", "controller", run.ID, "wrong-token")
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong collab token = %d %s", wrong.Code, wrong.Body.String())
	}
	server.bindCollabCapability("collab-token", run.ID)
	listed := performCollabRequest(handler, http.MethodGet, "/api/collaboration/agents", "", "controller", run.ID, "collab-token")
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), seed.BotID) {
		t.Fatalf("collab list should omit self = %d %s", listed.Code, listed.Body.String())
	}
}
