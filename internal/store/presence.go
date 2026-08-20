package store

import (
	"context"
	"fmt"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

// ListLatestRunsByBot returns at most one run per bot: the newest by
// updated_at, then id. Used to derive live Agent roster presence.
func (s *Store) ListLatestRunsByBot(ctx context.Context) ([]domain.Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, conversation_id, bot_id, provider, status, prompt, error, created_at, updated_at
FROM runs r
WHERE NOT EXISTS (
  SELECT 1 FROM runs newer
  WHERE newer.bot_id = r.bot_id
    AND (newer.updated_at > r.updated_at OR (newer.updated_at = r.updated_at AND newer.id > r.id))
)`)
	if err != nil {
		return nil, fmt.Errorf("list latest runs: %w", err)
	}
	defer rows.Close()
	result := make([]domain.Run, 0)
	seen := make(map[string]struct{})
	for rows.Next() {
		var item domain.Run
		if err := rows.Scan(&item.ID, &item.ConversationID, &item.BotID, &item.Provider, &item.Status, &item.Prompt, &item.Error, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan latest run: %w", err)
		}
		if _, exists := seen[item.BotID]; exists {
			continue
		}
		seen[item.BotID] = struct{}{}
		result = append(result, item)
	}
	return result, rows.Err()
}

// ListActiveHandoffs returns non-terminal Agent-to-Agent transfers.
func (s *Store) ListActiveHandoffs(ctx context.Context) ([]domain.Handoff, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+handoffSelectColumns+`
		FROM agent_handoffs
		WHERE status IN (`+activeHandoffStatuses+`)
		ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list active handoffs: %w", err)
	}
	defer rows.Close()
	return scanHandoffRows(rows)
}
