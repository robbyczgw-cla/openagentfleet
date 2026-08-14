package httpapi

import (
	"context"
	"sort"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

// visibleConversations applies the product-level conversation toggle without
// deleting or rewriting older threads. One-bot/one-chat is the default, while
// the advanced toggle reveals every durable thread again.
func (s *Server) visibleConversations(ctx context.Context, botID string) ([]domain.Conversation, error) {
	items, err := s.Store.ListConversations(ctx, botID)
	if err != nil {
		return nil, err
	}
	preferences, err := s.Store.GetPreferences(ctx)
	if err != nil {
		return nil, err
	}
	if preferences.Features.MultipleConversations || len(items) <= 1 {
		return items, nil
	}

	// Store.ListConversations is ordered by recent activity, not creation. The
	// canonical chat is the oldest thread for an agent. When the caller asks
	// for all agents (the mobile list and the desktop bootstrap do this), keep
	// one canonical chat per bot instead of accidentally hiding every agent but
	// the first one.
	canonicalByBot := make(map[string]domain.Conversation)
	for _, item := range items {
		current, exists := canonicalByBot[item.BotID]
		if !exists || item.CreatedAt < current.CreatedAt ||
			(item.CreatedAt == current.CreatedAt && item.ID < current.ID) {
			canonicalByBot[item.BotID] = item
		}
	}
	canonical := make([]domain.Conversation, 0, len(canonicalByBot))
	for _, item := range canonicalByBot {
		canonical = append(canonical, item)
	}
	sort.SliceStable(canonical, func(i, j int) bool {
		if canonical[i].UpdatedAt == canonical[j].UpdatedAt {
			return canonical[i].ID > canonical[j].ID
		}
		return canonical[i].UpdatedAt > canonical[j].UpdatedAt
	})
	return canonical, nil
}
