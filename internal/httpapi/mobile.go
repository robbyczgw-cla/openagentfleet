package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/compute"
	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

const (
	mobileAuthVersion          = domain.RemoteAuthVersionBearer
	mobileBearerLifetime       = 30 * 24 * time.Hour
	mobileBearerRandomBytes    = 32
	mobileJSONBodyBytes        = 32 << 10
	mobilePairingBodyBytes     = 4 << 10
	mobileAuthRecheckInterval  = 500 * time.Millisecond
	mobileEventKeepAlive       = 20 * time.Second
	mobileComputerLeaseTTL     = 30 * time.Second
	maxMobilePairingFieldBytes = 256
)

type mobilePairRequest struct {
	GrantID       string `json:"grant_id"`
	PairingSecret string `json:"pairing_secret"`
	DeviceName    string `json:"device_name"`
	Platform      string `json:"platform"`
}

type mobilePairResponse struct {
	AuthVersion int                 `json:"auth_version"`
	TokenType   string              `json:"token_type"`
	AccessToken string              `json:"access_token"`
	ExpiresAt   string              `json:"expires_at"`
	Device      domain.RemoteDevice `json:"device"`
	HostID      string              `json:"host_id"`
}

type mobileComputerStatus struct {
	Available      bool   `json:"available"`
	Running        bool   `json:"running"`
	BrowserReady   bool   `json:"browser_ready"`
	DesktopReady   bool   `json:"desktop_ready"`
	ControlHeld    bool   `json:"control_held,omitempty"`
	LeaseExpiresAt string `json:"control_lease_expires_at,omitempty"`
	Title          string `json:"title,omitempty"`
	Detail         string `json:"detail,omitempty"`
	ViewportWidth  int    `json:"viewport_width,omitempty"`
	ViewportHeight int    `json:"viewport_height,omitempty"`
}

type mobileBootstrapResponse struct {
	AuthVersion   int                   `json:"auth_version"`
	HostID        string                `json:"host_id"`
	Device        domain.RemoteDevice   `json:"device"`
	Conversations []domain.Conversation `json:"conversations"`
	Conversation  domain.Conversation   `json:"conversation"`
	Messages      []domain.Message      `json:"messages"`
	Runs          []domain.MobileRun    `json:"runs"`
	Computer      mobileComputerStatus  `json:"computer"`
	EventCursor   uint64                `json:"event_cursor"`
}

type mobileMessageRequest struct {
	ConversationID string `json:"conversation_id"`
	Content        string `json:"content"`
}

type mobilePairingAdminRequest struct {
	Scope      string `json:"scope"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

// Mobile computer control is intentionally click-only for the first remote
// slice. It must not inherit the broader browser action contract: mobile
// clients never receive the trusted Mac's secure secret handoff boundary.
type mobileComputerClickRequest struct {
	Action string  `json:"action"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
}

// MobileHandler is an independent, deny-by-default remote surface for the
// future loopback 4318 listener. It never delegates to Handler or RemoteToken.
func (s *Server) MobileHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if strings.TrimSpace(r.Header.Get("Origin")) != "" {
			s.mobileError(w, http.StatusForbidden, "origin is not allowed")
			return
		}

		switch {
		case r.URL.Path == "/api/v1/meta" && r.Method == http.MethodGet:
			s.mobileMeta(w, r)
			return
		case r.URL.Path == "/api/v1/pair" && r.Method == http.MethodPost:
			s.mobilePair(w, r)
			return
		case r.URL.Path == "/api/v1/bootstrap" && r.Method == http.MethodGet,
			r.URL.Path == "/api/v1/conversations" && r.Method == http.MethodGet,
			r.URL.Path == "/api/v1/messages" && r.Method == http.MethodPost,
			r.URL.Path == "/api/v1/computer" && r.Method == http.MethodGet,
			r.URL.Path == "/api/v1/computer/frame" && r.Method == http.MethodGet,
			r.URL.Path == "/api/v1/computer/control" && r.Method == http.MethodPost,
			r.URL.Path == "/api/v1/computer/browser/action" && r.Method == http.MethodPost,
			r.URL.Path == "/api/v1/events" && r.Method == http.MethodGet,
			r.URL.Path == "/api/v1/session/logout" && r.Method == http.MethodPost:
			// Explicit allowlist continues below after device authentication.
		default:
			s.mobileError(w, http.StatusNotFound, "not found")
			return
		}

		session, rawBearer, ok := s.authenticateMobileRequest(r)
		if !ok {
			s.mobileUnauthorized(w)
			return
		}
		if err := s.Store.TouchRemoteDeviceLastUsed(r.Context(), session.Device.ID); err != nil {
			s.mobileUnauthorized(w)
			return
		}

		switch r.URL.Path {
		case "/api/v1/bootstrap":
			s.mobileBootstrap(w, r, session)
		case "/api/v1/conversations":
			s.mobileConversations(w, r)
		case "/api/v1/messages":
			s.mobileCreateMessage(w, r, session.Device)
		case "/api/v1/computer":
			s.writeJSON(w, http.StatusOK, s.mobileComputerForToken(r.Context(), rawBearer))
		case "/api/v1/computer/frame":
			s.mobileComputerFrame(w, r)
		case "/api/v1/computer/control":
			s.mobileComputerControl(w, r, session.Device, rawBearer)
		case "/api/v1/computer/browser/action":
			s.mobileComputerBrowserAction(w, r, session.Device, rawBearer)
		case "/api/v1/events":
			s.mobileEvents(w, r, rawBearer)
		case "/api/v1/session/logout":
			s.releaseMobileComputerLeaseForDevice(session.Device.ID, rawBearer)
			if err := s.Store.RevokeMobileCredential(r.Context(), session.Device.ID, rawBearer); err != nil {
				s.mobileUnauthorized(w)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

func (s *Server) mobileMeta(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		s.mobileError(w, http.StatusServiceUnavailable, "mobile service unavailable")
		return
	}
	hostID, err := s.Store.GetOrCreateRemoteHostID(r.Context())
	if err != nil {
		s.mobileError(w, http.StatusServiceUnavailable, "mobile service unavailable")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"service":      "openagentfleet",
		"api_version":  1,
		"auth_version": mobileAuthVersion,
		"host_id":      hostID,
	})
}

func (s *Server) mobilePair(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		s.mobileError(w, http.StatusServiceUnavailable, "pairing unavailable")
		return
	}
	var request mobilePairRequest
	if err := decodeStrictMobileJSON(w, r, mobilePairingBodyBytes, &request); err != nil || !validMobilePairRequest(request) {
		s.mobileError(w, http.StatusBadRequest, "invalid pairing request")
		return
	}
	hostID, err := s.Store.GetOrCreateRemoteHostID(r.Context())
	if err != nil {
		s.mobileError(w, http.StatusServiceUnavailable, "pairing unavailable")
		return
	}
	rawBearer, err := newMobileBearer()
	if err != nil {
		s.mobileError(w, http.StatusServiceUnavailable, "pairing unavailable")
		return
	}
	expiresAt := time.Now().UTC().Add(mobileBearerLifetime)
	device, err := s.Store.ClaimRemotePairingGrant(
		r.Context(), request.GrantID, request.PairingSecret,
		strings.TrimSpace(request.DeviceName), strings.ToLower(strings.TrimSpace(request.Platform)),
		rawBearer, expiresAt,
	)
	if err != nil {
		s.mobileError(w, http.StatusUnauthorized, "pairing failed")
		return
	}
	s.writeJSON(w, http.StatusCreated, mobilePairResponse{
		AuthVersion: mobileAuthVersion,
		TokenType:   "Bearer",
		AccessToken: rawBearer,
		ExpiresAt:   expiresAt.Format(time.RFC3339Nano),
		Device:      device,
		HostID:      hostID,
	})
}

func (s *Server) mobileBootstrap(w http.ResponseWriter, r *http.Request, session domain.RemoteSession) {
	snapshot, err := s.Store.MobileBootstrapSnapshot(r.Context(), strings.TrimSpace(r.URL.Query().Get("conversation_id")))
	if errors.Is(err, store.ErrMobileConversationNotFound) {
		s.mobileError(w, http.StatusNotFound, "conversation not found")
		return
	}
	if err != nil {
		s.mobileError(w, http.StatusServiceUnavailable, "mobile state unavailable")
		return
	}
	visibleConversations, err := s.visibleConversations(r.Context(), snapshot.Conversation.BotID)
	if err != nil {
		s.mobileError(w, http.StatusServiceUnavailable, "conversations unavailable")
		return
	}
	hostID, err := s.Store.GetOrCreateRemoteHostID(r.Context())
	if err != nil {
		s.mobileError(w, http.StatusServiceUnavailable, "mobile state unavailable")
		return
	}
	s.writeJSON(w, http.StatusOK, mobileBootstrapResponse{
		AuthVersion:   session.AuthVersion,
		HostID:        hostID,
		Device:        session.Device,
		Conversations: visibleConversations,
		Conversation:  snapshot.Conversation,
		Messages:      snapshot.Messages,
		Runs:          snapshot.Runs,
		Computer:      s.mobileComputer(r.Context()),
		EventCursor:   snapshot.EventCursor,
	})
}

func (s *Server) mobileConversations(w http.ResponseWriter, r *http.Request) {
	items, err := s.visibleConversations(r.Context(), "")
	if err != nil {
		s.mobileError(w, http.StatusServiceUnavailable, "conversations unavailable")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"conversations": items})
}

func (s *Server) mobileCreateMessage(w http.ResponseWriter, r *http.Request, device domain.RemoteDevice) {
	if device.ScopeProfile != domain.RemoteScopeController && device.ScopeProfile != domain.RemoteScopeOwner {
		s.mobileError(w, http.StatusForbidden, "device is read only")
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		s.mobileError(w, http.StatusBadRequest, "idempotency key is required")
		return
	}
	var request mobileMessageRequest
	if err := decodeStrictMobileJSON(w, r, mobileJSONBodyBytes, &request); err != nil {
		s.mobileError(w, http.StatusBadRequest, "invalid message request")
		return
	}
	response, run, queuedEvent, created, err := s.Store.CreateMobileMessageRun(
		r.Context(), device.ID, idempotencyKey, request.ConversationID, request.Content,
	)
	if errors.Is(err, store.ErrMobileIdempotencyConflict) {
		s.mobileError(w, http.StatusConflict, "idempotency key conflict")
		return
	}
	if errors.Is(err, store.ErrMobileConversationNotFound) {
		s.mobileError(w, http.StatusNotFound, "conversation not found")
		return
	}
	if errors.Is(err, store.ErrRemoteCredentialInvalid) {
		s.mobileUnauthorized(w)
		return
	}
	if err != nil {
		s.mobileError(w, http.StatusBadRequest, "invalid message request")
		return
	}
	if created {
		if s.Broker != nil {
			s.Broker.Publish(queuedEvent)
		}
		if session, sessionErr := s.Store.GetHarnessSession(r.Context(), run.ConversationID, "grok"); sessionErr == nil {
			run.SessionID = session.NativeSessionID
		}
		s.startMobileRun(run)
	}
	s.writeJSON(w, http.StatusAccepted, response)
}

func (s *Server) startMobileRun(run domain.Run) {
	if !s.AllowHarnessExecution {
		_ = s.commitTerminalRunLifecycleEvent(run, "blocked", harness.ErrExecutionDisabled.Error(), "run.blocked", `{"reason":"execution_disabled"}`)
		return
	}
	if s.Runner == nil {
		_ = s.commitTerminalRunLifecycleEvent(run, "failed", "harness runner unavailable", "run.failed", `{"reason":"runner_unavailable"}`)
		return
	}
	s.executeRun(run, "", "", "", "", "", domain.AgentWebSearchLive, 0, nil)
}

func (s *Server) mobileComputer(ctx context.Context) mobileComputerStatus {
	if s.Docker == nil {
		return mobileComputerStatus{Detail: "Agent Computer unavailable"}
	}
	status := s.computerStatus(ctx)
	detail := "Agent Computer is ready"
	switch {
	case !status.Available:
		detail = "Agent Computer runtime unavailable"
	case !status.Running:
		detail = "Agent Computer is stopped"
	case !status.BrowserReady || !status.DesktopReady:
		detail = "Agent Computer is starting"
	}
	return mobileComputerStatus{
		Available: status.Available, Running: status.Running,
		BrowserReady: status.BrowserReady, DesktopReady: status.DesktopReady,
		Title: status.Title, Detail: detail,
		ViewportWidth: status.ViewportWidth, ViewportHeight: status.ViewportHeight,
	}
}

func (s *Server) mobileComputerFrame(w http.ResponseWriter, r *http.Request) {
	if s.Docker == nil {
		s.mobileError(w, http.StatusServiceUnavailable, "computer unavailable")
		return
	}
	status := s.computerStatus(r.Context())
	if !status.Running || !status.BrowserReady {
		s.mobileError(w, http.StatusConflict, "computer frame unavailable")
		return
	}
	frame, err := s.Docker.Frame(r.Context())
	if err != nil {
		s.mobileError(w, http.StatusBadGateway, "computer frame unavailable")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(frame)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(frame)
}

func mobileComputerControlAllowed(device domain.RemoteDevice) bool {
	return device.ScopeProfile == domain.RemoteScopeController || device.ScopeProfile == domain.RemoteScopeOwner
}

func (s *Server) mobileComputerControl(w http.ResponseWriter, r *http.Request, device domain.RemoteDevice, rawBearer string) {
	if !mobileComputerControlAllowed(device) {
		s.mobileError(w, http.StatusForbidden, "device is read only")
		return
	}
	var request takeoverRequest
	if err := decodeStrictMobileJSON(w, r, mobileJSONBodyBytes, &request); err != nil {
		s.mobileError(w, http.StatusBadRequest, "invalid computer control request")
		return
	}
	s.computerActionMu.Lock()
	defer s.computerActionMu.Unlock()
	now := time.Now().UTC()
	s.expireMobileComputerLeaseLocked(now)
	s.remoteComputerLeaseMu.Lock()
	if request.Enabled {
		if s.remoteComputerOwner != "" && s.remoteComputerOwner != rawBearer {
			s.remoteComputerLeaseMu.Unlock()
			s.mobileError(w, http.StatusLocked, "computer control is held by another device")
			return
		}
		s.computerMu.RLock()
		localTakeover, agentControl := s.computerTakeover, s.computerAgentControl
		s.computerMu.RUnlock()
		if (localTakeover && s.remoteComputerOwner != rawBearer) || agentControl {
			s.remoteComputerLeaseMu.Unlock()
			s.mobileError(w, http.StatusLocked, "computer is currently controlled on the Mac")
			return
		}
		s.remoteComputerOwner = rawBearer
		s.remoteComputerDeviceID = device.ID
		s.remoteComputerExpiresAt = now.Add(mobileComputerLeaseTTL)
	} else if s.remoteComputerOwner != rawBearer {
		s.remoteComputerLeaseMu.Unlock()
		s.mobileError(w, http.StatusLocked, "this device does not hold computer control")
		return
	} else {
		s.remoteComputerOwner = ""
		s.remoteComputerDeviceID = ""
		s.remoteComputerExpiresAt = time.Time{}
	}
	leaseExpiresAt := s.remoteComputerExpiresAt
	leaseHeld := request.Enabled
	s.remoteComputerLeaseMu.Unlock()

	s.computerMu.Lock()
	if request.Enabled {
		s.computerTakeover = true
		s.computerAgentControl = false
	} else {
		s.computerTakeover = false
		s.computerAgentControl = false
	}
	s.computerMu.Unlock()
	s.cancelPendingSecretHandoffs()
	status := s.mobileComputer(r.Context())
	status.ControlHeld = leaseHeld
	if leaseHeld {
		status.LeaseExpiresAt = leaseExpiresAt.Format(time.RFC3339Nano)
	}
	s.writeJSON(w, http.StatusOK, status)
}

func (s *Server) mobileComputerBrowserAction(w http.ResponseWriter, r *http.Request, device domain.RemoteDevice, rawBearer string) {
	s.mobileComputerAction(w, r, device, rawBearer)
}

func (s *Server) mobileComputerAction(w http.ResponseWriter, r *http.Request, device domain.RemoteDevice, rawBearer string) {
	if !mobileComputerControlAllowed(device) {
		s.mobileError(w, http.StatusForbidden, "device is read only")
		return
	}
	if s.Docker == nil {
		s.mobileError(w, http.StatusServiceUnavailable, "computer unavailable")
		return
	}
	s.computerActionMu.Lock()
	defer s.computerActionMu.Unlock()
	leaseExpiresAt, ok := s.mobileComputerLeaseValidLocked(rawBearer)
	if !ok {
		s.mobileError(w, http.StatusLocked, "take control on this device before sending computer actions")
		return
	}
	var click mobileComputerClickRequest
	if err := decodeStrictMobileJSON(w, r, mobileJSONBodyBytes, &click); err != nil || click.Action != "click" {
		s.mobileError(w, http.StatusBadRequest, "invalid computer action")
		return
	}
	action := compute.BrowserAction{Action: click.Action, X: click.X, Y: click.Y}
	actionContext, cancel := context.WithDeadline(r.Context(), leaseExpiresAt)
	defer cancel()
	view, err := s.Docker.Action(actionContext, action)
	if err != nil {
		s.mobileError(w, http.StatusBadGateway, "computer action failed")
		return
	}
	if !time.Now().UTC().Before(leaseExpiresAt) {
		s.expireMobileComputerLeaseLocked(time.Now().UTC())
		s.mobileError(w, http.StatusLocked, "computer control lease expired")
		return
	}
	leaseExpiresAt = s.refreshMobileComputerLeaseLocked(rawBearer)
	status := s.mobileComputer(r.Context())
	status.ControlHeld = true
	status.LeaseExpiresAt = leaseExpiresAt.Format(time.RFC3339Nano)
	status.Title = view.Title
	status.ViewportWidth = view.ViewportWidth
	status.ViewportHeight = view.ViewportHeight
	s.writeJSON(w, http.StatusOK, status)
}

func (s *Server) mobileComputerForToken(ctx context.Context, rawBearer string) mobileComputerStatus {
	s.computerActionMu.Lock()
	defer s.computerActionMu.Unlock()
	s.expireMobileComputerLeaseLocked(time.Now().UTC())
	status := s.mobileComputer(ctx)
	s.remoteComputerLeaseMu.Lock()
	if s.remoteComputerOwner == rawBearer && time.Now().UTC().Before(s.remoteComputerExpiresAt) {
		status.ControlHeld = true
		status.LeaseExpiresAt = s.remoteComputerExpiresAt.Format(time.RFC3339Nano)
	}
	s.remoteComputerLeaseMu.Unlock()
	return status
}

// mobileComputerLeaseValidLocked and refreshMobileComputerLeaseLocked are
// called while computerActionMu is held. That serializes lease expiry/control
// changes with the actual Docker action, so a released lease cannot authorize
// an action that was already past its check.
func (s *Server) mobileComputerLeaseValidLocked(rawBearer string) (time.Time, bool) {
	now := time.Now().UTC()
	s.expireMobileComputerLeaseLocked(now)
	s.remoteComputerLeaseMu.Lock()
	defer s.remoteComputerLeaseMu.Unlock()
	if s.remoteComputerOwner != rawBearer || !now.Before(s.remoteComputerExpiresAt) {
		return time.Time{}, false
	}
	s.computerMu.RLock()
	takeover := s.computerTakeover
	s.computerMu.RUnlock()
	return s.remoteComputerExpiresAt, takeover
}

func (s *Server) refreshMobileComputerLeaseLocked(rawBearer string) time.Time {
	s.remoteComputerLeaseMu.Lock()
	defer s.remoteComputerLeaseMu.Unlock()
	if s.remoteComputerOwner != rawBearer {
		return time.Time{}
	}
	s.remoteComputerExpiresAt = time.Now().UTC().Add(mobileComputerLeaseTTL)
	return s.remoteComputerExpiresAt
}

func (s *Server) clearMobileComputerLease() {
	s.remoteComputerLeaseMu.Lock()
	s.remoteComputerOwner = ""
	s.remoteComputerDeviceID = ""
	s.remoteComputerExpiresAt = time.Time{}
	s.remoteComputerLeaseMu.Unlock()
}

// expireMobileComputerLeaseLocked clears both halves of the mobile takeover
// state. All callers hold computerActionMu, so a new controller cannot enter
// while the old lease is being torn down.
func (s *Server) expireMobileComputerLeaseLocked(now time.Time) {
	s.remoteComputerLeaseMu.Lock()
	expired := s.remoteComputerOwner != "" && !now.Before(s.remoteComputerExpiresAt)
	if expired {
		s.remoteComputerOwner = ""
		s.remoteComputerDeviceID = ""
		s.remoteComputerExpiresAt = time.Time{}
	}
	s.remoteComputerLeaseMu.Unlock()
	if !expired {
		return
	}
	s.computerMu.Lock()
	wasMobileTakeover := s.computerTakeover && !s.computerAgentControl
	s.computerTakeover = false
	s.computerMu.Unlock()
	if wasMobileTakeover {
		s.cancelPendingSecretHandoffs()
	}
}

func (s *Server) releaseMobileComputerLeaseForDevice(deviceID, rawBearer string) {
	s.computerActionMu.Lock()
	defer s.computerActionMu.Unlock()
	s.remoteComputerLeaseMu.Lock()
	owned := (rawBearer != "" && s.remoteComputerOwner == rawBearer) || (deviceID != "" && s.remoteComputerDeviceID == deviceID)
	s.remoteComputerLeaseMu.Unlock()
	if !owned {
		return
	}
	s.clearMobileComputerLease()
	s.computerMu.Lock()
	s.computerTakeover = false
	s.computerMu.Unlock()
	s.cancelPendingSecretHandoffs()
}

func (s *Server) mobileEvents(w http.ResponseWriter, r *http.Request, rawBearer string) {
	if s.Broker == nil {
		s.mobileError(w, http.StatusServiceUnavailable, "event stream unavailable")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.mobileError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	afterValues := r.URL.Query()["after"]
	if len(afterValues) != 1 {
		writeMobileReset(w, flusher)
		return
	}
	afterText := afterValues[0]
	after, err := strconv.ParseUint(afterText, 10, 64)
	if err != nil || afterText == "" || after > math.MaxInt64 {
		writeMobileReset(w, flusher)
		return
	}

	channel, unsubscribe := s.Broker.Subscribe(r.Context())
	defer unsubscribe()
	valid, err := s.Store.ValidateMobileCursor(r.Context(), after)
	if err != nil {
		s.mobileError(w, http.StatusServiceUnavailable, "event stream unavailable")
		return
	}
	if !valid {
		writeMobileReset(w, flusher)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, ": connected\n\n")
	flusher.Flush()
	lastCursor := after
	catchUp := func() bool {
		for {
			items, listErr := s.Store.ListMobileEventsAfter(r.Context(), lastCursor)
			if listErr != nil {
				return false
			}
			if len(items) == 0 {
				return true
			}
			for _, item := range items {
				if item.Cursor <= lastCursor {
					continue
				}
				if !writeMobileEvent(w, flusher, item) {
					return false
				}
				lastCursor = item.Cursor
			}
			if len(items) < 256 {
				return true
			}
		}
	}
	if !catchUp() {
		return
	}

	recheck := time.NewTicker(mobileAuthRecheckInterval)
	keepAlive := time.NewTicker(mobileEventKeepAlive)
	defer recheck.Stop()
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-recheck.C:
			if _, err := s.Store.AuthenticateMobileCredential(r.Context(), rawBearer); err != nil {
				return
			}
			if !catchUp() {
				return
			}
		case <-keepAlive.C:
			if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case _, open := <-channel:
			if !open || !catchUp() {
				return
			}
		}
	}
}

func writeMobileReset(w http.ResponseWriter, flusher http.Flusher) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "event: ofb.reset\ndata: {\"reason\":\"cursor_reset_required\"}\n\n")
	flusher.Flush()
}

func writeMobileEvent(w http.ResponseWriter, flusher http.Flusher, item domain.MobileEventRecord) bool {
	status := strings.TrimPrefix(item.Type, "run.")
	envelope := domain.MobileEventEnvelope{
		Cursor: item.Cursor, Type: item.Type, RunID: item.RunID, ConversationID: item.ConversationID,
		Data: json.RawMessage(fmt.Sprintf(`{"status":%q}`, status)),
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: ofb.event\ndata: %s\n\n", item.Cursor, payload); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func (s *Server) authenticateMobileRequest(r *http.Request) (domain.RemoteSession, string, bool) {
	if s.Store == nil {
		return domain.RemoteSession{}, "", false
	}
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) || strings.TrimSpace(header) != header {
		return domain.RemoteSession{}, "", false
	}
	rawBearer := strings.TrimPrefix(header, prefix)
	if rawBearer == "" || strings.Contains(rawBearer, " ") {
		return domain.RemoteSession{}, "", false
	}
	session, err := s.Store.AuthenticateMobileCredential(r.Context(), rawBearer)
	if err != nil {
		return domain.RemoteSession{}, "", false
	}
	return session, rawBearer, true
}

func (s *Server) mobileUnauthorized(w http.ResponseWriter) {
	s.mobileError(w, http.StatusUnauthorized, "unauthorized")
}

func (s *Server) mobileError(w http.ResponseWriter, status int, message string) {
	s.writeJSON(w, status, map[string]string{"error": message})
}

func (s *Server) mobilePairingAdmin(w http.ResponseWriter, r *http.Request) {
	if !mobileLoopbackRequest(r) {
		s.writeErrorStatus(w, http.StatusForbidden, errors.New("local access required"))
		return
	}
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("mobile administration unavailable"))
		return
	}
	var request mobilePairingAdminRequest
	if err := decodeStrictMobileJSON(w, r, mobilePairingBodyBytes, &request); err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, errors.New("invalid pairing request"))
		return
	}
	request.Scope = strings.ToLower(strings.TrimSpace(request.Scope))
	if request.Scope == "" {
		request.Scope = domain.RemoteScopeObserver
	}
	if request.Scope != domain.RemoteScopeObserver && request.Scope != domain.RemoteScopeController {
		s.writeErrorStatus(w, http.StatusBadRequest, errors.New("scope must be observer or controller"))
		return
	}
	ttl := time.Duration(request.TTLSeconds) * time.Second
	if request.TTLSeconds < 0 || request.TTLSeconds > int(store.MaxRemotePairingTTL/time.Second) {
		s.writeErrorStatus(w, http.StatusBadRequest, errors.New("pairing ttl is invalid"))
		return
	}
	hostID, err := s.Store.GetOrCreateRemoteHostID(r.Context())
	if err != nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("mobile administration unavailable"))
		return
	}
	grant, secret, err := s.Store.CreateRemotePairingGrant(r.Context(), request.Scope, ttl)
	if err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, errors.New("pairing could not be created"))
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"grant": grant, "pairing_secret": secret, "host_id": hostID,
	})
}

func (s *Server) mobileDevicesAdmin(w http.ResponseWriter, r *http.Request) {
	if !mobileLoopbackRequest(r) {
		s.writeErrorStatus(w, http.StatusForbidden, errors.New("local access required"))
		return
	}
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("mobile administration unavailable"))
		return
	}
	items, err := s.Store.ListRemoteDevices(r.Context())
	if err != nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("mobile devices unavailable"))
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"devices": items})
}

func (s *Server) mobileRevokeDeviceAdmin(w http.ResponseWriter, r *http.Request) {
	if !mobileLoopbackRequest(r) {
		s.writeErrorStatus(w, http.StatusForbidden, errors.New("local access required"))
		return
	}
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("mobile administration unavailable"))
		return
	}
	deviceID, ok := mobileRevokeDeviceID(r.URL.Path)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	s.releaseMobileComputerLeaseForDevice(deviceID, "")
	if err := s.Store.RevokeRemoteDevice(r.Context(), deviceID); errors.Is(err, store.ErrRemoteDeviceNotFound) {
		s.writeErrorStatus(w, http.StatusNotFound, errors.New("device not found"))
		return
	} else if err != nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("device revocation failed"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func mobileRevokeDeviceID(path string) (string, bool) {
	const prefix = "/api/mobile/devices/"
	const suffix = "/revoke"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	deviceID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if deviceID == "" || strings.Contains(deviceID, "/") {
		return "", false
	}
	return deviceID, true
}

func mobileLoopbackRequest(r *http.Request) bool {
	remoteAddress := strings.TrimSpace(r.RemoteAddr)
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		host = remoteAddress
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func decodeStrictMobileJSON(w http.ResponseWriter, r *http.Request, limit int64, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain exactly one JSON value")
	}
	return nil
}

func validMobilePairRequest(request mobilePairRequest) bool {
	request.GrantID = strings.TrimSpace(request.GrantID)
	request.DeviceName = strings.TrimSpace(request.DeviceName)
	request.Platform = strings.ToLower(strings.TrimSpace(request.Platform))
	if request.GrantID == "" || request.PairingSecret == "" || request.DeviceName == "" {
		return false
	}
	if len(request.GrantID) > maxMobilePairingFieldBytes || len(request.PairingSecret) > maxMobilePairingFieldBytes || len([]rune(request.DeviceName)) > 128 {
		return false
	}
	return domain.ValidRemotePlatform(request.Platform)
}

func newMobileBearer() (string, error) {
	buffer := make([]byte, mobileBearerRandomBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
