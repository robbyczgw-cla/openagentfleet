package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

type projectArchiveRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
}

type projectAssociationRequest struct {
	ProjectID string `json:"project_id"`
}

// handleProjectRoutes owns both /api/projects and
// /api/project-associations. Server.Handler registers both prefixes.
func (s *Server) handleProjectRoutes(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/project-associations") {
		s.handleProjectAssociationRoutes(w, r)
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/projects"), "/")
	if path == "" {
		switch r.Method {
		case http.MethodGet:
			includeArchived, err := parseProjectArchivedFilter(r.URL.Query().Get("include_archived"))
			if err != nil {
				s.writeErrorStatus(w, http.StatusBadRequest, err)
				return
			}
			items, err := s.Store.ListProjects(r.Context(), includeArchived)
			if err != nil {
				s.writeProjectError(w, err)
				return
			}
			s.writeJSON(w, http.StatusOK, map[string]any{"projects": items})
		case http.MethodPost:
			var request domain.ProjectDraft
			if err := decodeStrictJSON(w, r, &request); err != nil {
				s.writeErrorStatus(w, http.StatusBadRequest, err)
				return
			}
			item, err := s.Store.CreateProject(r.Context(), request)
			if err != nil {
				s.writeProjectError(w, err)
				return
			}
			s.writeJSON(w, http.StatusCreated, map[string]any{"project": item})
		default:
			s.writeErrorStatus(w, http.StatusNotFound, errors.New("project endpoint not found"))
		}
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) == 2 && parts[0] == "task-snapshots" && parts[1] != "" && r.Method == http.MethodGet {
		item, err := s.Store.GetTaskProjectSnapshot(r.Context(), parts[1])
		if err != nil {
			s.writeProjectError(w, err)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"project_snapshot": item})
		return
	}
	if len(parts) == 1 && parts[0] != "" {
		switch r.Method {
		case http.MethodGet:
			item, err := s.Store.GetProject(r.Context(), parts[0])
			if err != nil {
				s.writeProjectError(w, err)
				return
			}
			revisions, err := s.Store.ListProjectBriefRevisions(r.Context(), item.ID)
			if err != nil {
				s.writeProjectError(w, err)
				return
			}
			s.writeJSON(w, http.StatusOK, map[string]any{"project": item, "brief_revisions": revisions})
		case http.MethodPatch:
			var request domain.ProjectUpdate
			if err := decodeStrictJSON(w, r, &request); err != nil {
				s.writeErrorStatus(w, http.StatusBadRequest, err)
				return
			}
			item, err := s.Store.UpdateProject(r.Context(), parts[0], request)
			if err != nil {
				s.writeProjectError(w, err)
				return
			}
			s.writeJSON(w, http.StatusOK, map[string]any{"project": item})
		default:
			s.writeErrorStatus(w, http.StatusNotFound, errors.New("project endpoint not found"))
		}
		return
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "archive" && r.Method == http.MethodPost {
		var request projectArchiveRequest
		if err := decodeStrictJSON(w, r, &request); err != nil {
			s.writeErrorStatus(w, http.StatusBadRequest, err)
			return
		}
		item, err := s.Store.ArchiveProject(r.Context(), parts[0], request.ExpectedVersion)
		if err != nil {
			s.writeProjectError(w, err)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"project": item})
		return
	}
	s.writeErrorStatus(w, http.StatusNotFound, errors.New("project endpoint not found"))
}

func (s *Server) handleProjectAssociationRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/project-associations"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[1] == "" {
		s.writeErrorStatus(w, http.StatusNotFound, errors.New("project association endpoint not found"))
		return
	}
	subjectType := domain.ProjectSubjectType(parts[0])
	if !domain.ValidProjectSubjectType(subjectType) {
		s.writeErrorStatus(w, http.StatusNotFound, errors.New("project association endpoint not found"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		item, err := s.Store.GetProjectAssociation(r.Context(), subjectType, parts[1])
		if err != nil {
			s.writeProjectError(w, err)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"association": item})
	case http.MethodPut:
		var request projectAssociationRequest
		if err := decodeStrictJSON(w, r, &request); err != nil {
			s.writeErrorStatus(w, http.StatusBadRequest, err)
			return
		}
		item, err := s.Store.SetProjectAssociation(r.Context(), strings.TrimSpace(request.ProjectID), subjectType, parts[1])
		if err != nil {
			s.writeProjectError(w, err)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"association": item})
	case http.MethodDelete:
		if err := s.Store.DeleteProjectAssociation(r.Context(), subjectType, parts[1]); err != nil {
			s.writeProjectError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		s.writeErrorStatus(w, http.StatusNotFound, errors.New("project association endpoint not found"))
	}
}

func (s *Server) writeProjectError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrProjectNotFound),
		errors.Is(err, store.ErrProjectSubjectNotFound),
		errors.Is(err, store.ErrProjectAssociationNotFound),
		errors.Is(err, store.ErrProjectSnapshotNotFound):
		s.writeErrorStatus(w, http.StatusNotFound, err)
	case errors.Is(err, store.ErrProjectArchived),
		errors.Is(err, store.ErrProjectVersionConflict),
		errors.Is(err, store.ErrProjectAgentNotMember):
		s.writeErrorStatus(w, http.StatusConflict, err)
	case errors.Is(err, store.ErrProjectAgentNotFound):
		s.writeErrorStatus(w, http.StatusBadRequest, err)
	default:
		s.writeErrorStatus(w, http.StatusBadRequest, err)
	}
}

func parseProjectArchivedFilter(raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, errors.New("include_archived must be true or false")
	}
	return value, nil
}
