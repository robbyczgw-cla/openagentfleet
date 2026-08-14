package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"

	"github.com/robbyczgw-cla/openagentfleet/internal/websearchplus"
)

type strictOptionalBool struct {
	set   bool
	value bool
}

func (value *strictOptionalBool) UnmarshalJSON(payload []byte) error {
	switch {
	case bytes.Equal(payload, []byte("true")):
		value.set = true
		value.value = true
		return nil
	case bytes.Equal(payload, []byte("false")):
		value.set = true
		value.value = false
		return nil
	default:
		return errors.New("must be a boolean")
	}
}

type searchConnectorPatchRequest struct {
	WebSearchPlusEnabled strictOptionalBool `json:"web_search_plus_enabled"`
	HoundEnabled         strictOptionalBool `json:"hound_enabled"`
}

func (s *Server) getSearchConnectors(w http.ResponseWriter, r *http.Request) {
	if s.SearchConnectors == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("search connector controller unavailable"))
		return
	}
	if err := requireEmptyBody(w, r); err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	s.writeJSON(w, http.StatusOK, s.SearchConnectors.Status(r.Context()))
}

func (s *Server) patchSearchConnectors(w http.ResponseWriter, r *http.Request) {
	if s.SearchConnectors == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("search connector controller unavailable"))
		return
	}
	var request *searchConnectorPatchRequest
	if err := decodeStrictJSON(w, r, &request); err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	if request == nil {
		s.writeErrorStatus(w, http.StatusBadRequest, errors.New("invalid JSON request: expected an object"))
		return
	}
	patch := websearchplus.ConnectorPatch{}
	if request.WebSearchPlusEnabled.set {
		patch.WebSearchPlusEnabled = &request.WebSearchPlusEnabled.value
	}
	if request.HoundEnabled.set {
		patch.HoundEnabled = &request.HoundEnabled.value
	}
	status, err := s.SearchConnectors.Patch(r.Context(), patch)
	if err != nil {
		s.writeErrorStatus(w, http.StatusInternalServerError, fmt.Errorf("update search connectors: %w", err))
		return
	}
	s.writeJSON(w, http.StatusOK, status)
}
