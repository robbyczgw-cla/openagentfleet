package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

// maxRoutinePreviewOccurrences bounds one preview request. Each occurrence
// walks forward a minute at a time, so an unbounded count would let a single
// request scan years of calendar time.
const maxRoutinePreviewOccurrences = 32

const defaultRoutinePreviewOccurrences = 5

type routinePreviewRequest struct {
	CronExpression string `json:"cron_expression"`
	TimeZone       string `json:"time_zone"`
	After          string `json:"after,omitempty"`
	Count          int    `json:"count,omitempty"`
}

// previewRoutineSchedule answers "when would this actually run?" without
// creating or changing anything. The workspace calls it on every edit of the
// easy schedule picker and of the advanced cron field, so it touches no store
// and returns only computed times.
func (s *Server) previewRoutineSchedule(w http.ResponseWriter, r *http.Request) {
	var request routinePreviewRequest
	if err := decodeStrictJSON(w, r, &request); err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	if request.Count == 0 {
		request.Count = defaultRoutinePreviewOccurrences
	}
	if request.Count < 1 || request.Count > maxRoutinePreviewOccurrences {
		s.writeErrorStatus(w, http.StatusBadRequest, errors.New("routine preview count must be between 1 and 32"))
		return
	}
	// time.LoadLocation("") resolves to UTC, so an omitted zone would silently
	// preview in UTC while the picker shows nothing selected. RoutineDraft
	// rejects a blank zone; preview has to agree or it would promise times the
	// routine will never actually run at.
	if strings.TrimSpace(request.TimeZone) == "" {
		s.writeErrorStatus(w, http.StatusBadRequest, errors.New("routine time zone is required"))
		return
	}
	after := s.currentTime()
	if strings.TrimSpace(request.After) != "" {
		parsed, err := parseRoutinePreviewTime(request.After)
		if err != nil {
			s.writeErrorStatus(w, http.StatusBadRequest, err)
			return
		}
		after = parsed
	}
	occurrences := make([]string, 0, request.Count)
	for index := 0; index < request.Count; index++ {
		if err := r.Context().Err(); err != nil {
			return
		}
		next, err := domain.NextCronTime(request.CronExpression, request.TimeZone, after)
		if err != nil {
			s.writeErrorStatus(w, http.StatusBadRequest, err)
			return
		}
		occurrences = append(occurrences, next.Format(time.RFC3339))
		after = next
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"occurrences": occurrences,
		"time_zone":   strings.TrimSpace(request.TimeZone),
	})
}

type routineEditRequest struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	CronExpression string `json:"cron_expression"`
	TimeZone       string `json:"time_zone"`
}

// editRoutine updates a paused or disabled routine in place. The store rejects
// the edit when the routine is enabled or still owns an in-flight occurrence,
// which keeps the workspace from rewriting work the scheduler already claimed.
func (s *Server) editRoutine(w http.ResponseWriter, r *http.Request, routineID string) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("agent store unavailable"))
		return
	}
	var request routineEditRequest
	if err := decodeStrictJSON(w, r, &request); err != nil {
		s.writeErrorStatus(w, http.StatusBadRequest, err)
		return
	}
	item, err := s.Store.EditRoutineSchedule(r.Context(), routineID, request.Name, request.Description, request.CronExpression, request.TimeZone)
	if err != nil {
		s.writeRoutineError(w, err)
		return
	}
	s.publishRoutine(item)
	s.writeJSON(w, http.StatusOK, map[string]any{"routine": item})
}

func parseRoutinePreviewTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, errors.New("invalid routine preview start timestamp")
	}
	return parsed, nil
}
