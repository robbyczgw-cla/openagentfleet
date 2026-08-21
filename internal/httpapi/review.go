package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/harness"
)

const reviewSummaryLimit = 160

type reviewItem struct {
	Kind           string                  `json:"kind"`
	BotID          string                  `json:"bot_id"`
	BotName        string                  `json:"bot_name"`
	ConversationID string                  `json:"conversation_id"`
	RunID          string                  `json:"run_id"`
	CreatedAt      string                  `json:"created_at"`
	ID             string                  `json:"id,omitempty"`
	Action         string                  `json:"action,omitempty"`
	Status         string                  `json:"status,omitempty"`
	Summary        string                  `json:"summary,omitempty"`
	Options        []domain.ApprovalOption `json:"options,omitempty"`
}

func (s *Server) listReview(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		s.writeErrorStatus(w, http.StatusServiceUnavailable, errors.New("review store unavailable"))
		return
	}
	items, err := s.reviewQueue(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) reviewQueue(ctx context.Context) ([]reviewItem, error) {
	bots, err := s.Store.ListBots(ctx)
	if err != nil {
		return nil, err
	}
	botName := make(map[string]string, len(bots))
	for _, bot := range bots {
		botName[bot.ID] = bot.Name
	}
	items := make([]reviewItem, 0)

	approvals, err := s.Store.ListApprovals(ctx, "pending")
	if err != nil {
		return nil, err
	}
	for _, approval := range approvals {
		if approval.Status != "pending" {
			continue
		}
		run, runErr := s.Store.GetRun(ctx, approval.RunID)
		if runErr != nil || run.Status != "waiting_for_approval" {
			continue
		}
		items = append(items, reviewItem{
			Kind:           "approval",
			BotID:          run.BotID,
			BotName:        reviewBotName(botName, run.BotID),
			ConversationID: run.ConversationID,
			RunID:          run.ID,
			CreatedAt:      approval.CreatedAt,
			ID:             approval.ID,
			Action:         strings.TrimSpace(approval.Action),
			Status:         "pending",
			Options:        mobileApprovalOptions(approval.Payload),
		})
	}

	runs, err := s.Store.ListLatestTerminalRunsByBot(ctx)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		if !reviewTerminalRunStatus(run.Status) {
			continue
		}
		events, eventErr := s.Store.ListRunEvents(ctx, run.ID)
		if eventErr != nil {
			events = nil
		}
		assistant, _ := s.Store.LastAssistantContent(ctx, run.ConversationID)
		items = append(items, reviewItem{
			Kind:           "run",
			BotID:          run.BotID,
			BotName:        reviewBotName(botName, run.BotID),
			ConversationID: run.ConversationID,
			RunID:          run.ID,
			CreatedAt:      reviewRunCreatedAt(run),
			Status:         run.Status,
			Summary:        summarizeRunWork(events, run.Status, assistant),
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		if left.Kind != right.Kind {
			return left.Kind == "approval"
		}
		return reviewTime(left.CreatedAt).After(reviewTime(right.CreatedAt))
	})
	return items, nil
}

func reviewTerminalRunStatus(status string) bool {
	switch status {
	case "completed", "failed", "blocked", "stopped":
		return true
	default:
		return false
	}
}

func reviewBotName(names map[string]string, botID string) string {
	if name := strings.TrimSpace(names[botID]); name != "" {
		return name
	}
	return "Agent"
}

func reviewRunCreatedAt(run domain.Run) string {
	if strings.TrimSpace(run.UpdatedAt) != "" {
		return run.UpdatedAt
	}
	return run.CreatedAt
}

func reviewTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, value)
	}
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func summarizeRunWork(events []domain.RunEvent, status, assistant string) string {
	var fileTitle, lastWorkType string
	for _, event := range events {
		kind, title := reviewEventWork(event)
		if kind == "" {
			continue
		}
		lastWorkType = kind
		if fileTitle == "" && isFileOrDiffKind(kind) && title != "" {
			fileTitle = title
		}
	}
	if fileTitle != "" {
		return boundReviewText(fileTitle)
	}
	if lastWorkType != "" {
		return boundReviewText(lastWorkType)
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = "unknown"
	}
	snippet := boundReviewText(assistant)
	if snippet == "" {
		return status
	}
	return boundReviewText(status + " · " + snippet)
}

func reviewEventWork(event domain.RunEvent) (kind, title string) {
	eventType := strings.TrimSpace(event.Type)
	if eventType == "" || isReviewLifecycleType(eventType) {
		return "", ""
	}
	payload := reviewJSONMap(event.Data)
	kind = eventType
	if inner := reviewString(payload["type"]); inner != "" {
		kind = inner
	}
	if !isReviewWorkKind(kind, eventType) {
		return "", ""
	}
	return kind, reviewEventTitle(payload)
}

func isReviewLifecycleType(eventType string) bool {
	lower := strings.ToLower(eventType)
	return strings.HasPrefix(lower, "run.") ||
		strings.HasPrefix(lower, "session.") ||
		strings.HasPrefix(lower, "approval.") ||
		strings.HasPrefix(lower, "lead.") ||
		strings.HasPrefix(lower, "worker.")
}

func isReviewWorkKind(kind, eventType string) bool {
	lower := strings.ToLower(kind + " " + eventType)
	if strings.Contains(lower, "thought") || strings.Contains(lower, "reason") {
		return false
	}
	if strings.Contains(lower, "file") || strings.Contains(lower, "diff") || strings.Contains(lower, "tool") {
		return true
	}
	if eventType != "provider.output" {
		return false
	}
	inner := strings.ToLower(strings.TrimSpace(kind))
	return inner != "" && inner != "text" && inner != "thought"
}

func isFileOrDiffKind(kind string) bool {
	lower := strings.ToLower(kind)
	return strings.Contains(lower, "file") || strings.Contains(lower, "diff")
}

func reviewEventTitle(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	candidates := []any{payload}
	if text := reviewString(payload["text"]); text != "" {
		if nested := reviewJSONMap(text); nested != nil {
			candidates = append(candidates, nested)
		}
	}
	if data, ok := payload["data"].(map[string]any); ok {
		candidates = append(candidates, data)
	}
	for _, candidate := range candidates {
		obj, ok := candidate.(map[string]any)
		if !ok {
			continue
		}
		for _, nestedKey := range []string{"update", "tool_call", "file", "diff"} {
			if nested, ok := obj[nestedKey].(map[string]any); ok {
				if title := reviewTitleFields(nested); title != "" {
					return title
				}
			}
		}
		if title := reviewTitleFields(obj); title != "" {
			return title
		}
	}
	return ""
}

func reviewTitleFields(payload map[string]any) string {
	for _, key := range []string{"title", "path", "filename", "name"} {
		value := reviewString(payload[key])
		if value == "" || isReviewSecretKey(key) {
			continue
		}
		if key == "path" || key == "filename" {
			value = path.Base(strings.ReplaceAll(value, "\\", "/"))
		}
		if title := boundReviewText(value); title != "" {
			return title
		}
	}
	if file, ok := payload["file"].(string); ok {
		if title := boundReviewText(path.Base(strings.ReplaceAll(file, "\\", "/"))); title != "" {
			return title
		}
	}
	return ""
}

func reviewJSONMap(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" || (raw[0] != '{' && raw[0] != '[') {
		return nil
	}
	var object map[string]any
	if json.Unmarshal([]byte(raw), &object) == nil {
		return object
	}
	return nil
}

func reviewString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func isReviewSecretKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	switch lower {
	case "command", "prompt", "workdir", "native_session_id", "token", "password",
		"secret", "payload", "error", "api_key", "authorization", "bearer":
		return true
	default:
		return strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password")
	}
}

func boundReviewText(value string) string {
	value = harness.Redact(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
	if value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) <= reviewSummaryLimit {
		return value
	}
	runes := []rune(value)
	return string(runes[:reviewSummaryLimit])
}
