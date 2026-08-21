package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
)

const (
	webhookJSONBodyBytes   = 4 << 10
	webhookBearerPrefix    = "Bearer "
	maxWebhookIdempotency  = 128
)

// WebhookHandler is a deny-by-default loopback delivery surface. It never
// delegates to Handler and never accepts the desktop RemoteToken as a
// substitute for the per-routine hashed secret.
func (s *Server) WebhookHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if strings.TrimSpace(r.Header.Get("Origin")) != "" {
			s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin is not allowed"})
			return
		}
		routineID, ok := webhookRoutineID(r.URL.Path)
		if !ok || r.Method != http.MethodPost {
			s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		s.deliverRoutineWebhook(w, r, routineID)
	})
}

func webhookRoutineID(path string) (string, bool) {
	const prefix = "/hooks/routines/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	routineID := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if routineID == "" || strings.Contains(routineID, "/") {
		return "", false
	}
	return routineID, true
}

func (s *Server) deliverRoutineWebhook(w http.ResponseWriter, r *http.Request, routineID string) {
	if s.Store == nil {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "webhook unavailable"})
		return
	}
	secret := webhookBearerSecret(r.Header.Get("Authorization"))
	if err := s.Store.AuthenticateRoutineWebhook(r.Context(), routineID, secret); err != nil {
		s.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	// Bound and discard the body. Delivery does not inject caller JSON into
	// the Agent prompt; the routine description remains the task.
	r.Body = http.MaxBytesReader(w, r.Body, webhookJSONBodyBytes)
	_, _ = io.Copy(io.Discard, r.Body)

	preferences, err := s.Store.GetPreferences(r.Context())
	if err != nil {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "webhook unavailable"})
		return
	}
	if !preferences.Normalize().Features.Routines {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "routines are disabled"})
		return
	}
	item, err := s.Store.GetRoutine(r.Context(), routineID)
	if err != nil {
		s.writeRoutineError(w, err)
		return
	}
	now := s.currentTime()
	approvalID, occurrenceKey, err := s.routineWebhookClaimApprovalID(r.Context(), item, now)
	if errors.Is(err, errRoutineWaitingApproval) {
		s.writeJSON(w, http.StatusAccepted, map[string]any{
			"routine":              item,
			"waiting_for_approval": true,
		})
		return
	}
	if err != nil {
		s.writeRoutineError(w, err)
		return
	}
	idempotency := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotency == "" {
		idempotency = strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	}
	if idempotency == "" {
		idempotency = id.New("claim")
	}
	if len(idempotency) > maxWebhookIdempotency {
		s.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "idempotency key is too long"})
		return
	}
	claimed, err := s.Store.ClaimWebhookRoutineRun(r.Context(), domain.RoutineClaim{
		RoutineID:      item.ID,
		LeaseOwner:     s.routineOwner(),
		LeaseDuration:  s.leaseDuration(),
		IdempotencyKey: idempotency,
		ApprovalID:     approvalID,
		OccurrenceKey:  occurrenceKey,
		Now:            now,
	})
	if err != nil {
		s.writeRoutineError(w, err)
		return
	}
	s.launchRoutineOccurrence(r.Context(), item, claimed)
	updated, getErr := s.Store.GetRoutine(r.Context(), item.ID)
	if getErr != nil {
		updated = item
	}
	s.writeJSON(w, http.StatusAccepted, map[string]any{"routine": updated, "run": claimed})
}

func webhookBearerSecret(header string) string {
	if !strings.HasPrefix(header, webhookBearerPrefix) || strings.TrimSpace(header) != header {
		return ""
	}
	return strings.TrimPrefix(header, webhookBearerPrefix)
}
