package httpapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

func TestProjectsAPIEditAssociationAndArchive(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	agent, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Builder", Title: "Build agent"})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: instance}
	handler := http.HandlerFunc(server.handleProjectRoutes)

	createdResponse := performRequest(handler, http.MethodPost, "/api/projects", `{
		"name":"Release", "brief":"# Release\nShip it.", "agent_ids":["`+agent.Bot.ID+`"]
	}`, "")
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created struct {
		Project domain.Project `json:"project"`
	}
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Project.Version != 1 || created.Project.BriefRevision != 1 {
		t.Fatalf("created project = %#v", created.Project)
	}

	associated := performRequest(handler, http.MethodPut,
		"/api/project-associations/conversation/"+agent.Conversation.ID,
		`{"project_id":"`+created.Project.ID+`"}`, "")
	if associated.Code != http.StatusOK || !strings.Contains(associated.Body.String(), created.Project.ID) {
		t.Fatalf("associate = %d %s", associated.Code, associated.Body.String())
	}
	loadedAssociation := performRequest(handler, http.MethodGet,
		"/api/project-associations/conversation/"+agent.Conversation.ID, "", "")
	if loadedAssociation.Code != http.StatusOK || !strings.Contains(loadedAssociation.Body.String(), `"subject_type":"conversation"`) {
		t.Fatalf("get association = %d %s", loadedAssociation.Code, loadedAssociation.Body.String())
	}
	run, err := instance.CreateRun(t.Context(), agent.Conversation.ID, agent.Bot.ID, "test", "release")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.SnapshotProjectForRun(t.Context(), run.ID, domain.ProjectSubjectConversation, agent.Conversation.ID, agent.Bot.ID); err != nil {
		t.Fatal(err)
	}
	taskSnapshot := performRequest(handler, http.MethodGet, "/api/projects/task-snapshots/"+run.ID, "", "")
	if taskSnapshot.Code != http.StatusOK || !strings.Contains(taskSnapshot.Body.String(), `"brief_revision":1`) {
		t.Fatalf("task snapshot = %d %s", taskSnapshot.Code, taskSnapshot.Body.String())
	}

	stale := performRequest(handler, http.MethodPatch, "/api/projects/"+created.Project.ID, `{
		"name":"Release", "brief":"changed", "agent_ids":["`+agent.Bot.ID+`"], "expected_version":9
	}`, "")
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "version conflict") {
		t.Fatalf("stale edit = %d %s", stale.Code, stale.Body.String())
	}
	edited := performRequest(handler, http.MethodPatch, "/api/projects/"+created.Project.ID, `{
		"name":"Release two", "brief":"changed", "agent_ids":["`+agent.Bot.ID+`"], "expected_version":1
	}`, "")
	if edited.Code != http.StatusOK || !strings.Contains(edited.Body.String(), `"brief_revision":2`) {
		t.Fatalf("edit = %d %s", edited.Code, edited.Body.String())
	}
	detail := performRequest(handler, http.MethodGet, "/api/projects/"+created.Project.ID, "", "")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"brief_revisions":[`) {
		t.Fatalf("detail = %d %s", detail.Code, detail.Body.String())
	}

	archived := performRequest(handler, http.MethodPost, "/api/projects/"+created.Project.ID+"/archive", `{"expected_version":2}`, "")
	if archived.Code != http.StatusOK || !strings.Contains(archived.Body.String(), `"status":"archived"`) {
		t.Fatalf("archive = %d %s", archived.Code, archived.Body.String())
	}
	activeList := performRequest(handler, http.MethodGet, "/api/projects", "", "")
	if activeList.Code != http.StatusOK || strings.Contains(activeList.Body.String(), created.Project.ID) {
		t.Fatalf("active list = %d %s", activeList.Code, activeList.Body.String())
	}
	allList := performRequest(handler, http.MethodGet, "/api/projects?include_archived=true", "", "")
	if allList.Code != http.StatusOK || !strings.Contains(allList.Body.String(), created.Project.ID) {
		t.Fatalf("archived list = %d %s", allList.Code, allList.Body.String())
	}
}

func TestProjectsAPIRejectsUnknownFieldsAndInvalidAssociation(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	server := &Server{Store: instance}
	handler := http.HandlerFunc(server.handleProjectRoutes)

	unknown := performRequest(handler, http.MethodPost, "/api/projects", `{"name":"x","brief":"y","agent_ids":["z"],"extra":true}`, "")
	if unknown.Code != http.StatusBadRequest || !strings.Contains(unknown.Body.String(), "unknown field") {
		t.Fatalf("unknown field = %d %s", unknown.Code, unknown.Body.String())
	}
	invalidType := performRequest(handler, http.MethodGet, "/api/project-associations/agent/id", "", "")
	if invalidType.Code != http.StatusNotFound {
		t.Fatalf("invalid association type = %d %s", invalidType.Code, invalidType.Body.String())
	}
	invalidFilter := performRequest(handler, http.MethodGet, "/api/projects?include_archived=sometimes", "", "")
	if invalidFilter.Code != http.StatusBadRequest {
		t.Fatalf("invalid archive filter = %d %s", invalidFilter.Code, invalidFilter.Body.String())
	}
}
