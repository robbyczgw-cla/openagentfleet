package httpapi

import (
	"net/http"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

const (
	fleetHostAPIVersion     = 1
	fleetHostRoleAuthority  = "authority"
)

type fleetHostStatus struct {
	HostID      string `json:"host_id"`
	APIVersion  int    `json:"api_version"`
	AuthVersion int    `json:"auth_version"`
	Role        string `json:"role"`
}

// FleetHostHandler is a small discovery surface for an always-on fleet host.
// Tests can mount it on a local mux; Handler also serves GET /api/host/status.
func (s *Server) FleetHostHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/api/host/status" && r.Method == http.MethodGet {
			s.hostStatus(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

func (s *Server) hostStatus(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "host unavailable"})
		return
	}
	hostID, err := s.Store.GetOrCreateRemoteHostID(r.Context())
	if err != nil {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "host unavailable"})
		return
	}
	// auth_version stays at RemoteAuthVersionBearer until a coordinated bump
	// for refresh rotation. DPoP is not implemented on this status surface.
	s.writeJSON(w, http.StatusOK, fleetHostStatus{
		HostID:      hostID,
		APIVersion:  fleetHostAPIVersion,
		AuthVersion: domain.RemoteAuthVersionBearer,
		Role:        fleetHostRoleAuthority,
	})
}
