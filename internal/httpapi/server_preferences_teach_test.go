package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/compute"
	"github.com/robbyczgw-cla/openagentfleet/internal/integrations"
	"github.com/robbyczgw-cla/openagentfleet/internal/preferences"
	"github.com/robbyczgw-cla/openagentfleet/internal/secrethandoff"
	"github.com/robbyczgw-cla/openagentfleet/internal/skillworkshop"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
	"github.com/robbyczgw-cla/openagentfleet/internal/teach"
)

func TestPreferencesPersistStrictPatchAndBootstrap(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "botd.sqlite")
	instance, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	server := (&Server{Store: instance, HarnessWorkdir: t.TempDir()}).Handler()

	response := performRequest(server, http.MethodGet, "/api/preferences", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("preferences GET = %d, body = %s", response.Code, response.Body.String())
	}
	var initial preferences.Preferences
	if err := json.NewDecoder(response.Body).Decode(&initial); err != nil {
		t.Fatal(err)
	}
	if initial != preferences.Defaults() {
		t.Fatalf("initial preferences = %#v", initial)
	}

	response = performRequest(server, http.MethodPatch, "/api/preferences", `{"appearance":{"theme":"dark"},"usage":{"default_worker":"claude"}}`, "")
	if response.Code != http.StatusOK {
		t.Fatalf("preferences PATCH = %d, body = %s", response.Code, response.Body.String())
	}
	response = performRequest(server, http.MethodPatch, "/api/preferences", `{"computer":{"auto_takeover":true}}`, "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unsafe preferences PATCH = %d, body = %s", response.Code, response.Body.String())
	}
	response = performRequest(server, http.MethodPatch, "/api/preferences", `{"unknown":true}`, "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown preferences PATCH = %d, body = %s", response.Code, response.Body.String())
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	stored, err := reopened.GetPreferences(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Appearance.Theme != preferences.ThemeDark || stored.Usage.DefaultWorker != preferences.ProviderClaude || stored.Computer.AutoTakeover {
		t.Fatalf("persisted preferences = %#v", stored)
	}
	bootstrapServer := (&Server{Store: reopened, HarnessWorkdir: t.TempDir()}).Handler()
	response = performRequest(bootstrapServer, http.MethodGet, "/api/bootstrap", "", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"preferences"`) || !strings.Contains(response.Body.String(), `"theme":"dark"`) {
		t.Fatalf("bootstrap preferences = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestLegacyWorkerDefaultsDoNotOverrideWorkspaceLead(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.PatchPreferences(t.Context(), []byte(`{"usage":{"default_worker":"claude","reasoning_effort":"low","permission_mode":"plan"}}`)); err != nil {
		t.Fatal(err)
	}

	request := applyUsageDefaults(messageRequest{}, preferences.UsageDefaults{DefaultWorker: "claude", ReasoningEffort: "low", PermissionMode: "plan"})
	if request.Provider != "claude" || request.ReasoningEffort != "low" || request.PermissionMode != "plan" {
		t.Fatalf("resolved defaults = %#v", request)
	}
	explicit := applyUsageDefaults(messageRequest{Provider: "pi", ReasoningEffort: "high", PermissionMode: "auto"}, preferences.UsageDefaults{DefaultWorker: "claude", ReasoningEffort: "low", PermissionMode: "plan"})
	if explicit.Provider != "pi" || explicit.ReasoningEffort != "high" || explicit.PermissionMode != "auto" {
		t.Fatalf("explicit overrides were replaced: %#v", explicit)
	}

	conversations, err := instance.ListConversations(t.Context(), "")
	if err != nil || len(conversations) == 0 {
		t.Fatalf("seeded conversations = %#v, err = %v", conversations, err)
	}
	handler := (&Server{Store: instance, HarnessWorkdir: t.TempDir()}).Handler()
	response := performRequest(handler, http.MethodPost, "/api/messages", `{"conversation_id":"`+conversations[0].ID+`","content":"Use my defaults"}`, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("message = %d, body = %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Run struct {
			Provider string `json:"provider"`
		} `json:"run"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Run.Provider != preferences.ProviderGrok {
		t.Fatalf("run provider = %q, want workspace lead %q", envelope.Run.Provider, preferences.ProviderGrok)
	}
}

func TestWorkspaceEngineDrivesUnconfiguredAgentRuns(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}

	handler := (&Server{Store: instance, HarnessWorkdir: t.TempDir()}).Handler()
	response := performRequest(handler, http.MethodPatch, "/api/preferences", `{"workspace":{"engine":"opencode"}}`, "")
	if response.Code != http.StatusOK {
		t.Fatalf("workspace preferences PATCH = %d, body = %s", response.Code, response.Body.String())
	}
	var updated preferences.Preferences
	if err := json.NewDecoder(response.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.Workspace.Engine != preferences.ProviderOpenCode || updated.Usage.DefaultWorker != preferences.ProviderOpenCode {
		t.Fatalf("workspace preference aliases = %#v", updated)
	}

	// The explicit workspace value is authoritative even if a legacy document
	// happens to contain a different default_worker value.
	divergent := preferences.Defaults()
	divergent.Workspace.Engine = preferences.ProviderOpenCode
	divergent.Workspace.Model = "opencode/deepseek-v4-flash-free"
	divergent.Usage.DefaultWorker = preferences.ProviderClaude
	request := applyWorkspaceDefaults(messageRequest{}, divergent)
	if request.Provider != preferences.ProviderOpenCode {
		t.Fatalf("resolved provider = %q, want workspace engine", request.Provider)
	}
	if request.Model != "opencode/deepseek-v4-flash-free" {
		t.Fatalf("resolved model = %q, want workspace model", request.Model)
	}
	explicit := applyWorkspaceDefaults(messageRequest{Provider: preferences.ProviderPi}, divergent)
	if explicit.Provider != preferences.ProviderPi {
		t.Fatalf("explicit provider was replaced: %#v", explicit)
	}

	conversations, err := instance.ListConversations(t.Context(), "")
	if err != nil || len(conversations) == 0 {
		t.Fatalf("seeded conversations = %#v, err = %v", conversations, err)
	}
	response = performRequest(handler, http.MethodPost, "/api/messages", `{"conversation_id":"`+conversations[0].ID+`","content":"Use the workspace engine"}`, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("message = %d, body = %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Run struct {
			Provider string `json:"provider"`
		} `json:"run"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Run.Provider != preferences.ProviderOpenCode {
		t.Fatalf("run provider = %q, want workspace engine", envelope.Run.Provider)
	}
}

func TestTeachTraceRedactsSensitiveActionsAndRequiresExplicitSkillEnable(t *testing.T) {
	root := t.TempDir()
	recorder, err := teach.New(teach.Config{Root: filepath.Join(root, "traces")})
	if err != nil {
		t.Fatal(err)
	}
	workshop, err := skillworkshop.New(filepath.Join(root, "workshop"))
	if err != nil {
		t.Fatal(err)
	}
	serverValue := &Server{
		Teach:             recorder,
		TeachRoot:         recorder.Root(),
		Workshop:          workshop,
		EnabledSkillsRoot: filepath.Join(root, "enabled"),
	}
	handler := serverValue.Handler()
	response := performRequest(handler, http.MethodPost, "/api/teach/start", `{"goal":"Open the release dashboard and verify the build"}`, "")
	if response.Code != http.StatusCreated {
		t.Fatalf("teach start = %d, body = %s", response.Code, response.Body.String())
	}
	secret := "password=hunter2"
	serverValue.recordTeachAction(teach.SurfaceBrowser, compute.BrowserAction{Action: "type", Text: secret}, compute.ViewStatus{URL: "https://example.com"})
	serverValue.recordTeachAction(teach.SurfaceBrowser, compute.BrowserAction{Action: "click", X: 40, Y: 80}, compute.ViewStatus{URL: "https://example.com"})

	response = performRequest(handler, http.MethodGet, "/api/teach", "", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"direct_novnc_input":false`) || !strings.Contains(response.Body.String(), `"step_count":2`) {
		t.Fatalf("teach status = %d, body = %s", response.Code, response.Body.String())
	}
	response = performRequest(handler, http.MethodPost, "/api/teach/stop", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("teach stop = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), secret) || !strings.Contains(response.Body.String(), `"redacted":true`) || !strings.Contains(response.Body.String(), `"auto_enabled":false`) || !strings.Contains(response.Body.String(), `"status":{"state":"stopped"`) {
		t.Fatalf("unsafe teach stop response = %s", response.Body.String())
	}
	drafts, err := workshop.List()
	if err != nil || len(drafts) != 1 || drafts[0].State != skillworkshop.StateDraft {
		t.Fatalf("workshop drafts = %#v, err = %v", drafts, err)
	}
	if _, err := workshop.Deployment(drafts[0].ID, serverValue.EnabledSkillsRoot); !errors.Is(err, skillworkshop.ErrNotFound) {
		t.Fatalf("teach stop enabled skill automatically: %v", err)
	}

	base := "/api/skills/" + drafts[0].ID
	response = performRequest(handler, http.MethodPost, base+"/review", `{"reviewer":"local-user","approved":true,"findings":[],"notes":"Bounded local workflow"}`, "")
	if response.Code != http.StatusOK {
		t.Fatalf("skill review = %d, body = %s", response.Code, response.Body.String())
	}
	response = performRequest(handler, http.MethodPost, base+"/test", `{"runner":"safe-fixture","passed":true,"evidence":"Validated with non-production data"}`, "")
	if response.Code != http.StatusOK {
		t.Fatalf("skill test = %d, body = %s", response.Code, response.Body.String())
	}
	response = performRequest(handler, http.MethodPost, base+"/enable", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("skill enable = %d, body = %s", response.Code, response.Body.String())
	}
	deployment, err := workshop.Deployment(drafts[0].ID, serverValue.EnabledSkillsRoot)
	if err != nil || !deployment.Active || deployment.Version != 1 {
		t.Fatalf("deployment = %#v, err = %v", deployment, err)
	}
	response = performRequest(handler, http.MethodPost, base+"/disable", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("skill disable = %d, body = %s", response.Code, response.Body.String())
	}
	response = performRequest(handler, http.MethodPost, base+"/rollback", `{"version":1}`, "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"active":true`) {
		t.Fatalf("skill rollback = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestComputerControlIsExclusiveAndRawVNCIsDisabled(t *testing.T) {
	serverValue := &Server{RemoteToken: "remote-secret", Docker: &compute.Docker{}}
	handler := serverValue.Handler()
	response := performRequest(handler, http.MethodPost, "/api/computer/agent-control", `{"enabled":true}`, "remote-secret")
	if response.Code != http.StatusOK || !serverValue.computerAgentControl || serverValue.computerTakeover {
		t.Fatalf("agent control state = %d, agent=%v takeover=%v", response.Code, serverValue.computerAgentControl, serverValue.computerTakeover)
	}
	response = performRequest(handler, http.MethodPost, "/api/computer/takeover", `{"enabled":true}`, "remote-secret")
	if response.Code != http.StatusOK || !serverValue.computerTakeover || serverValue.computerAgentControl {
		t.Fatalf("takeover state = %d, agent=%v takeover=%v", response.Code, serverValue.computerAgentControl, serverValue.computerTakeover)
	}

	response = performRequest(handler, http.MethodPost, "/api/computer/desktop/viewer-session", "", "remote-secret")
	if response.Code != http.StatusGone {
		t.Fatalf("viewer-session should be disabled = %d, body = %s", response.Code, response.Body.String())
	}
	rawVNC := performRequest(handler, http.MethodGet, "/api/computer/desktop/vnc_lite.html", "", "remote-secret")
	if rawVNC.Code != http.StatusGone {
		t.Fatalf("raw VNC should be disabled even with API authorization = %d, body = %s", rawVNC.Code, rawVNC.Body.String())
	}

	preflight := httptest.NewRequest(http.MethodOptions, "/api/events", nil)
	preflight.Header.Set("Origin", "http://127.0.0.1:1421")
	preflightResponse := httptest.NewRecorder()
	handler.ServeHTTP(preflightResponse, preflight)
	if !strings.Contains(preflightResponse.Header().Get("Access-Control-Allow-Headers"), "Last-Event-ID") || preflightResponse.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("CORS headers = %#v", preflightResponse.Header())
	}
}

func TestComputerStopStopsRemoteWorkerAndReleasesControl(t *testing.T) {
	stopped := false
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stop":
			stopped = true
			w.WriteHeader(http.StatusNoContent)
		case "/status":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(compute.Status{Available: true, State: compute.ComputerStateStopped, CanRetry: true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer worker.Close()
	serverValue := &Server{
		RemoteToken: "remote-secret",
		Docker: &compute.Docker{
			AllowExecution: true,
			RemoteBaseURL:  worker.URL,
			RemoteToken:    strings.Repeat("r", 32),
		},
		computerTakeover:     true,
		computerAgentControl: true,
	}
	response := performRequest(serverValue.Handler(), http.MethodPost, "/api/computer/stop", "", "remote-secret")
	if response.Code != http.StatusOK || !stopped {
		t.Fatalf("stop response = %d, stopped=%v, body=%s", response.Code, stopped, response.Body.String())
	}
	if serverValue.computerTakeover || serverValue.computerAgentControl {
		t.Fatal("computer control remained enabled after stop")
	}
	var status compute.Status
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.State != compute.ComputerStateStopped {
		t.Fatalf("stop status = %#v", status)
	}
}

func TestSecretHandoffHTTPNeverAcceptsSecretAndHasNoSubmitRoute(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	run, err := instance.CreateRun(t.Context(), conversation.ID, conversation.BotID, "grok", "safe prompt")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := secrethandoff.New(secrethandoff.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	computer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/target" || r.URL.Query().Get("surface") != "browser" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"computer_id":"computer-123","target_id":"tab-456"}`))
	}))
	defer computer.Close()
	parsed, err := url.Parse(computer.URL)
	if err != nil {
		t.Fatal(err)
	}
	viewPort, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: instance, SecretHandoffs: manager, Docker: &compute.Docker{ViewPort: viewPort}, computerTakeover: true}).Handler()

	secret := "do-not-accept-this-secret"
	badBody := `{"run_id":"` + run.ID + `","conversation_id":"` + conversation.ID + `","surface":"browser","purpose":"password","secret":"` + secret + `"}`
	response := performRequest(handler, http.MethodPost, "/api/secret-handoffs", badBody, "")
	if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), secret) {
		t.Fatalf("secret-bearing request = %d, body = %s", response.Code, response.Body.String())
	}
	body := `{"run_id":"` + run.ID + `","conversation_id":"` + conversation.ID + `","surface":"browser","purpose":"password"}`
	response = performRequest(handler, http.MethodPost, "/api/secret-handoffs", body, "")
	if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), `"secret"`) || !strings.Contains(response.Body.String(), `"submit_available":false`) {
		t.Fatalf("handoff create = %d, body = %s", response.Code, response.Body.String())
	}
	var created struct {
		Request secrethandoff.Request `json:"request"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Request.ComputerID != "" || created.Request.TargetID != "" {
		t.Fatalf("target metadata must not be exposed through HTTP: %#v", created.Request)
	}
	stored, err := manager.Get(created.Request.ID)
	if err != nil || stored.ComputerID != "computer-123" || stored.TargetID != "tab-456" {
		t.Fatalf("stored target binding = %#v, err=%v", stored, err)
	}
	response = performRequest(handler, http.MethodPost, "/api/secret-handoffs/"+created.Request.ID+"/submit", "", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("unexpected HTTP submit route = %d, body = %s", response.Code, response.Body.String())
	}
	response = performRequest(handler, http.MethodPost, "/api/secret-handoffs/"+created.Request.ID+"/cancel", "", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("handoff cancel = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestComputerControlTransitionCancelsPendingSecureHandoffs(t *testing.T) {
	manager, err := secrethandoff.New(secrethandoff.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	serverValue := &Server{SecretHandoffs: manager, computerTakeover: true}
	handler := serverValue.Handler()

	createReady := func(t *testing.T) secrethandoff.Request {
		t.Helper()
		request, err := manager.Create(secrethandoff.CreateRequest{
			RunID:          "run-control-transition",
			ConversationID: "conversation-control-transition",
			Surface:        string(teach.SurfaceBrowser),
			ComputerID:     "computer-123",
			TargetID:       "tab-456",
			Purpose:        secrethandoff.PurposePassword,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.Submit(request.ID, []byte("must-be-wiped-on-control-change")); err != nil {
			t.Fatal(err)
		}
		return request
	}
	assertCancelled := func(t *testing.T, request secrethandoff.Request) {
		t.Helper()
		status, err := manager.Get(request.ID)
		if err != nil || status.Status != secrethandoff.StatusCancelled || status.Ready {
			t.Fatalf("handoff after control transition = %#v, err=%v", status, err)
		}
	}

	first := createReady(t)
	response := performRequest(handler, http.MethodPost, "/api/computer/takeover", `{"enabled":false}`, "")
	if response.Code != http.StatusOK || serverValue.computerTakeover {
		t.Fatalf("release control = %d, takeover=%v", response.Code, serverValue.computerTakeover)
	}
	assertCancelled(t, first)

	second := createReady(t)
	response = performRequest(handler, http.MethodPost, "/api/computer/agent-control", `{"enabled":true}`, "")
	if response.Code != http.StatusOK || !serverValue.computerAgentControl || serverValue.computerTakeover {
		t.Fatalf("agent control = %d, agent=%v takeover=%v", response.Code, serverValue.computerAgentControl, serverValue.computerTakeover)
	}
	assertCancelled(t, second)
}

func TestNativeSecretDeliveryRequiresHumanTakeoverAndConsumesOnce(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	run, err := instance.CreateRun(t.Context(), conversation.ID, conversation.BotID, "grok", "safe prompt")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := secrethandoff.New(secrethandoff.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	created, err := manager.Create(secrethandoff.CreateRequest{
		RunID:          run.ID,
		ConversationID: conversation.ID,
		Surface:        string(teach.SurfaceBrowser),
		ComputerID:     "computer-123",
		TargetID:       "tab-456",
		Purpose:        secrethandoff.PurposePassword,
	})
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("sentinel-secret-not-for-traces")
	if err := manager.Submit(created.ID, secret); err != nil {
		t.Fatal(err)
	}

	var received struct {
		Action        string `json:"action"`
		Text          string `json:"text"`
		Sensitive     bool   `json:"sensitive"`
		NativeHandoff bool   `json:"native_handoff"`
		ComputerID    string `json:"computer_id"`
		TargetID      string `json:"target_id"`
	}
	computer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ready":true,"url":"https://example.test","title":"Ready","viewport":{"width":1440,"height":900}}`))
		case "/action":
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Errorf("decode sensitive action: %v", err)
			}
			if received.TargetID != "tab-456" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":"secure target changed"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ready":true,"url":"https://example.test","title":"Ready","viewport":{"width":1440,"height":900}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer computer.Close()
	parsed, err := url.Parse(computer.URL)
	if err != nil {
		t.Fatal(err)
	}
	viewPort, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	serverValue := &Server{
		Store:            instance,
		Docker:           &compute.Docker{ViewPort: viewPort},
		SecretHandoffs:   manager,
		computerTakeover: true,
	}
	if err := serverValue.DeliverSecretHandoff(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}
	if received.Action != "type" || !received.Sensitive || !received.NativeHandoff || received.Text != "sentinel-secret-not-for-traces" || received.ComputerID != "computer-123" || received.TargetID != "tab-456" {
		t.Fatalf("native delivery action = %#v", received)
	}
	status, err := manager.Get(created.ID)
	if err != nil || status.Status != secrethandoff.StatusClaimed || status.Ready {
		t.Fatalf("handoff after delivery = %#v, err=%v", status, err)
	}

	blocked, err := manager.Create(secrethandoff.CreateRequest{
		RunID:          run.ID,
		ConversationID: conversation.ID,
		Surface:        string(teach.SurfaceBrowser),
		ComputerID:     "computer-123",
		TargetID:       "tab-456",
		Purpose:        secrethandoff.PurposeTwoFactorCode,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Submit(blocked.ID, []byte("123456")); err != nil {
		t.Fatal(err)
	}
	serverValue.computerTakeover = false
	if err := serverValue.DeliverSecretHandoff(t.Context(), blocked.ID); err == nil || strings.Contains(err.Error(), "123456") {
		t.Fatalf("delivery without takeover = %v", err)
	}
	status, err = manager.Get(blocked.ID)
	if err != nil || status.Status != secrethandoff.StatusPending || !status.Ready {
		t.Fatalf("blocked handoff was unexpectedly consumed = %#v, err=%v", status, err)
	}

	targetChanged, err := manager.Create(secrethandoff.CreateRequest{
		RunID:          run.ID,
		ConversationID: conversation.ID,
		Surface:        string(teach.SurfaceBrowser),
		ComputerID:     "computer-123",
		TargetID:       "different-tab",
		Purpose:        secrethandoff.PurposePassword,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Submit(targetChanged.ID, []byte("another-sentinel")); err != nil {
		t.Fatal(err)
	}
	serverValue.computerTakeover = true
	if err := serverValue.DeliverSecretHandoff(t.Context(), targetChanged.ID); err == nil || strings.Contains(err.Error(), "another-sentinel") {
		t.Fatalf("delivery after target change = %v", err)
	}
	status, err = manager.Get(targetChanged.ID)
	if err != nil || status.Status != secrethandoff.StatusClaimed || status.Ready {
		t.Fatalf("target-changed handoff was not consumed after failed injection = %#v, err=%v", status, err)
	}
}

func TestIntegrationsEndpointUsesFixedInventoryAndCaches(t *testing.T) {
	runner := &countingIntegrationRunner{}
	handler := (&Server{IntegrationRunner: runner}).Handler()
	first := performRequest(handler, http.MethodGet, "/api/integrations", "", "")
	second := performRequest(handler, http.MethodGet, "/api/integrations", "", "")
	if first.Code != http.StatusOK || second.Code != http.StatusOK || runner.calls != len(integrations.AllowedCommandSpecs()) {
		t.Fatalf("integration inventory first=%d second=%d calls=%d", first.Code, second.Code, runner.calls)
	}
	if !strings.Contains(first.Body.String(), `"fixed_allowlist":true`) || !strings.Contains(second.Body.String(), `"cached":true`) {
		t.Fatalf("integration responses first=%s second=%s", first.Body.String(), second.Body.String())
	}
}

type countingIntegrationRunner struct {
	calls int
}

func (runner *countingIntegrationRunner) Run(_ context.Context, _ integrations.CommandSpec) (integrations.CommandOutput, error) {
	runner.calls++
	return integrations.CommandOutput{}, nil
}

func performRequest(handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
