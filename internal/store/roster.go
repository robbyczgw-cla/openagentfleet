package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

type AgentRosterUpdate struct {
	Pinned *bool
	Hidden *bool
	Unread *bool
}

type agentRosterRow struct {
	pinned     bool
	hidden     bool
	unread     bool
	lastReadAt string
}

// MigrateRoster is additive and idempotent. Store.Open invokes it so existing
// control-plane databases gain roster flags without rewriting bots.
func (s *Store) MigrateRoster(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS agent_roster (
  bot_id TEXT PRIMARY KEY REFERENCES bots(id) ON DELETE CASCADE,
  pinned INTEGER NOT NULL DEFAULT 0 CHECK(pinned IN (0, 1)),
  hidden INTEGER NOT NULL DEFAULT 0 CHECK(hidden IN (0, 1)),
  unread INTEGER NOT NULL DEFAULT 0 CHECK(unread IN (0, 1)),
  last_read_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate agent roster: %w", err)
	}
	return nil
}

func (s *Store) PatchAgentRoster(ctx context.Context, botID string, update AgentRosterUpdate) (domain.Agent, error) {
	if botID == "" {
		return domain.Agent{}, ErrAgentNotFound
	}
	if update.Pinned == nil && update.Hidden == nil && update.Unread == nil {
		return domain.Agent{}, errors.New("at least one roster field is required")
	}
	s.agentMu.Lock()
	defer s.agentMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Agent{}, fmt.Errorf("begin agent roster update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM bots WHERE id = ?", botID).Scan(&exists); err != nil {
		return domain.Agent{}, fmt.Errorf("load agent roster bot: %w", err)
	}
	if exists == 0 {
		return domain.Agent{}, ErrAgentNotFound
	}

	current := agentRosterRow{}
	var pinned, hidden, unread int
	err = tx.QueryRowContext(ctx, `SELECT pinned, hidden, unread, last_read_at FROM agent_roster WHERE bot_id = ?`, botID).
		Scan(&pinned, &hidden, &unread, &current.lastReadAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return domain.Agent{}, fmt.Errorf("load agent roster: %w", err)
	}
	if err == nil {
		current.pinned = pinned != 0
		current.hidden = hidden != 0
		current.unread = unread != 0
	}

	if update.Pinned != nil {
		current.pinned = *update.Pinned
	}
	if update.Hidden != nil {
		current.hidden = *update.Hidden
	}
	if update.Unread != nil {
		current.unread = *update.Unread
		if !*update.Unread {
			current.lastReadAt = now()
		}
	}
	updatedAt := now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_roster (bot_id, pinned, hidden, unread, last_read_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(bot_id) DO UPDATE SET
			pinned = excluded.pinned,
			hidden = excluded.hidden,
			unread = excluded.unread,
			last_read_at = excluded.last_read_at,
			updated_at = excluded.updated_at`,
		botID, boolToInt(current.pinned), boolToInt(current.hidden), boolToInt(current.unread), current.lastReadAt, updatedAt); err != nil {
		return domain.Agent{}, fmt.Errorf("update agent roster: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Agent{}, fmt.Errorf("commit agent roster update: %w", err)
	}
	return s.getAgent(ctx, botID)
}

func (s *Store) MarkAgentUnread(ctx context.Context, botID string) error {
	unread := true
	_, err := s.PatchAgentRoster(ctx, botID, AgentRosterUpdate{Unread: &unread})
	return err
}

func (s *Store) getAgent(ctx context.Context, botID string) (domain.Agent, error) {
	agents, err := s.ListAgents(ctx)
	if err != nil {
		return domain.Agent{}, err
	}
	for _, agent := range agents {
		if agent.Bot.ID == botID {
			return agent, nil
		}
	}
	return domain.Agent{}, ErrAgentNotFound
}

func (s *Store) applyAgentRoster(ctx context.Context, items []domain.Agent) error {
	if len(items) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT bot_id, pinned, hidden, unread FROM agent_roster`)
	if err != nil {
		return fmt.Errorf("list agent roster: %w", err)
	}
	defer rows.Close()
	byBotID := make(map[string]agentRosterRow)
	for rows.Next() {
		var botID string
		var pinned, hidden, unread int
		if err := rows.Scan(&botID, &pinned, &hidden, &unread); err != nil {
			return fmt.Errorf("scan agent roster: %w", err)
		}
		byBotID[botID] = agentRosterRow{pinned: pinned != 0, hidden: hidden != 0, unread: unread != 0}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate agent roster: %w", err)
	}
	for index := range items {
		row, ok := byBotID[items[index].Bot.ID]
		if !ok {
			continue
		}
		items[index].Pinned = row.pinned
		items[index].Hidden = row.hidden
		items[index].Unread = row.unread
	}
	return nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
