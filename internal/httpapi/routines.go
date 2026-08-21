package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
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

type routinePauseRequest struct {
	Reason string `json:"reason"`
}

type routineHeartbeatRequest struct {
	OptedIn bool `json:"opted_in"`
}

func (s *Server) handleRoutineRoutes(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/routines" {
		switch r.Method {
		case http.MethodGet:
			s.listRoutines(w, r)
		case http.MethodPost:
			s.createRoutine(w, r)
		default:
			s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		}
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/routines/") {
		s.writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/routines/"), "/")
	if rest == "" {
		s.writeErrorStatus(w, http.StatusNotFound, store.ErrRoutineNotFound)
		return
	}
	routineID, action, _ := strings.Cut(rest, "/")
	if routineID == "" || strings.Contains(routineID, "/") {
		s.writeErrorStatus(w, http.StatusNotFound, store.ErrRoutineNotFound)
		return
	}
	switch {
	case action == "" && r.Method == http.MethodGet:
		s.getRoutine(w, r, routineID)
	case action == "enable" && r.Method == http.MethodPost:
		s.enableRoutine(w, r, routineID)
	case action == "pause" && r.Method == http.MethodPost:
		s.pauseRoutine(w, r, routineID)
	case action == "resolve" && r.Method == http.MethodPost:
		s.resolveRoutine(w, r, routineID)
	case action == "heartbeat" && r.Method == http.MethodPost:
		s.setRoutineHeartbeat(w, r, routineID)
	case action == "test" && r.Method == http.MethodPost:
		s.testRoutine(w, r, routineID)
	case action == "runs" && r.Method == http.MethodGet:
		s.listRoutineRuns(w, r, routineID)
	case action == "history" && r.Method == http.MethodGet:
		s.listRoutineHistory(w, r, routineID)
	default:
		s.writeErrorStatus(w, http.StatusNotFound, store.ErrRoutineNotFound)
	}
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

func (s *Server) getRoutine(w http.ResponseWriter, r *http.Request, routineID string) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	item, err := s.Store.GetRoutine(r.Context(), routineID)
	if err != nil {
		s.writeRoutineError(w, err)
		return
	}
	runs, err := s.Store.ListRoutineRuns(r.Context(), routineID, 20)
	if err != nil {
		s.writeRoutineError(w, err)
		return
	}
	history, err := s.Store.ListRoutineHistory(r.Context(), routineID, 20)
	if err != nil {
		s.writeRoutineError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"routine": item, "runs": runs, "history": history})
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

func (s *Server) enableRoutine(w http.ResponseWriter, r *http.Request, routineID string) {
	s.changeRoutineLifecycle(w, r, routineID, domain.RoutineStatusEnabled, "")
}

func (s *Server) pauseRoutine(w http.ResponseWriter, r *http.Request, routineID string) {
	reason := ""
	if r.Body != nil {
		var request routinePauseRequest
		if err := decodeOptionalRoutineJSON(r, &request); err != nil {
			s.writeErrorStatus(w, http.StatusBadRequest, err)
			return
		}
		reason = strings.TrimSpace(request.Reason)
	}
	s.changeRoutineLifecycle(w, r, routineID, domain.RoutineStatusPaused, reason)
}

func (s *Server) resolveRoutine(w http.ResponseWriter, r *http.Request, routineID string) {
	s.changeRoutineLifecycle(w, r, routineID, domain.RoutineStatusEnabled, "resolve_attention")
}

func (s *Server) changeRoutineLifecycle(w http.ResponseWriter, r *http.Request, routineID string, target domain.RoutineStatus, reason string) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	nextRunAt, err := parseOptionalRoutineNextRun(r)
	if err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	if target == domain.RoutineStatusEnabled {
		item, getErr := s.Store.GetRoutine(r.Context(), routineID)
		if getErr != nil {
			s.writeRoutineError(w, getErr)
			return
		}
		computed, computeErr := futureRoutineNextRun(item, nextRunAt, s.currentTime(), reason == "resolve_attention")
		if computeErr != nil {
			s.writeErrorStatus(w, http.StatusBadRequest, computeErr)
			return
		}
		nextRunAt = computed
	}
	var item domain.Routine
	switch {
	case target == domain.RoutineStatusPaused:
		item, err = s.Store.PauseRoutine(r.Context(), routineID, reason)
	case reason == "resolve_attention":
		item, err = s.Store.ResolveRoutineAttention(r.Context(), routineID, nextRunAt)
	default:
		item, err = s.Store.ResumeRoutine(r.Context(), routineID, nextRunAt)
	}
	if err != nil {
		s.writeRoutineError(w, err)
		return
	}
	s.publishRoutine(item)
	s.writeJSON(w, http.StatusOK, map[string]any{"routine": item})
}

func (s *Server) testRoutine(w http.ResponseWriter, r *http.Request, routineID string) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	item, err := s.Store.GetRoutine(r.Context(), routineID)
	if err != nil {
		s.writeRoutineError(w, err)
		return
	}
	if item.Status == domain.RoutineStatusNeedsAttention {
		s.writeRoutineError(w, store.ErrRoutineNeedsAttention)
		return
	}
	now := s.currentTime()
	approvalID, occurrenceKey, err := s.routineTestClaimApprovalID(r.Context(), item, now)
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
	claimed, err := s.Store.ClaimTestRoutineRun(r.Context(), domain.RoutineClaim{
		RoutineID:      item.ID,
		LeaseOwner:     s.routineOwner(),
		LeaseDuration:  s.leaseDuration(),
		IdempotencyKey: id.New("claim"),
		ApprovalID:     approvalID,
		OccurrenceKey:  occurrenceKey,
		Now:            now,
	})
	if err != nil {
		s.writeRoutineError(w, err)
		return
	}
	s.launchRoutineOccurrence(r.Context(), item, claimed)
	updated, err := s.Store.GetRoutine(r.Context(), item.ID)
	if err != nil {
		updated = item
	}
	if runs, listErr := s.Store.ListRoutineRuns(r.Context(), item.ID, 1); listErr == nil {
		for _, run := range runs {
			if run.ID == claimed.ID {
				claimed = run
				break
			}
		}
	}
	s.publishRoutine(updated)
	s.writeJSON(w, http.StatusAccepted, map[string]any{
		"routine": updated,
		"run":     claimed,
	})
}

func (s *Server) setRoutineHeartbeat(w http.ResponseWriter, r *http.Request, routineID string) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	var request routineHeartbeatRequest
	if err := decodeStrictJSON(w, r, &request); err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	item, err := s.Store.SetRoutineHeartbeatOptIn(r.Context(), routineID, request.OptedIn)
	if err != nil {
		s.writeRoutineError(w, err)
		return
	}
	s.publishRoutine(item)
	s.writeJSON(w, http.StatusOK, map[string]any{"routine": item})
}

func (s *Server) listRoutineRuns(w http.ResponseWriter, r *http.Request, routineID string) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	if _, err := s.Store.GetRoutine(r.Context(), routineID); err != nil {
		s.writeRoutineError(w, err)
		return
	}
	items, err := s.Store.ListRoutineRuns(r.Context(), routineID, 50)
	if err != nil {
		s.writeRoutineError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"runs": items})
}

func (s *Server) listRoutineHistory(w http.ResponseWriter, r *http.Request, routineID string) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	if _, err := s.Store.GetRoutine(r.Context(), routineID); err != nil {
		s.writeRoutineError(w, err)
		return
	}
	items, err := s.Store.ListRoutineHistory(r.Context(), routineID, 50)
	if err != nil {
		s.writeRoutineError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"history": items})
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
		parsed, err = time.Parse(time.RFC3339, value)
	}
	if err != nil {
		return time.Time{}, errors.New("invalid routine next run timestamp")
	}
	return parsed, nil
}

func decodeOptionalRoutineJSON(r *http.Request, destination any) error {
	if r.Body == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBodyBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxJSONBodyBytes {
		return errors.New("request body is invalid or too large")
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("invalid JSON request")
	}
	return nil
}

func futureRoutineNextRun(item domain.Routine, requested, now time.Time, rotate bool) (time.Time, error) {
	if !requested.IsZero() && requested.After(now) {
		return requested, nil
	}
	if !rotate && requested.IsZero() {
		if stored, ok := parseRoutineTime(item.NextRunAt); ok && stored.After(now) {
			return time.Time{}, nil
		}
	}
	return domain.DefaultNextRunAt(item, now)
}

func parseRoutineTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, true
	}
	parsed, err := time.Parse(time.RFC3339, value)
	return parsed, err == nil
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
