package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
)

const memorySelectColumns = "id, bot_id, category, status, source, content, priority, expires_at, created_at, updated_at"

var (
	ErrBotMemoryNotFound = errors.New("bot memory not found")
	ErrMemoryBotNotFound = errors.New("bot not found")
)

func (s *Store) CreateBotMemory(ctx context.Context, botID string, draft domain.BotMemoryDraft) (domain.BotMemory, error) {
	if err := domain.ValidateMemoryIdentifier("bot id", botID); err != nil {
		return domain.BotMemory{}, err
	}
	normalized, err := domain.NormalizeBotMemoryDraft(draft)
	if err != nil {
		return domain.BotMemory{}, err
	}
	if err := s.requireMemoryBot(ctx, botID); err != nil {
		return domain.BotMemory{}, err
	}
	timestamp := now()
	item := domain.BotMemory{
		ID:        id.New("memory"),
		BotID:     botID,
		Category:  normalized.Category,
		Status:    normalized.Status,
		Source:    normalized.Source,
		Content:   normalized.Content,
		Priority:  normalized.Priority,
		ExpiresAt: normalized.ExpiresAt,
		CreatedAt: timestamp,
		UpdatedAt: timestamp,
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO bot_memories
		(id, bot_id, category, status, source, content, priority, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.BotID, item.Category, item.Status, item.Source, item.Content,
		item.Priority, item.ExpiresAt, item.CreatedAt, item.UpdatedAt)
	if err != nil {
		return domain.BotMemory{}, fmt.Errorf("create bot memory: %w", err)
	}
	return item, nil
}

func (s *Store) GetBotMemory(ctx context.Context, botID, memoryID string) (domain.BotMemory, error) {
	if err := validateMemoryScope(botID, memoryID); err != nil {
		return domain.BotMemory{}, err
	}
	item, err := scanBotMemory(s.db.QueryRowContext(ctx, "SELECT "+memorySelectColumns+" FROM bot_memories WHERE bot_id = ? AND id = ?", botID, memoryID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.BotMemory{}, ErrBotMemoryNotFound
	}
	if err != nil {
		return domain.BotMemory{}, fmt.Errorf("get bot memory: %w", err)
	}
	return item, nil
}

func (s *Store) UpdateBotMemory(ctx context.Context, botID, memoryID string, update domain.BotMemoryUpdate) (domain.BotMemory, error) {
	if err := validateMemoryScope(botID, memoryID); err != nil {
		return domain.BotMemory{}, err
	}
	normalized, err := domain.NormalizeBotMemoryUpdate(update)
	if err != nil {
		return domain.BotMemory{}, err
	}
	existing, err := s.GetBotMemory(ctx, botID, memoryID)
	if err != nil {
		return domain.BotMemory{}, err
	}
	updatedAt := nextMemoryTimestamp(existing.UpdatedAt)
	result, err := s.db.ExecContext(ctx, `UPDATE bot_memories
		SET category = ?, status = ?, content = ?, priority = ?, expires_at = ?, updated_at = ?
		WHERE bot_id = ? AND id = ?`, normalized.Category, normalized.Status, normalized.Content,
		normalized.Priority, normalized.ExpiresAt, updatedAt, botID, memoryID)
	if err != nil {
		return domain.BotMemory{}, fmt.Errorf("update bot memory: %w", err)
	}
	if count, countErr := result.RowsAffected(); countErr != nil {
		return domain.BotMemory{}, fmt.Errorf("update bot memory: %w", countErr)
	} else if count != 1 {
		return domain.BotMemory{}, errors.New("bot memory not found")
	}
	return s.GetBotMemory(ctx, botID, memoryID)
}

// ArchiveBotMemory removes a record from the agent context while retaining it
// for user review and possible restoration.
func (s *Store) ArchiveBotMemory(ctx context.Context, botID, memoryID string) (domain.BotMemory, error) {
	existing, err := s.GetBotMemory(ctx, botID, memoryID)
	if err != nil {
		return domain.BotMemory{}, err
	}
	if existing.Status == domain.MemoryStatusArchived {
		return existing, nil
	}
	return s.UpdateBotMemory(ctx, botID, memoryID, domain.BotMemoryUpdate{
		Category:  existing.Category,
		Status:    domain.MemoryStatusArchived,
		Content:   existing.Content,
		Priority:  existing.Priority,
		ExpiresAt: existing.ExpiresAt,
	})
}

// ListBotMemories returns active memory by default. includeInactive exposes
// archived and expired records for review without changing retrieval behavior.
func (s *Store) ListBotMemories(ctx context.Context, botID string, includeInactive bool) ([]domain.BotMemory, error) {
	if err := domain.ValidateMemoryIdentifier("bot id", botID); err != nil {
		return nil, err
	}
	if err := s.requireMemoryBot(ctx, botID); err != nil {
		return nil, err
	}
	query := "SELECT " + memorySelectColumns + " FROM bot_memories WHERE bot_id = ?"
	args := []any{botID}
	if !includeInactive {
		query += " AND status = ? AND (expires_at = '' OR expires_at > ?)"
		args = append(args, domain.MemoryStatusApproved, now())
	}
	query += " ORDER BY priority DESC, updated_at DESC, created_at DESC, id ASC"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list bot memories: %w", err)
	}
	defer rows.Close()
	items := make([]domain.BotMemory, 0)
	for rows.Next() {
		item, scanErr := scanBotMemory(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list bot memories: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bot memories: %w", err)
	}
	return items, nil
}

// RetrieveBotMemories returns approved, non-expired memories within both
// caller-provided budgets. Byte accounting covers UTF-8 content bytes only.
func (s *Store) RetrieveBotMemories(ctx context.Context, botID string, maxCount, maxBytes int) ([]domain.BotMemory, error) {
	if err := domain.ValidateMemoryRetrievalLimits(maxCount, maxBytes); err != nil {
		return nil, err
	}
	items, err := s.ListBotMemories(ctx, botID, false)
	if err != nil {
		return nil, err
	}
	result := make([]domain.BotMemory, 0, min(maxCount, len(items)))
	usedBytes := 0
	for _, item := range items {
		contentBytes := len(item.Content)
		if contentBytes > maxBytes-usedBytes {
			continue
		}
		result = append(result, item)
		usedBytes += contentBytes
		if len(result) == maxCount || usedBytes == maxBytes {
			break
		}
	}
	return result, nil
}

// DeleteBotMemory permanently deletes a user-reviewed memory. Callers should
// prefer ArchiveBotMemory for ordinary removal from the agent context and use
// this only after an explicit, user-visible confirmation.
func (s *Store) DeleteBotMemory(ctx context.Context, botID, memoryID string) (domain.BotMemory, error) {
	item, err := s.GetBotMemory(ctx, botID, memoryID)
	if err != nil {
		return domain.BotMemory{}, err
	}
	result, err := s.db.ExecContext(ctx, "DELETE FROM bot_memories WHERE bot_id = ? AND id = ?", botID, memoryID)
	if err != nil {
		return domain.BotMemory{}, fmt.Errorf("delete bot memory: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return domain.BotMemory{}, fmt.Errorf("delete bot memory: %w", err)
	}
	if count != 1 {
		return domain.BotMemory{}, ErrBotMemoryNotFound
	}
	return item, nil
}

func (s *Store) requireMemoryBot(ctx context.Context, botID string) error {
	var exists int
	if err := s.db.QueryRowContext(ctx, "SELECT 1 FROM bots WHERE id = ?", botID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return ErrMemoryBotNotFound
	} else if err != nil {
		return fmt.Errorf("verify memory bot: %w", err)
	}
	return nil
}

func validateMemoryScope(botID, memoryID string) error {
	if err := domain.ValidateMemoryIdentifier("bot id", botID); err != nil {
		return err
	}
	return domain.ValidateMemoryIdentifier("memory id", memoryID)
}

type memoryScanner interface {
	Scan(dest ...any) error
}

func scanBotMemory(scanner memoryScanner) (domain.BotMemory, error) {
	var item domain.BotMemory
	err := scanner.Scan(&item.ID, &item.BotID, &item.Category, &item.Status, &item.Source,
		&item.Content, &item.Priority, &item.ExpiresAt, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func nextMemoryTimestamp(previous string) string {
	candidate := time.Now().UTC()
	if parsed, err := time.Parse(time.RFC3339Nano, previous); err == nil && !candidate.After(parsed) {
		candidate = parsed.Add(time.Nanosecond)
	}
	return candidate.Format(time.RFC3339Nano)
}
