package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

type memoryProposalFieldsRequest struct {
	Category  *domain.MemoryCategory `json:"category"`
	Content   *string                `json:"content"`
	Priority  *int                   `json:"priority"`
	ExpiresAt memoryExpiryUpdate     `json:"expires_at"`
}

type agentMemoryProposalRequest struct {
	Category  domain.MemoryCategory `json:"category"`
	Content   string                `json:"content"`
	Priority  int                   `json:"priority"`
	ExpiresAt string                `json:"expires_at"`
}

func (s *Server) createAgentMemoryProposal(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(s.RemoteToken) == "" {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("controller authentication is required for memory proposals"))
		return
	}
	run, err := s.authorizedMemoryProposalRun(r)
	if err != nil {
		s.writeErrorStatus(w, http.StatusUnauthorized, err)
		return
	}
	preferences, err := s.Store.GetPreferences(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	if !preferences.Normalize().Features.MemoryProposals {
		s.writeErrorStatus(w, http.StatusForbidden, errors.New("memory proposals are disabled"))
		return
	}
	var request agentMemoryProposalRequest
	if err := decodeStrictJSON(w, r, &request); err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	if request.Priority == 0 {
		request.Priority = 3
	}
	source, err := s.Store.MemoryProposalSourceMessage(r.Context(), run)
	if err != nil {
		s.writeErrorStatus(w, http.StatusConflict, err)
		return
	}
	proposal, err := s.Store.CreateMemoryProposal(r.Context(), domain.MemoryProposalDraft{
		BotID: run.BotID, SourceRunID: run.ID, SourceMessageID: source.ID,
		Category: request.Category, Content: request.Content, Priority: request.Priority, ExpiresAt: request.ExpiresAt,
	})
	if err != nil {
		s.writeMemoryProposalStoreError(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, proposal)
}

func (s *Server) listMemoryProposals(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeMemoryProposalReviewer(w, r) {
		return
	}
	botID := strings.TrimSpace(r.URL.Query().Get("bot_id"))
	status := domain.MemoryProposalStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	items, err := s.Store.ListMemoryProposals(r.Context(), botID, status)
	if err != nil {
		s.writeMemoryProposalStoreError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"proposals": items})
}

func (s *Server) patchMemoryProposal(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeMemoryProposalReviewer(w, r) {
		return
	}
	proposalID, action, err := memoryProposalPath(r)
	if err != nil || action != "" {
		if err == nil {
			err = errors.New("memory proposal path is invalid")
		}
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	existing, err := s.Store.GetMemoryProposal(r.Context(), proposalID)
	if err != nil {
		s.writeMemoryProposalStoreError(w, err)
		return
	}
	fields, provided, err := decodeMemoryProposalFields(w, r, existing)
	if err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	if !provided {
		s.writeErrorStatus(w, http.StatusBadRequest, errors.New("memory proposal update is required"))
		return
	}
	updated, err := s.Store.UpdateMemoryProposal(r.Context(), proposalID, fields)
	if err != nil {
		s.writeMemoryProposalStoreError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, updated)
}

func (s *Server) acceptMemoryProposal(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeMemoryProposalReviewer(w, r) {
		return
	}
	proposalID, action, err := memoryProposalPath(r)
	if err != nil || action != "accept" {
		if err == nil {
			err = errors.New("memory proposal accept path is invalid")
		}
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	existing, err := s.Store.GetMemoryProposal(r.Context(), proposalID)
	if err != nil {
		s.writeMemoryProposalStoreError(w, err)
		return
	}
	var replacement *domain.MemoryProposalUpdate
	if r.Body != nil {
		var request memoryProposalFieldsRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		decodeErr := decoder.Decode(&request)
		if decodeErr != nil && !errors.Is(decodeErr, io.EOF) {
			s.writeErrorStatus(w, http.StatusBadRequest, decodeErr)
			return
		}
		if decodeErr == nil {
			fields, provided := applyMemoryProposalFields(existing, request)
			if provided {
				replacement = &fields
			}
		}
	}
	proposal, memory, err := s.Store.AcceptMemoryProposal(r.Context(), proposalID, replacement)
	if err != nil {
		s.writeMemoryProposalStoreError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"proposal": proposal, "memory": memory})
}

func (s *Server) rejectMemoryProposal(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeMemoryProposalReviewer(w, r) {
		return
	}
	proposalID, action, err := memoryProposalPath(r)
	if err != nil || action != "reject" {
		if err == nil {
			err = errors.New("memory proposal reject path is invalid")
		}
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	proposal, err := s.Store.RejectMemoryProposal(r.Context(), proposalID)
	if err != nil {
		s.writeMemoryProposalStoreError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, proposal)
}

func decodeMemoryProposalFields(w http.ResponseWriter, r *http.Request, existing domain.MemoryProposal) (domain.MemoryProposalUpdate, bool, error) {
	var request memoryProposalFieldsRequest
	if err := decodeStrictJSON(w, r, &request); err != nil {
		return domain.MemoryProposalUpdate{}, false, err
	}
	fields, provided := applyMemoryProposalFields(existing, request)
	return fields, provided, nil
}

func applyMemoryProposalFields(existing domain.MemoryProposal, request memoryProposalFieldsRequest) (domain.MemoryProposalUpdate, bool) {
	result := domain.MemoryProposalUpdate{Category: existing.Category, Content: existing.Content, Priority: existing.Priority, ExpiresAt: existing.ExpiresAt}
	provided := false
	if request.Category != nil {
		result.Category, provided = *request.Category, true
	}
	if request.Content != nil {
		result.Content, provided = *request.Content, true
	}
	if request.Priority != nil {
		result.Priority, provided = *request.Priority, true
	}
	if request.ExpiresAt.Set {
		result.ExpiresAt, provided = request.ExpiresAt.Value, true
	}
	return result, provided
}

func memoryProposalPath(r *http.Request) (string, string, error) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/memory-proposals/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) < 1 || len(parts) > 2 || parts[0] == "" {
		return "", "", errors.New("memory proposal id is required")
	}
	if err := domain.ValidateMemoryIdentifier("memory proposal id", parts[0]); err != nil {
		return "", "", err
	}
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	return parts[0], action, nil
}

func (s *Server) writeMemoryProposalStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrMemoryProposalNotFound):
		s.writeErrorStatus(w, http.StatusNotFound, err)
	case errors.Is(err, store.ErrMemoryProposalDuplicate), errors.Is(err, store.ErrMemoryProposalLimit), errors.Is(err, store.ErrMemoryProposalResolved):
		s.writeErrorStatus(w, http.StatusConflict, err)
	case errors.Is(err, store.ErrMemoryProposalAcceptedMemoryDeleted):
		s.writeErrorStatus(w, http.StatusGone, err)
	default:
		s.writeErrorStatus(w, http.StatusBadRequest, err)
	}
}

func (s *Server) authorizeMemoryProposalReviewer(w http.ResponseWriter, r *http.Request) bool {
	if strings.TrimSpace(s.RemoteToken) == "" {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("controller authentication is required for memory proposal review"))
		return false
	}
	if !authorized(r, s.RemoteToken) {
		s.writeErrorStatus(w, http.StatusUnauthorized, errors.New("controller authorization required for memory proposal review"))
		return false
	}
	return true
}
