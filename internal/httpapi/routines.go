package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/skillworkshop"
	"github.com/robbyczgw-cla/openagentfleet/internal/store"
)

type routineCreateRequest struct {
	domain.RoutineDraft
	SkillID string `json:"skill_id"`
}

type routineEnableRequest struct {
	NextRunAt string `json:"next_run_at"`
}

func (s *Server) inspectSkill(w http.ResponseWriter, r *http.Request) {
	if s.Workshop == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("OpenAgentFleet Skill Workshop is not configured"))
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/skills/"), "/")
	if id == "" || strings.Contains(id, "/") {
		s.writeErrorStatus(w, http.StatusNotFound, errors.New("unknown Skill Workshop endpoint"))
		return
	}
	inspection, err := s.Workshop.Inspect(id)
	if err != nil {
		s.writeWorkshopError(w, err)
		return
	}
	payload := map[string]any{"inspection": inspection, "auto_enabled": false}
	if s.EnabledSkillsRoot == "" {
		payload["deployment"] = nil
		payload["deployment_error"] = "enabled skill root is not configured"
	} else {
		deployment, deploymentErr := s.Workshop.Deployment(id, s.EnabledSkillsRoot)
		switch {
		case deploymentErr == nil:
			payload["deployment"] = deployment
		case errors.Is(deploymentErr, skillworkshop.ErrNotFound):
			payload["deployment"] = nil
		default:
			s.writeWorkshopError(w, deploymentErr)
			return
		}
	}
	s.writeJSON(w, http.StatusOK, payload)
}

func (s *Server) listRoutines(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	items, err := s.Store.ListRoutines(r.Context(), domain.RoutineListFilter{
		BotID:  strings.TrimSpace(r.URL.Query().Get("bot_id")),
		Kind:   domain.RoutineKind(strings.TrimSpace(r.URL.Query().Get("kind"))),
		Status: domain.RoutineStatus(strings.TrimSpace(r.URL.Query().Get("status"))),
	})
	if err != nil {
		s.writeRoutineError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"routines": items})
}

func (s *Server) createRoutine(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	var request routineCreateRequest
	if err := decodeStrictJSON(w, r, &request); err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	skillID := strings.TrimSpace(request.SkillID)
	if skillID != "" {
		if err := s.requireEnabledSkill(skillID); err != nil {
			s.writeErrorStatus(w, http.StatusConflict, err)
			return
		}
		if !strings.Contains(request.Description, skillID) {
			if strings.TrimSpace(request.Description) == "" {
				request.Description = "skill:" + skillID
			} else {
				request.Description = "skill:" + skillID + "\n" + request.Description
			}
		}
	}
	item, err := s.Store.CreateRoutine(r.Context(), request.RoutineDraft)
	if err != nil {
		s.writeRoutineError(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"routine": item})
}

func (s *Server) enableRoutine(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/routines/"), "/enable")
	routineID := strings.Trim(path, "/")
	if routineID == "" || strings.Contains(routineID, "/") {
		s.writeErrorStatus(w, http.StatusNotFound, store.ErrRoutineNotFound)
		return
	}
	nextRunAt, err := parseOptionalRoutineNextRun(r)
	if err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	item, err := s.Store.ResumeRoutine(r.Context(), routineID, nextRunAt)
	if err != nil {
		s.writeRoutineError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"routine": item})
}

func (s *Server) requireEnabledSkill(id string) error {
	if s.Workshop == nil || strings.TrimSpace(s.EnabledSkillsRoot) == "" {
		return errors.New("skill is not enabled")
	}
	deployment, err := s.Workshop.Deployment(id, s.EnabledSkillsRoot)
	if err != nil || !deployment.Active {
		return errors.New("skill is not enabled")
	}
	return nil
}

func parseOptionalRoutineNextRun(r *http.Request) (time.Time, error) {
	if r.Body == nil {
		return time.Time{}, nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBodyBytes+1))
	if err != nil {
		return time.Time{}, err
	}
	if len(data) > maxJSONBodyBytes {
		return time.Time{}, errors.New("request body is invalid or too large")
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return time.Time{}, nil
	}
	var request routineEnableRequest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return time.Time{}, errors.New("invalid JSON request")
	}
	value := strings.TrimSpace(request.NextRunAt)
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, errors.New("invalid routine next run timestamp")
	}
	return parsed, nil
}

func (s *Server) writeRoutineError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrRoutineNotFound), errors.Is(err, store.ErrRoutineBotNotFound):
		status = http.StatusNotFound
	case errors.Is(err, store.ErrRoutineHeartbeatOptIn),
		errors.Is(err, store.ErrRoutineNextRunRequired),
		errors.Is(err, store.ErrRoutineNeedsAttention),
		errors.Is(err, store.ErrRoutineDisabled),
		errors.Is(err, store.ErrRoutinePaused),
		errors.Is(err, store.ErrRoutineRunActive):
		status = http.StatusConflict
	case strings.Contains(err.Error(), "routine "):
		status = http.StatusBadRequest
	}
	s.writeErrorStatus(w, status, err)
}
