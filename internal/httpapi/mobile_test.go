package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/compute"
	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/events"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

func TestMobileHandlerIsAnExactOriginFreeAllowlist(t *testing.T) {
	instance := openMobileHTTPStore(t)
	server := &Server{Store: instance, RemoteToken: "legacy-token", Broker: events.New()}
	handler := server.MobileHandler()

	unsafe := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/bootstrap"},
		{http.MethodGet, "/api/preferences"},
		{http.MethodPost, "/api/computer/ensure"},
		{http.MethodPost, "/api/computer/takeover"},
		{http.MethodPost, "/api/v1/computer/action"},
		{http.MethodPost, "/api/v1/computer/desktop/action"},
		{http.MethodGet, "/api/v1/computer/desktop/frame"},
		{http.MethodGet, "/api/harnesses/auth"},
		{http.MethodGet, "/api/teach"},
		{http.MethodGet, "/api/skills"},
		{http.MethodGet, "/api/secret-handoffs/transport"},
		{http.MethodOptions, "/api/v1/meta"},
		{http.MethodPost, "/api/v1/meta"},
	}
	for _, item := range unsafe {
		response := mobileHTTPCall(handler, item.method, item.path, "", "", "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, body = %s", item.method, item.path, response.Code, response.Body.String())
		}
	}

	meta := mobileHTTPCall(handler, http.MethodGet, "/api/v1/meta", "", "", "")
	if meta.Code != http.StatusOK {
		t.Fatalf("meta = %d, body = %s", meta.Code, meta.Body.String())
	}
	if meta.Header().Get("Access-Control-Allow-Credentials") != "" || meta.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("mobile CORS headers = %#v", meta.Header())
	}
	originRequest := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	originRequest.Header.Set("Origin", "https://openagentfleet.example")
	originResponse := httptest.NewRecorder()
	handler.ServeHTTP(originResponse, originRequest)
	if originResponse.Code != http.StatusForbidden || originResponse.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("origin response = %d, headers = %#v", originResponse.Code, originResponse.Header())
	}
}

func TestMobileAuthenticationFailuresAreIdentical(t *testing.T) {
	instance := openMobileHTTPStore(t)
	device, token := issueMobileHTTPCredential(t, instance, domain.RemoteScopeObserver)
	handler := (&Server{Store: instance, RemoteToken: token}).MobileHandler()

	missing := mobileHTTPCall(handler, http.MethodGet, "/api/v1/bootstrap", "", "", "")
	wrong := mobileHTTPCall(handler, http.MethodGet, "/api/v1/bootstrap", "", randomMobileHTTPToken(t), "")
	if err := instance.RevokeRemoteDevice(t.Context(), device.ID); err != nil {
		t.Fatal(err)
	}
	revoked := mobileHTTPCall(handler, http.MethodGet, "/api/v1/bootstrap", "", token, "")
	for name, response := range map[string]*httptest.ResponseRecorder{"missing": missing, "wrong": wrong, "revoked": revoked} {
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s auth = %d, body = %s", name, response.Code, response.Body.String())
		}
	}
	if missing.Body.String() != wrong.Body.String() || wrong.Body.String() != revoked.Body.String() {
		t.Fatalf("auth bodies differ: missing=%q wrong=%q revoked=%q", missing.Body.String(), wrong.Body.String(), revoked.Body.String())
	}
}

func TestMobileLogoutRevokesOnlyPresentedCredential(t *testing.T) {
	instance := openMobileHTTPStore(t)
	device, firstToken := issueMobileHTTPCredential(t, instance, domain.RemoteScopeObserver)
	secondToken := randomMobileHTTPToken(t)
	if err := instance.StoreRemoteCredential(t.Context(), device.ID, secondToken, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: instance}).MobileHandler()
	logout := mobileHTTPCall(handler, http.MethodPost, "/api/v1/session/logout", "", firstToken, "")
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout = %d, body = %s", logout.Code, logout.Body.String())
	}
	first := mobileHTTPCall(handler, http.MethodGet, "/api/v1/bootstrap", "", firstToken, "")
	second := mobileHTTPCall(handler, http.MethodGet, "/api/v1/bootstrap", "", secondToken, "")
	if first.Code != http.StatusUnauthorized || second.Code != http.StatusOK {
		t.Fatalf("post-logout first=%d second=%d", first.Code, second.Code)
	}
}

func TestMobilePairingIssuesDeviceSpecificBearerWithoutSecretReflection(t *testing.T) {
	instance := openMobileHTTPStore(t)
	handler := (&Server{Store: instance}).MobileHandler()

	invalidSecret := "invalid-pairing-secret-that-must-never-be-reflected"
	invalid := mobileHTTPCall(handler, http.MethodPost, "/api/v1/pair", `{"grant_id":"missing","pairing_secret":"`+invalidSecret+`","device_name":"Phone","platform":"ios"}`, "", "")
	if invalid.Code != http.StatusUnauthorized || strings.Contains(invalid.Body.String(), invalidSecret) || invalid.Body.String() != "{\"error\":\"pairing failed\"}\n" {
		t.Fatalf("invalid pairing = %d, body = %q", invalid.Code, invalid.Body.String())
	}

	firstGrant, firstSecret, err := instance.CreateRemotePairingGrant(t.Context(), domain.RemoteScopeObserver, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	secondGrant, secondSecret, err := instance.CreateRemotePairingGrant(t.Context(), domain.RemoteScopeController, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first := pairMobileHTTPDevice(t, handler, firstGrant.ID, firstSecret, "Robby's iPhone", "ios")
	second := pairMobileHTTPDevice(t, handler, secondGrant.ID, secondSecret, "Robby's Pixel", "android")
	if first.AccessToken == second.AccessToken || first.Device.ID == second.Device.ID {
		t.Fatalf("pairing identities were reused: first=%#v second=%#v", first, second)
	}
	if first.AuthVersion != 1 || first.TokenType != "Bearer" || first.HostID == "" || first.Device.ScopeProfile != domain.RemoteScopeObserver {
		t.Fatalf("first pairing response = %#v", first)
	}
	firstSession, err := instance.AuthenticateMobileCredential(t.Context(), first.AccessToken)
	if err != nil || firstSession.Device.ID != first.Device.ID {
		t.Fatalf("first credential session = %#v, err = %v", firstSession, err)
	}
	secondSession, err := instance.AuthenticateMobileCredential(t.Context(), second.AccessToken)
	if err != nil || secondSession.Device.ID != second.Device.ID {
		t.Fatalf("second credential session = %#v, err = %v", secondSession, err)
	}
}

func TestMobileBootstrapAndComputerDTOsDoNotLeakLocalState(t *testing.T) {
	instance := openMobileHTTPStore(t)
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.CreateMessage(t.Context(), conversation.ID, "user", "safe visible message"); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.CreateRun(t.Context(), conversation.ID, conversation.BotID, "grok", "PRIVATE_PROMPT_SENTINEL"); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.UpsertHarnessSession(t.Context(), conversation.ID, "grok", "NATIVE_SESSION_SENTINEL", "/private/workdir/sentinel", "session", "ready"); err != nil {
		t.Fatal(err)
	}
	if err := instance.UpsertCapabilities(t.Context(), []domain.Capability{{Name: "private", Command: "PRIVATE_COMMAND_SENTINEL", Available: true}}); err != nil {
		t.Fatal(err)
	}
	_, token := issueMobileHTTPCredential(t, instance, domain.RemoteScopeObserver)
	handler := (&Server{Store: instance}).MobileHandler()

	bootstrap := mobileHTTPCall(handler, http.MethodGet, "/api/v1/bootstrap?conversation_id="+conversation.ID, "", token, "")
	if bootstrap.Code != http.StatusOK {
		t.Fatalf("bootstrap = %d, body = %s", bootstrap.Code, bootstrap.Body.String())
	}
	assertMobileResponseHasNoBannedState(t, bootstrap.Body.String())
	for _, sentinel := range []string{"PRIVATE_PROMPT_SENTINEL", "NATIVE_SESSION_SENTINEL", "/private/workdir/sentinel", "PRIVATE_COMMAND_SENTINEL"} {
		if strings.Contains(bootstrap.Body.String(), sentinel) {
			t.Fatalf("bootstrap leaked %q: %s", sentinel, bootstrap.Body.String())
		}
	}
	computer := mobileHTTPCall(handler, http.MethodGet, "/api/v1/computer", "", token, "")
	if computer.Code != http.StatusOK {
		t.Fatalf("computer = %d, body = %s", computer.Code, computer.Body.String())
	}
	assertMobileResponseHasNoBannedState(t, computer.Body.String())
}

func TestMobileComputerControlUsesDeviceBoundLease(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/status":
			_ = json.NewEncoder(w).Encode(compute.Status{State: compute.ComputerStateReady, Available: true, Running: true, BrowserReady: true, DesktopReady: true})
		case "/action", "/desktop/action":
			_ = json.NewEncoder(w).Encode(compute.ViewStatus{Ready: true, Title: "Remote controlled", ViewportWidth: 1280, ViewportHeight: 720})
		default:
			http.NotFound(w, r)
		}
	}))
	defer worker.Close()
	instance := openMobileHTTPStore(t)
	_, controllerToken := issueMobileHTTPCredential(t, instance, domain.RemoteScopeController)
	_, otherToken := issueMobileHTTPCredential(t, instance, domain.RemoteScopeController)
	_, observerToken := issueMobileHTTPCredential(t, instance, domain.RemoteScopeObserver)
	docker := &compute.Docker{AllowExecution: true, RemoteBaseURL: worker.URL, RemoteToken: strings.Repeat("r", 32)}
	handler := (&Server{Store: instance, Docker: docker}).MobileHandler()

	if response := mobileHTTPCall(handler, http.MethodPost, "/api/v1/computer/control", `{"enabled":true}`, observerToken, ""); response.Code != http.StatusForbidden {
		t.Fatalf("observer control = %d, body = %s", response.Code, response.Body.String())
	}
	control := mobileHTTPCall(handler, http.MethodPost, "/api/v1/computer/control", `{"enabled":true}`, controllerToken, "")
	if control.Code != http.StatusOK || !strings.Contains(control.Body.String(), `"control_held":true`) {
		t.Fatalf("controller control = %d, body = %s", control.Code, control.Body.String())
	}
	if response := mobileHTTPCall(handler, http.MethodPost, "/api/v1/computer/control", `{"enabled":true}`, otherToken, ""); response.Code != http.StatusLocked {
		t.Fatalf("second controller control = %d, body = %s", response.Code, response.Body.String())
	}
	action := mobileHTTPCall(handler, http.MethodPost, "/api/v1/computer/browser/action", `{"action":"click","x":12,"y":20}`, controllerToken, "")
	if action.Code != http.StatusOK || !strings.Contains(action.Body.String(), `"control_held":true`) {
		t.Fatalf("controller action = %d, body = %s", action.Code, action.Body.String())
	}
	for _, body := range []string{`{"action":"type","text":"secret"}`, `{"action":"navigate","url":"https://example.com"}`} {
		if response := mobileHTTPCall(handler, http.MethodPost, "/api/v1/computer/browser/action", body, controllerToken, ""); response.Code != http.StatusBadRequest {
			t.Fatalf("non-click mobile action %q = %d, body = %s", body, response.Code, response.Body.String())
		}
	}
	release := mobileHTTPCall(handler, http.MethodPost, "/api/v1/computer/control", `{"enabled":false}`, controllerToken, "")
	if release.Code != http.StatusOK || strings.Contains(release.Body.String(), `"control_held":true`) {
		t.Fatalf("controller release = %d, body = %s", release.Code, release.Body.String())
	}
	if response := mobileHTTPCall(handler, http.MethodPost, "/api/v1/computer/browser/action", `{"action":"click","x":12,"y":20}`, controllerToken, ""); response.Code != http.StatusLocked {
		t.Fatalf("action after release = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestMobileComputerLeaseExpiryAndLogoutClearTakeover(t *testing.T) {
	instance := openMobileHTTPStore(t)
	_, controllerToken := issueMobileHTTPCredential(t, instance, domain.RemoteScopeController)
	device, logoutToken := issueMobileHTTPCredential(t, instance, domain.RemoteScopeController)
	server := &Server{Store: instance}
	handler := server.MobileHandler()

	if response := mobileHTTPCall(handler, http.MethodPost, "/api/v1/computer/control", `{"enabled":true}`, controllerToken, ""); response.Code != http.StatusOK {
		t.Fatalf("initial control = %d, body = %s", response.Code, response.Body.String())
	}
	server.remoteComputerLeaseMu.Lock()
	server.remoteComputerExpiresAt = time.Now().UTC().Add(-time.Second)
	server.remoteComputerLeaseMu.Unlock()
	status := mobileHTTPCall(handler, http.MethodGet, "/api/v1/computer", "", controllerToken, "")
	if status.Code != http.StatusOK || strings.Contains(status.Body.String(), `"control_held":true`) {
		t.Fatalf("expired control status = %d, body = %s", status.Code, status.Body.String())
	}
	server.computerMu.RLock()
	takeoverAfterExpiry := server.computerTakeover
	server.computerMu.RUnlock()
	if takeoverAfterExpiry {
		t.Fatal("expired mobile lease left global takeover enabled")
	}

	if response := mobileHTTPCall(handler, http.MethodPost, "/api/v1/computer/control", `{"enabled":true}`, logoutToken, ""); response.Code != http.StatusOK {
		t.Fatalf("second controller control = %d, body = %s", response.Code, response.Body.String())
	}
	logout := mobileHTTPCall(handler, http.MethodPost, "/api/v1/session/logout", "", logoutToken, "")
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout = %d, body = %s", logout.Code, logout.Body.String())
	}
	server.remoteComputerLeaseMu.Lock()
	ownerAfterLogout, deviceAfterLogout := server.remoteComputerOwner, server.remoteComputerDeviceID
	server.remoteComputerLeaseMu.Unlock()
	if ownerAfterLogout != "" || deviceAfterLogout != "" {
		t.Fatalf("logout left mobile lease owner=%q device=%q", ownerAfterLogout, deviceAfterLogout)
	}
	server.computerMu.RLock()
	takeoverAfterLogout := server.computerTakeover
	server.computerMu.RUnlock()
	if takeoverAfterLogout {
		t.Fatal("logout left global takeover enabled")
	}
	if device.ID == "" {
		t.Fatal("logout test device was not persisted")
	}
}

func TestMobileObserverControllerAndMessageIdempotency(t *testing.T) {
	instance := openMobileHTTPStore(t)
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	_, observerToken := issueMobileHTTPCredential(t, instance, domain.RemoteScopeObserver)
	_, controllerToken := issueMobileHTTPCredential(t, instance, domain.RemoteScopeController)
	broker := events.New()
	subscriptionContext, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	published, unsubscribe := broker.Subscribe(subscriptionContext)
	t.Cleanup(unsubscribe)
	handler := (&Server{Store: instance, Broker: broker, AllowHarnessExecution: false}).MobileHandler()
	body := `{"conversation_id":"` + conversation.ID + `","content":"Build the mobile alpha"}`

	observer := mobileHTTPCall(handler, http.MethodPost, "/api/v1/messages", body, observerToken, "observer-key")
	if observer.Code != http.StatusForbidden {
		t.Fatalf("observer post = %d, body = %s", observer.Code, observer.Body.String())
	}
	first := mobileHTTPCall(handler, http.MethodPost, "/api/v1/messages", body, controllerToken, "controller-key")
	if first.Code != http.StatusAccepted {
		t.Fatalf("controller post = %d, body = %s", first.Code, first.Body.String())
	}
	repeated := mobileHTTPCall(handler, http.MethodPost, "/api/v1/messages", body, controllerToken, "controller-key")
	if repeated.Code != http.StatusAccepted || repeated.Body.String() != first.Body.String() {
		t.Fatalf("repeated post = %d body=%q; first=%q", repeated.Code, repeated.Body.String(), first.Body.String())
	}
	conflict := mobileHTTPCall(handler, http.MethodPost, "/api/v1/messages", `{"conversation_id":"`+conversation.ID+`","content":"Different"}`, controllerToken, "controller-key")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("idempotency conflict = %d, body = %s", conflict.Code, conflict.Body.String())
	}
	clientOptions := mobileHTTPCall(handler, http.MethodPost, "/api/v1/messages", `{"conversation_id":"`+conversation.ID+`","content":"No options","provider":"claude","model":"x","attachment_ids":["x"]}`, controllerToken, "option-key")
	if clientOptions.Code != http.StatusBadRequest {
		t.Fatalf("client options = %d, body = %s", clientOptions.Code, clientOptions.Body.String())
	}
	runs, err := instance.ListRuns(t.Context(), conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Provider != "grok" {
		t.Fatalf("runs = %#v", runs)
	}
	if runs[0].Status != "blocked" {
		t.Fatalf("run status = %q, want blocked", runs[0].Status)
	}
	durable, err := instance.ListRunEvents(t.Context(), runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(durable) != 2 || durable[0].Type != "run.queued" || durable[1].Type != "run.blocked" {
		t.Fatalf("mobile lifecycle events = %#v", durable)
	}
	for index, want := range durable {
		select {
		case got := <-published:
			if got.ID != want.ID || got.Type != want.Type {
				t.Fatalf("published mobile event %d = %#v, want durable %#v", index, got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for published mobile event %d", index)
		}
	}
	select {
	case duplicate := <-published:
		t.Fatalf("idempotent retry published duplicate event: %#v", duplicate)
	case <-time.After(25 * time.Millisecond):
	}
	messages, err := instance.ListMessages(t.Context(), conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Content != "Build the mobile alpha" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestMobileStaleCursorResetsWithoutHistoricalReplay(t *testing.T) {
	instance := openMobileHTTPStore(t)
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	run, err := instance.CreateRun(t.Context(), conversation.ID, conversation.BotID, "grok", "historical")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.AppendRunEvent(t.Context(), run.ID, "run.queued", `{"status":"queued"}`); err != nil {
		t.Fatal(err)
	}
	_, token := issueMobileHTTPCredential(t, instance, domain.RemoteScopeObserver)
	handler := (&Server{Store: instance, Broker: events.New()}).MobileHandler()

	for _, after := range []string{"", "not-a-number", "999999999", "18446744073709551615"} {
		response := mobileHTTPCall(handler, http.MethodGet, "/api/v1/events?after="+after, "", token, "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "event: ofb.reset") || strings.Contains(response.Body.String(), "event: ofb.event") || strings.Contains(response.Body.String(), "run.queued") {
			t.Fatalf("after=%q response = %d, body = %q", after, response.Code, response.Body.String())
		}
	}
}

func TestMobileDeviceRevocationClosesActiveEventStream(t *testing.T) {
	instance := openMobileHTTPStore(t)
	device, token := issueMobileHTTPCredential(t, instance, domain.RemoteScopeObserver)
	snapshot, err := instance.MobileBootstrapSnapshot(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&Server{Store: instance, Broker: events.New()}).MobileHandler())
	defer server.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/events?after="+jsonNumber(snapshot.EventCursor), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || line != ": connected\n" {
		t.Fatalf("stream first line = %q, err = %v", line, err)
	}
	if err := instance.RevokeRemoteDevice(t.Context(), device.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, readErr := io.ReadAll(reader)
		done <- readErr
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stream close error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revoked device stream remained open beyond the re-authentication bound")
	}
}

func TestLegacyMobileAdminEndpointsRequireLoopbackAndExistingRemoteAuth(t *testing.T) {
	instance := openMobileHTTPStore(t)
	handler := (&Server{Store: instance}).Handler()
	nonLoopback := httptest.NewRequest(http.MethodPost, "/api/mobile/pairings", strings.NewReader(`{"scope":"observer"}`))
	nonLoopback.RemoteAddr = "100.64.0.2:4317"
	nonLoopbackResponse := httptest.NewRecorder()
	handler.ServeHTTP(nonLoopbackResponse, nonLoopback)
	if nonLoopbackResponse.Code != http.StatusForbidden {
		t.Fatalf("non-loopback pairing = %d, body = %s", nonLoopbackResponse.Code, nonLoopbackResponse.Body.String())
	}

	loopback := httptest.NewRequest(http.MethodPost, "/api/mobile/pairings", strings.NewReader(`{"scope":"controller","ttl_seconds":60}`))
	loopback.RemoteAddr = "127.0.0.1:4317"
	loopbackResponse := httptest.NewRecorder()
	handler.ServeHTTP(loopbackResponse, loopback)
	if loopbackResponse.Code != http.StatusCreated || strings.Contains(loopbackResponse.Body.String(), `"url"`) || !strings.Contains(loopbackResponse.Body.String(), `"pairing_secret"`) {
		t.Fatalf("loopback pairing = %d, body = %s", loopbackResponse.Code, loopbackResponse.Body.String())
	}
	bareIPv6Loopback := httptest.NewRequest(http.MethodGet, "/api/mobile/devices", nil)
	bareIPv6Loopback.RemoteAddr = "::1"
	bareIPv6LoopbackResponse := httptest.NewRecorder()
	handler.ServeHTTP(bareIPv6LoopbackResponse, bareIPv6Loopback)
	if bareIPv6LoopbackResponse.Code != http.StatusOK {
		t.Fatalf("bare IPv6 loopback devices = %d, body = %s", bareIPv6LoopbackResponse.Code, bareIPv6LoopbackResponse.Body.String())
	}

	devices := httptest.NewRequest(http.MethodGet, "/api/mobile/devices", nil)
	devices.RemoteAddr = "203.0.113.9:4317"
	devicesResponse := httptest.NewRecorder()
	handler.ServeHTTP(devicesResponse, devices)
	if devicesResponse.Code != http.StatusForbidden {
		t.Fatalf("non-loopback devices = %d", devicesResponse.Code)
	}
	revoke := httptest.NewRequest(http.MethodPost, "/api/mobile/devices/device-x/revoke", nil)
	revoke.RemoteAddr = "203.0.113.9:4317"
	revokeResponse := httptest.NewRecorder()
	handler.ServeHTTP(revokeResponse, revoke)
	if revokeResponse.Code != http.StatusForbidden {
		t.Fatalf("non-loopback revoke = %d", revokeResponse.Code)
	}

	protected := (&Server{Store: instance, RemoteToken: "legacy-admin-token"}).Handler()
	protectedRequest := httptest.NewRequest(http.MethodGet, "/api/mobile/devices", nil)
	protectedRequest.RemoteAddr = "127.0.0.1:4317"
	protectedResponse := httptest.NewRecorder()
	protected.ServeHTTP(protectedResponse, protectedRequest)
	if protectedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("legacy global auth was loosened: %d, body = %s", protectedResponse.Code, protectedResponse.Body.String())
	}
}

func openMobileHTTPStore(t *testing.T) *store.Store {
	t.Helper()
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	return instance
}

func issueMobileHTTPCredential(t *testing.T, instance *store.Store, scope string) (domain.RemoteDevice, string) {
	t.Helper()
	grant, secret, err := instance.CreateRemotePairingGrant(t.Context(), scope, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token := randomMobileHTTPToken(t)
	device, err := instance.ClaimRemotePairingGrant(t.Context(), grant.ID, secret, "HTTP Test Phone", domain.RemotePlatformIOS, token, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return device, token
}

func pairMobileHTTPDevice(t *testing.T, handler http.Handler, grantID, secret, name, platform string) mobilePairResponse {
	t.Helper()
	body, err := json.Marshal(mobilePairRequest{GrantID: grantID, PairingSecret: secret, DeviceName: name, Platform: platform})
	if err != nil {
		t.Fatal(err)
	}
	response := mobileHTTPCall(handler, http.MethodPost, "/api/v1/pair", string(body), "", "")
	if response.Code != http.StatusCreated {
		t.Fatalf("pair = %d, body = %s", response.Code, response.Body.String())
	}
	var value mobilePairResponse
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func mobileHTTPCall(handler http.Handler, method, path, body, token, idempotencyKey string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func randomMobileHTTPToken(t *testing.T) string {
	t.Helper()
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer)
}

func assertMobileResponseHasNoBannedState(t *testing.T, body string) {
	t.Helper()
	lower := strings.ToLower(body)
	for _, banned := range []string{
		`"workdir"`, `"native_session_id"`, `"command"`, `"auth"`, `"oauth"`,
		`"skills"`, `"preferences"`, `"attachments"`, `"storage_path"`,
		`"container_id"`, `"image"`, `"url"`, `"takeover"`, `"agent_control"`,
		`"prompt"`, `"provider"`,
	} {
		if strings.Contains(lower, banned) {
			t.Fatalf("mobile response contains banned field %s: %s", banned, body)
		}
	}
}

func jsonNumber(value uint64) string {
	return strings.TrimSpace(string(mustJSON(value)))
}

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
