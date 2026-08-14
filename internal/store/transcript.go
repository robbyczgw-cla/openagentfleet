package store

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

// ListTranscriptBlocks projects the durable approval table into safe typed
// blocks. It is intentionally a read model for the first transcript slice;
// messages and provider event payloads remain unchanged.
func (s *Store) ListTranscriptBlocks(ctx context.Context, conversationID string) ([]domain.TranscriptBlock, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT a.id, r.conversation_id, a.run_id, a.provider, a.action, a.payload,
       a.status, a.selected_option_id, a.created_at, a.updated_at, a.resolved_at
FROM approval_requests a
JOIN runs r ON r.id = a.run_id
WHERE r.conversation_id = ?
ORDER BY a.created_at, a.id`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]domain.TranscriptBlock, 0)
	for rows.Next() {
		var item domain.TranscriptBlock
		var payload string
		if err := rows.Scan(&item.ApprovalID, &item.ConversationID, &item.RunID, &item.Provider, &item.Action, &payload, &item.Status, &item.SelectedOptionID, &item.CreatedAt, &item.UpdatedAt, &item.ResolvedAt); err != nil {
			return nil, err
		}
		item.ID = "approval-block:" + item.ApprovalID
		item.Kind = "approval"
		item.Options = decodeApprovalOptions(payload)
		result = append(result, item)
	}
	return result, rows.Err()
}

func decodeApprovalOptions(payload string) []domain.ApprovalOption {
	var envelope struct {
		Options json.RawMessage `json:"options"`
	}
	if json.Unmarshal([]byte(payload), &envelope) != nil || len(envelope.Options) == 0 {
		return nil
	}
	var options []domain.ApprovalOption
	if json.Unmarshal(envelope.Options, &options) != nil {
		return nil
	}
	clean := make([]domain.ApprovalOption, 0, len(options))
	for _, option := range options {
		option.OptionID = strings.TrimSpace(option.OptionID)
		option.Name = strings.TrimSpace(option.Name)
		option.Kind = strings.TrimSpace(option.Kind)
		if option.OptionID == "" || option.Name == "" {
			continue
		}
		if len(clean) >= 16 {
			break
		}
		clean = append(clean, option)
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}
