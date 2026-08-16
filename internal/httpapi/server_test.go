package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/events"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

func TestRemoteTokenProtectsAPIButNotHealth(t *testing.T) {
	server := (&Server{RemoteToken: "secret"}).Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
	healthRequest := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthResponse := httptest.NewRecorder()
	server.ServeHTTP(healthResponse, healthRequest)
	if healthResponse.Code != http.StatusOK {
		t.Fatalf("expected health 200, got %d", healthResponse.Code)
	}
}

func TestRemoteTokenAuthorizationRequiresExactBearerValue(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	request.Header.Set("Authorization", "Bearer remote-token")
	if !authorized(request, "remote-token") {
		t.Fatal("exact remote bearer token was rejected")
	}
	request.Header.Set("Authorization", "Bearer remote-token-extra")
	if authorized(request, "remote-token") {
		t.Fatal("different remote bearer token was accepted")
	}
}

func TestCORSAllowsSSEResumeHeader(t *testing.T) {
	server := (&Server{}).Handler()
	request := httptest.NewRequest(http.MethodOptions, "/api/events", nil)
	request.Header.Set("Origin", "http://127.0.0.1:1421")
	request.Header.Set("Access-Control-Request-Headers", "Last-Event-ID")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d", response.Code)
	}
	if !strings.Contains(response.Header().Get("Access-Control-Allow-Headers"), "Last-Event-ID") {
		t.Fatalf("SSE resume header is not CORS allowed: %q", response.Header().Get("Access-Control-Allow-Headers"))
	}
}

func TestDesktopLifecycleEventsCommitAndPublishOnce(t *testing.T) {
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
	broker := events.New()
	subscriptionContext, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	published, unsubscribe := broker.Subscribe(subscriptionContext)
	t.Cleanup(unsubscribe)
	handler := (&Server{Store: instance, Broker: broker, HarnessWorkdir: t.TempDir()}).Handler()
	response := performRequest(handler, http.MethodPost, "/api/messages", `{"conversation_id":"`+conversation.ID+`","content":"Atomic lifecycle","provider":"grok"}`, "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("create message = %d, body = %s", response.Code, response.Body.String())
	}
	runs, err := instance.ListRuns(t.Context(), conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != "blocked" {
		t.Fatalf("runs = %#v, want one blocked run", runs)
	}
	durable, err := instance.ListRunEvents(t.Context(), runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(durable) != 2 || durable[0].Type != "run.queued" || durable[1].Type != "run.blocked" {
		t.Fatalf("durable lifecycle events = %#v", durable)
	}
	for index, want := range durable {
		select {
		case got := <-published:
			if got.ID != want.ID || got.Type != want.Type {
				t.Fatalf("published event %d = %#v, want durable %#v", index, got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for published event %d", index)
		}
	}
	select {
	case duplicate := <-published:
		t.Fatalf("unexpected duplicate lifecycle publication: %#v", duplicate)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestLifecycleEventIsNotPublishedWhenCommitFails(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Seed(t.Context()); err != nil {
		_ = instance.Close()
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		_ = instance.Close()
		t.Fatal(err)
	}
	run, _, err := instance.CreateRunWithQueuedEvent(t.Context(), conversation.ID, conversation.BotID, "grok", "commit failure")
	if err != nil {
		_ = instance.Close()
		t.Fatal(err)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	broker := events.New()
	subscriptionContext, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	published, unsubscribe := broker.Subscribe(subscriptionContext)
	t.Cleanup(unsubscribe)
	server := &Server{Store: instance, Broker: broker}
	if _, err := server.commitRunLifecycleEvent(t.Context(), run, "running", "", "run.started", `{"status":"running"}`); err == nil {
		t.Fatal("lifecycle commit unexpectedly succeeded against closed store")
	}
	select {
	case event := <-published:
		t.Fatalf("failed lifecycle commit was published: %#v", event)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestCancellingApprovalWaitResolvesPendingRequest(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversations, err := instance.ListConversations(t.Context(), "")
	if err != nil || len(conversations) == 0 {
		t.Fatalf("conversations = %#v, err = %v", conversations, err)
	}
	run, err := instance.CreateRun(t.Context(), conversations[0].ID, conversations[0].BotID, "grok", "approval test")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: instance}
	approvalContext, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, approvalErr := server.awaitApproval(approvalContext, run, harness.PermissionRequest{ToolCall: json.RawMessage(`{"title":"terminal"}`), Options: json.RawMessage(`[]`)})
		result <- approvalErr
	}()

	var approval domain.ApprovalRequest
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pending, listErr := instance.ListApprovals(t.Context(), "pending")
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(pending) == 1 {
			approval = pending[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if approval.ID == "" {
		t.Fatal("approval was not created")
	}
	cancel()
	select {
	case approvalErr := <-result:
		if !errors.Is(approvalErr, context.Canceled) {
			t.Fatalf("awaitApproval error = %v", approvalErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("awaitApproval did not stop after cancellation")
	}
	resolved, err := instance.GetApproval(t.Context(), approval.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != "cancelled" {
		t.Fatalf("approval status = %q, want cancelled", resolved.Status)
	}
}

func TestResolveApprovalRejectsTerminalRun(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversations, err := instance.ListConversations(t.Context(), "")
	if err != nil || len(conversations) == 0 {
		t.Fatalf("conversations = %#v, err = %v", conversations, err)
	}
	run, err := instance.CreateRun(t.Context(), conversations[0].ID, conversations[0].BotID, "grok", "terminal run")
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.UpdateRun(t.Context(), run.ID, "failed", "test"); err != nil {
		t.Fatal(err)
	}
	approval, err := instance.CreateApproval(t.Context(), run.ID, "grok", "terminal", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: instance}).Handler()
	response := performRequest(handler, http.MethodPost, "/api/approvals/"+approval.ID, `{"status":"approved","option_id":"allow"}`, "")
	if response.Code != http.StatusConflict {
		t.Fatalf("terminal approval resolution = %d, body = %s", response.Code, response.Body.String())
	}
	stored, err := instance.GetApproval(t.Context(), approval.ID)
	if err != nil || stored.Status != "pending" {
		t.Fatalf("stored approval = %#v, err = %v", stored, err)
	}
}

func TestEventStreamReplaysOnlyAfterResumeCursor(t *testing.T) {
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
	run, err := instance.CreateRun(t.Context(), conversation.ID, conversation.BotID, "grok", "test stream")
	if err != nil {
		t.Fatal(err)
	}
	first, err := instance.AppendRunEvent(t.Context(), run.ID, "run.queued", `{}`)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer((&Server{Store: instance, Broker: events.New()}).Handler())
	defer server.Close()

	open := func(lastEventID string) (*http.Response, *bufio.Reader, context.CancelFunc) {
		ctx, cancel := context.WithCancel(t.Context())
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/events?conversation_id="+conversation.ID, nil)
		if requestErr != nil {
			cancel()
			t.Fatal(requestErr)
		}
		if lastEventID != "" {
			request.Header.Set("Last-Event-ID", lastEventID)
		}
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr != nil {
			cancel()
			t.Fatal(requestErr)
		}
		reader := bufio.NewReader(response.Body)
		for _, want := range []string{"event: ready\n", "data: {\"ok\":true}\n", "\n"} {
			line, readErr := reader.ReadString('\n')
			if readErr != nil || line != want {
				cancel()
				_ = response.Body.Close()
				t.Fatalf("ready stream line = %q, err = %v, want %q", line, readErr, want)
			}
		}
		return response, reader, cancel
	}

	initial, initialReader, cancelInitial := open("")
	defer cancelInitial()
	defer initial.Body.Close()
	nextLine := make(chan string, 1)
	go func() {
		line, _ := initialReader.ReadString('\n')
		nextLine <- line
	}()
	select {
	case line := <-nextLine:
		t.Fatalf("fresh stream replayed durable history: %q", line)
	case <-time.After(100 * time.Millisecond):
		// A fresh bootstrap is authoritative; no historical event should arrive.
	}
	cancelInitial()
	_ = initial.Body.Close()

	second, err := instance.AppendRunEvent(t.Context(), run.ID, "run.started", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	resumed, resumedReader, cancelResumed := open(first.ID)
	defer cancelResumed()
	defer resumed.Body.Close()
	line, err := resumedReader.ReadString('\n')
	if err != nil || line != "id: "+second.ID+"\n" {
		t.Fatalf("resumed stream first replay line = %q, err = %v, want event %q", line, err, second.ID)
	}
}

func TestCORSAndMutationsRejectUntrustedLoopbackOrigins(t *testing.T) {
	server := (&Server{}).Handler()
	preflight := httptest.NewRequest(http.MethodOptions, "/api/events", nil)
	preflight.Header.Set("Origin", "http://127.0.0.1:9999")
	preflightResponse := httptest.NewRecorder()
	server.ServeHTTP(preflightResponse, preflight)
	if preflightResponse.Code != http.StatusForbidden || preflightResponse.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("untrusted preflight = %d, headers = %#v", preflightResponse.Code, preflightResponse.Header())
	}

	mutation := httptest.NewRequest(http.MethodPost, "/api/computer/takeover", strings.NewReader(`{"enabled":true}`))
	mutation.Header.Set("Origin", "http://127.0.0.1:9999")
	mutationResponse := httptest.NewRecorder()
	server.ServeHTTP(mutationResponse, mutation)
	if mutationResponse.Code != http.StatusForbidden {
		t.Fatalf("untrusted mutation = %d, body = %s", mutationResponse.Code, mutationResponse.Body.String())
	}
}

func TestCORSAllowsViteFallbackDevelopmentOrigin(t *testing.T) {
	server := (&Server{}).Handler()
	request := httptest.NewRequest(http.MethodOptions, "/api/events", nil)
	request.Header.Set("Origin", "http://127.0.0.1:1422")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("Access-Control-Allow-Origin") != "http://127.0.0.1:1422" {
		t.Fatalf("Vite fallback CORS = %d, headers = %#v", response.Code, response.Header())
	}
}

func TestConversationAndSearchAPI(t *testing.T) {
	instance, err := store.Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	server := (&Server{Store: instance, HarnessWorkdir: t.TempDir()}).Handler()
	defaultCreateRequest := httptest.NewRequest(http.MethodPost, "/api/conversations", strings.NewReader(`{"title":"Hidden"}`))
	defaultCreateResponse := httptest.NewRecorder()
	server.ServeHTTP(defaultCreateResponse, defaultCreateRequest)
	if defaultCreateResponse.Code != http.StatusConflict {
		t.Fatalf("default conversation creation status = %d, body = %s", defaultCreateResponse.Code, defaultCreateResponse.Body.String())
	}
	preferencesRequest := httptest.NewRequest(http.MethodPatch, "/api/preferences", strings.NewReader(`{"features":{"multiple_conversations":true}}`))
	preferencesResponse := httptest.NewRecorder()
	server.ServeHTTP(preferencesResponse, preferencesRequest)
	if preferencesResponse.Code != http.StatusOK {
		t.Fatalf("enable multiple conversations status = %d, body = %s", preferencesResponse.Code, preferencesResponse.Body.String())
	}
	createRequest := httptest.NewRequest(http.MethodPost, "/api/conversations", strings.NewReader(`{"title":"Research"}`))
	createResponse := httptest.NewRecorder()
	server.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create conversation status = %d, body = %s", createResponse.Code, createResponse.Body.String())
	}
	var conversation domain.Conversation
	if err := json.NewDecoder(createResponse.Body).Decode(&conversation); err != nil {
		t.Fatal(err)
	}
	if conversation.Title != "Research" {
		t.Fatalf("conversation = %#v", conversation)
	}
	if _, err := instance.CreateMessage(t.Context(), conversation.ID, "user", "searchable Atlas note"); err != nil {
		t.Fatal(err)
	}
	renameRequest := httptest.NewRequest(http.MethodPatch, "/api/conversations/"+conversation.ID, strings.NewReader(`{"title":"Renamed"}`))
	renameResponse := httptest.NewRecorder()
	server.ServeHTTP(renameResponse, renameRequest)
	if renameResponse.Code != http.StatusOK {
		t.Fatalf("rename status = %d, body = %s", renameResponse.Code, renameResponse.Body.String())
	}
	searchRequest := httptest.NewRequest(http.MethodGet, "/api/search?q=searchable", nil)
	searchResponse := httptest.NewRecorder()
	server.ServeHTTP(searchResponse, searchRequest)
	if searchResponse.Code != http.StatusOK || !strings.Contains(searchResponse.Body.String(), conversation.ID) {
		t.Fatalf("search status = %d, body = %s", searchResponse.Code, searchResponse.Body.String())
	}
}

func TestSTTStatusAndTranscriptionRequireConfiguration(t *testing.T) {
	server := (&Server{}).Handler()

	statusRequest := httptest.NewRequest(http.MethodGet, "/api/stt", nil)
	statusResponse := httptest.NewRecorder()
	server.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("STT status = %d, body = %s", statusResponse.Code, statusResponse.Body.String())
	}
	var status struct {
		Available bool   `json:"available"`
		Detail    string `json:"detail"`
	}
	if err := json.NewDecoder(statusResponse.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Available || status.Detail == "" {
		t.Fatalf("unconfigured STT status = %#v", status)
	}

	transcriptionRequest := httptest.NewRequest(http.MethodPost, "/api/transcriptions", nil)
	transcriptionResponse := httptest.NewRecorder()
	server.ServeHTTP(transcriptionResponse, transcriptionRequest)
	if transcriptionResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured transcription = %d, body = %s", transcriptionResponse.Code, transcriptionResponse.Body.String())
	}
}

func TestAttachmentUploadAndClaimLifecycle(t *testing.T) {
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

	uploadDir := filepath.Join(t.TempDir(), "uploads")
	server := (&Server{
		Store:                 instance,
		HarnessWorkdir:        t.TempDir(),
		UploadDir:             uploadDir,
		AllowHarnessExecution: false,
	}).Handler()

	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	if err := writer.WriteField("conversation_id", conversation.ID); err != nil {
		t.Fatal(err)
	}
	file, err := writer.CreateFormFile("file", "../brief?.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("attachment contents")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	uploadRequest := httptest.NewRequest(http.MethodPost, "/api/attachments", &multipartBody)
	uploadRequest.Header.Set("Content-Type", writer.FormDataContentType())
	uploadResponse := httptest.NewRecorder()
	server.ServeHTTP(uploadResponse, uploadRequest)
	if uploadResponse.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, body = %s", uploadResponse.Code, uploadResponse.Body.String())
	}
	var attachment domain.Attachment
	if err := json.NewDecoder(uploadResponse.Body).Decode(&attachment); err != nil {
		t.Fatal(err)
	}
	if attachment.Name != "brief_.txt" || attachment.MessageID != "" || attachment.StoragePath != "" {
		t.Fatalf("upload response attachment = %#v", attachment)
	}

	stored, err := instance.GetAttachment(t.Context(), attachment.ID)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(stored.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "attachment contents" {
		t.Fatalf("stored attachment content = %q", content)
	}

	downloadRequest := httptest.NewRequest(http.MethodGet, "/api/attachments/"+attachment.ID, nil)
	downloadResponse := httptest.NewRecorder()
	server.ServeHTTP(downloadResponse, downloadRequest)
	if downloadResponse.Code != http.StatusOK || downloadResponse.Body.String() != "attachment contents" {
		t.Fatalf("download status = %d, body = %q", downloadResponse.Code, downloadResponse.Body.String())
	}
	if got := downloadResponse.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("attachment nosniff header = %q", got)
	}
	if got := downloadResponse.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") {
		t.Fatalf("attachment disposition = %q", got)
	}

	messageRequest := httptest.NewRequest(http.MethodPost, "/api/messages", strings.NewReader(`{"conversation_id":"`+conversation.ID+`","content":"Review this","provider":"pi","permission_mode":"workspace","attachment_ids":["`+attachment.ID+`"]}`))
	messageRequest.Header.Set("Content-Type", "application/json")
	messageResponse := httptest.NewRecorder()
	server.ServeHTTP(messageResponse, messageRequest)
	if messageResponse.Code != http.StatusAccepted {
		t.Fatalf("create message status = %d, body = %s", messageResponse.Code, messageResponse.Body.String())
	}
	var created struct {
		Message domain.Message `json:"message"`
	}
	if err := json.NewDecoder(messageResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	claimed, err := instance.GetAttachment(t.Context(), attachment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.MessageID != created.Message.ID {
		t.Fatalf("claimed attachment message ID = %q, want %q", claimed.MessageID, created.Message.ID)
	}

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/attachments/"+attachment.ID, nil)
	deleteResponse := httptest.NewRecorder()
	server.ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusConflict {
		t.Fatalf("delete sent attachment status = %d, body = %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	if _, err := os.Stat(stored.StoragePath); err != nil {
		t.Fatalf("sent attachment file should remain available: %v", err)
	}
}

func TestAttachmentUploadUsesDetectedContentType(t *testing.T) {
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
	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	if err := writer.WriteField("conversation_id", conversation.ID); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="file"; filename="report.txt"`},
		"Content-Type":        {"text/plain"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("\x89PNG\r\n\x1a\nnot really a full image")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	server := (&Server{Store: instance, HarnessWorkdir: t.TempDir(), UploadDir: filepath.Join(t.TempDir(), "uploads")}).Handler()
	request := httptest.NewRequest(http.MethodPost, "/api/attachments", &multipartBody)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, body = %s", response.Code, response.Body.String())
	}
	var attachment domain.Attachment
	if err := json.NewDecoder(response.Body).Decode(&attachment); err != nil {
		t.Fatal(err)
	}
	stored, err := instance.GetAttachment(t.Context(), attachment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MediaType != "image/png" {
		t.Fatalf("stored media type = %q, want detected image/png instead of client header", stored.MediaType)
	}
}
