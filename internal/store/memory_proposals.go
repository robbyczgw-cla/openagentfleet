package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
)

var (
	ErrMemoryProposalNotFound              = errors.New("memory proposal not found")
	ErrMemoryProposalResolved              = errors.New("memory proposal is no longer pending")
	ErrMemoryProposalDuplicate             = errors.New("duplicate memory proposal")
	ErrMemoryProposalLimit                 = errors.New("memory proposal limit reached")
	ErrMemoryProposalAcceptedMemoryDeleted = errors.New("accepted proposal memory was deleted")
)

const memoryProposalColumns = `id, bot_id, source_run_id, source_message_id, category, status,
	content, priority, expires_at, COALESCE(memory_id, ''), created_at, updated_at`

func (s *Store) EnsureMemoryProposalsSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS memory_proposals (
		id TEXT PRIMARY KEY,
		bot_id TEXT NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
		source_run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		source_message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
		category TEXT NOT NULL CHECK(category IN ('fact', 'preference', 'instruction', 'project')),
		status TEXT NOT NULL CHECK(status IN ('pending', 'accepted', 'rejected')),
		content TEXT NOT NULL CHECK(length(CAST(content AS BLOB)) BETWEEN 1 AND 4096),
		normalized_content TEXT NOT NULL,
		priority INTEGER NOT NULL CHECK(priority BETWEEN 1 AND 5),
		expires_at TEXT NOT NULL DEFAULT '',
		memory_id TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE(bot_id, normalized_content)
	);
	CREATE INDEX IF NOT EXISTS memory_proposals_review_idx
		ON memory_proposals(status, created_at DESC, id);
	CREATE INDEX IF NOT EXISTS memory_proposals_bot_idx
		ON memory_proposals(bot_id, status, created_at DESC, id);`)
	if err != nil {
		return fmt.Errorf("migrate memory proposals: %w", err)
	}
	hasMemoryFK, err := s.memoryProposalsHasMemoryFK(ctx)
	if err != nil {
		return err
	}
	if hasMemoryFK {
		if err := s.rebuildMemoryProposalsWithoutMemoryFK(ctx); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS memory_proposals_review_idx
		ON memory_proposals(status, created_at DESC, id);
	CREATE INDEX IF NOT EXISTS memory_proposals_bot_idx
		ON memory_proposals(bot_id, status, created_at DESC, id);`)
	return err
}

func (s *Store) CreateMemoryProposal(ctx context.Context, draft domain.MemoryProposalDraft) (domain.MemoryProposal, error) {
	normalized, err := domain.NormalizeMemoryProposalDraft(draft)
	if err != nil {
		return domain.MemoryProposal{}, err
	}
	contentKey, err := domain.NormalizeMemoryProposalContentKey(normalized.Content)
	if err != nil {
		return domain.MemoryProposal{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.MemoryProposal{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var runBotID, conversationID string
	if err := tx.QueryRowContext(ctx, "SELECT bot_id, conversation_id FROM runs WHERE id = ?", normalized.SourceRunID).Scan(&runBotID, &conversationID); errors.Is(err, sql.ErrNoRows) {
		return domain.MemoryProposal{}, ErrMemoryProposalNotFound
	} else if err != nil {
		return domain.MemoryProposal{}, fmt.Errorf("verify proposal run: %w", err)
	}
	if runBotID != normalized.BotID {
		return domain.MemoryProposal{}, errors.New("memory proposal run does not belong to bot")
	}
	var sourceRole string
	if err := tx.QueryRowContext(ctx, "SELECT role FROM messages WHERE id = ? AND conversation_id = ?", normalized.SourceMessageID, conversationID).Scan(&sourceRole); errors.Is(err, sql.ErrNoRows) {
		return domain.MemoryProposal{}, errors.New("memory proposal source message does not belong to run conversation")
	} else if err != nil {
		return domain.MemoryProposal{}, fmt.Errorf("verify proposal source message: %w", err)
	}
	if sourceRole != "user" {
		return domain.MemoryProposal{}, errors.New("memory proposal source message must be a user message")
	}
	var botPending, runTotal int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_proposals WHERE bot_id = ? AND status = ?", normalized.BotID, domain.MemoryProposalStatusPending).Scan(&botPending); err != nil {
		return domain.MemoryProposal{}, fmt.Errorf("count pending memory proposals: %w", err)
	}
	if botPending >= domain.MemoryProposalPendingPerBot {
		return domain.MemoryProposal{}, ErrMemoryProposalLimit
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_proposals WHERE source_run_id = ?", normalized.SourceRunID).Scan(&runTotal); err != nil {
		return domain.MemoryProposal{}, fmt.Errorf("count run memory proposals: %w", err)
	}
	if runTotal >= domain.MemoryProposalPerRun {
		return domain.MemoryProposal{}, ErrMemoryProposalLimit
	}
	timestamp := now()
	item := domain.MemoryProposal{
		ID: id.New("memprop"), BotID: normalized.BotID, SourceRunID: normalized.SourceRunID,
		SourceMessageID: normalized.SourceMessageID, Category: normalized.Category,
		Status: domain.MemoryProposalStatusPending, Content: normalized.Content, Priority: normalized.Priority,
		ExpiresAt: normalized.ExpiresAt, CreatedAt: timestamp, UpdatedAt: timestamp,
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO memory_proposals
		(id, bot_id, source_run_id, source_message_id, category, status, content, normalized_content,
		 priority, expires_at, memory_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)`, item.ID, item.BotID, item.SourceRunID,
		item.SourceMessageID, item.Category, item.Status, item.Content, contentKey, item.Priority,
		item.ExpiresAt, item.CreatedAt, item.UpdatedAt)
	if err != nil {
		if isMemoryProposalUniqueError(err) {
			return domain.MemoryProposal{}, ErrMemoryProposalDuplicate
		}
		return domain.MemoryProposal{}, fmt.Errorf("create memory proposal: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.MemoryProposal{}, fmt.Errorf("create memory proposal: %w", err)
	}
	return item, nil
}

func (s *Store) GetMemoryProposal(ctx context.Context, proposalID string) (domain.MemoryProposal, error) {
	if err := domain.ValidateMemoryIdentifier("memory proposal id", proposalID); err != nil {
		return domain.MemoryProposal{}, err
	}
	item, err := scanMemoryProposal(s.db.QueryRowContext(ctx, "SELECT "+memoryProposalColumns+" FROM memory_proposals WHERE id = ?", proposalID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MemoryProposal{}, ErrMemoryProposalNotFound
	}
	if err != nil {
		return domain.MemoryProposal{}, fmt.Errorf("get memory proposal: %w", err)
	}
	return item, nil
}

func (s *Store) ListMemoryProposals(ctx context.Context, botID string, status domain.MemoryProposalStatus) ([]domain.MemoryProposal, error) {
	query := "SELECT " + memoryProposalColumns + " FROM memory_proposals WHERE 1=1"
	args := make([]any, 0, 2)
	if botID != "" {
		if err := domain.ValidateMemoryIdentifier("bot id", botID); err != nil {
			return nil, err
		}
		query += " AND bot_id = ?"
		args = append(args, botID)
	}
	if status != "" {
		if err := domain.ValidateMemoryProposalStatus(status); err != nil {
			return nil, err
		}
		query += " AND status = ?"
		args = append(args, status)
	}
	query += " ORDER BY created_at DESC, id ASC"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list memory proposals: %w", err)
	}
	defer rows.Close()
	items := make([]domain.MemoryProposal, 0)
	for rows.Next() {
		item, scanErr := scanMemoryProposal(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list memory proposals: %w", scanErr)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpdateMemoryProposal(ctx context.Context, proposalID string, update domain.MemoryProposalUpdate) (domain.MemoryProposal, error) {
	if err := domain.ValidateMemoryIdentifier("memory proposal id", proposalID); err != nil {
		return domain.MemoryProposal{}, err
	}
	normalized, err := domain.NormalizeMemoryProposalUpdate(update)
	if err != nil {
		return domain.MemoryProposal{}, err
	}
	key, err := domain.NormalizeMemoryProposalContentKey(normalized.Content)
	if err != nil {
		return domain.MemoryProposal{}, err
	}
	timestamp := now()
	result, err := s.db.ExecContext(ctx, `UPDATE memory_proposals SET category = ?, content = ?, normalized_content = ?,
		priority = ?, expires_at = ?, updated_at = ? WHERE id = ? AND status = ?`, normalized.Category,
		normalized.Content, key, normalized.Priority, normalized.ExpiresAt, timestamp, proposalID, domain.MemoryProposalStatusPending)
	if err != nil {
		if isMemoryProposalUniqueError(err) {
			return domain.MemoryProposal{}, ErrMemoryProposalDuplicate
		}
		return domain.MemoryProposal{}, fmt.Errorf("update memory proposal: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return domain.MemoryProposal{}, fmt.Errorf("update memory proposal: %w", err)
	}
	if count == 0 {
		if _, getErr := s.GetMemoryProposal(ctx, proposalID); getErr != nil {
			return domain.MemoryProposal{}, getErr
		}
		return domain.MemoryProposal{}, ErrMemoryProposalResolved
	}
	return s.GetMemoryProposal(ctx, proposalID)
}

func (s *Store) AcceptMemoryProposal(ctx context.Context, proposalID string, replacement *domain.MemoryProposalUpdate) (domain.MemoryProposal, domain.BotMemory, error) {
	if err := domain.ValidateMemoryIdentifier("memory proposal id", proposalID); err != nil {
		return domain.MemoryProposal{}, domain.BotMemory{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.MemoryProposal{}, domain.BotMemory{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// Acquire SQLite's write lock before reading status. This prevents two
	// reviewers from both reading pending and then racing a lock upgrade.
	if _, err := tx.ExecContext(ctx, "UPDATE memory_proposals SET updated_at = updated_at WHERE id = ?", proposalID); err != nil {
		return domain.MemoryProposal{}, domain.BotMemory{}, fmt.Errorf("lock memory proposal for acceptance: %w", err)
	}
	proposal, err := scanMemoryProposal(tx.QueryRowContext(ctx, "SELECT "+memoryProposalColumns+" FROM memory_proposals WHERE id = ?", proposalID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MemoryProposal{}, domain.BotMemory{}, ErrMemoryProposalNotFound
	}
	if err != nil {
		return domain.MemoryProposal{}, domain.BotMemory{}, fmt.Errorf("get memory proposal for acceptance: %w", err)
	}
	if proposal.Status == domain.MemoryProposalStatusAccepted {
		memory, loadErr := scanBotMemory(tx.QueryRowContext(ctx, "SELECT "+memorySelectColumns+" FROM bot_memories WHERE id = ? AND bot_id = ?", proposal.MemoryID, proposal.BotID))
		if errors.Is(loadErr, sql.ErrNoRows) {
			return proposal, domain.BotMemory{}, ErrMemoryProposalAcceptedMemoryDeleted
		}
		if loadErr != nil {
			return domain.MemoryProposal{}, domain.BotMemory{}, fmt.Errorf("load accepted proposal memory: %w", loadErr)
		}
		return proposal, memory, nil
	}
	if proposal.Status != domain.MemoryProposalStatusPending {
		return domain.MemoryProposal{}, domain.BotMemory{}, ErrMemoryProposalResolved
	}
	fields := domain.MemoryProposalUpdate{Category: proposal.Category, Content: proposal.Content, Priority: proposal.Priority, ExpiresAt: proposal.ExpiresAt}
	if replacement != nil {
		fields = *replacement
	}
	normalized, err := domain.NormalizeMemoryProposalUpdate(fields)
	if err != nil {
		return domain.MemoryProposal{}, domain.BotMemory{}, err
	}
	timestamp := now()
	memory := domain.BotMemory{
		ID: id.New("memory"), BotID: proposal.BotID, Category: normalized.Category,
		Status: domain.MemoryStatusApproved, Source: domain.MemorySourceAgentProposal,
		Content: normalized.Content, Priority: normalized.Priority, ExpiresAt: normalized.ExpiresAt,
		CreatedAt: timestamp, UpdatedAt: timestamp,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bot_memories
		(id, bot_id, category, status, source, content, priority, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, memory.ID, memory.BotID, memory.Category,
		memory.Status, memory.Source, memory.Content, memory.Priority, memory.ExpiresAt, memory.CreatedAt, memory.UpdatedAt); err != nil {
		return domain.MemoryProposal{}, domain.BotMemory{}, fmt.Errorf("accept memory proposal: %w", err)
	}
	key, _ := domain.NormalizeMemoryProposalContentKey(normalized.Content)
	result, err := tx.ExecContext(ctx, `UPDATE memory_proposals SET category = ?, status = ?, content = ?,
		normalized_content = ?, priority = ?, expires_at = ?, memory_id = ?, updated_at = ?
		WHERE id = ? AND status = ?`, normalized.Category, domain.MemoryProposalStatusAccepted,
		normalized.Content, key, normalized.Priority, normalized.ExpiresAt, memory.ID, timestamp,
		proposalID, domain.MemoryProposalStatusPending)
	if err != nil {
		return domain.MemoryProposal{}, domain.BotMemory{}, fmt.Errorf("accept memory proposal: %w", err)
	}
	if count, countErr := result.RowsAffected(); countErr != nil {
		return domain.MemoryProposal{}, domain.BotMemory{}, countErr
	} else if count != 1 {
		return domain.MemoryProposal{}, domain.BotMemory{}, ErrMemoryProposalResolved
	}
	if err := tx.Commit(); err != nil {
		return domain.MemoryProposal{}, domain.BotMemory{}, fmt.Errorf("accept memory proposal: %w", err)
	}
	proposal.Category, proposal.Status, proposal.Content = normalized.Category, domain.MemoryProposalStatusAccepted, normalized.Content
	proposal.Priority, proposal.ExpiresAt, proposal.MemoryID, proposal.UpdatedAt = normalized.Priority, normalized.ExpiresAt, memory.ID, timestamp
	return proposal, memory, nil
}

func (s *Store) memoryProposalsHasMemoryFK(ctx context.Context) (bool, error) {
	rows, err := s.db.QueryContext(ctx, "PRAGMA foreign_key_list(memory_proposals)")
	if err != nil {
		return false, fmt.Errorf("inspect memory proposal schema: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sequence, id int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return false, fmt.Errorf("inspect memory proposal schema: %w", err)
		}
		if table == "bot_memories" && from == "memory_id" {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) rebuildMemoryProposalsWithoutMemoryFK(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("rebuild memory proposal schema: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE memory_proposals_replacement (
		id TEXT PRIMARY KEY,
		bot_id TEXT NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
		source_run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		source_message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
		category TEXT NOT NULL CHECK(category IN ('fact', 'preference', 'instruction', 'project')),
		status TEXT NOT NULL CHECK(status IN ('pending', 'accepted', 'rejected')),
		content TEXT NOT NULL CHECK(length(CAST(content AS BLOB)) BETWEEN 1 AND 4096),
		normalized_content TEXT NOT NULL,
		priority INTEGER NOT NULL CHECK(priority BETWEEN 1 AND 5),
		expires_at TEXT NOT NULL DEFAULT '',
		memory_id TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE(bot_id, normalized_content)
	);
	INSERT INTO memory_proposals_replacement
		SELECT id, bot_id, source_run_id, source_message_id, category, status, content,
			normalized_content, priority, expires_at, memory_id, created_at, updated_at
		FROM memory_proposals;
	DROP TABLE memory_proposals;
	ALTER TABLE memory_proposals_replacement RENAME TO memory_proposals;`); err != nil {
		return fmt.Errorf("rebuild memory proposal schema: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("rebuild memory proposal schema: %w", err)
	}
	return nil
}

func (s *Store) RejectMemoryProposal(ctx context.Context, proposalID string) (domain.MemoryProposal, error) {
	if err := domain.ValidateMemoryIdentifier("memory proposal id", proposalID); err != nil {
		return domain.MemoryProposal{}, err
	}
	timestamp := now()
	result, err := s.db.ExecContext(ctx, "UPDATE memory_proposals SET status = ?, updated_at = ? WHERE id = ? AND status = ?", domain.MemoryProposalStatusRejected, timestamp, proposalID, domain.MemoryProposalStatusPending)
	if err != nil {
		return domain.MemoryProposal{}, fmt.Errorf("reject memory proposal: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return domain.MemoryProposal{}, err
	}
	if count == 0 {
		existing, getErr := s.GetMemoryProposal(ctx, proposalID)
		if getErr != nil {
			return domain.MemoryProposal{}, getErr
		}
		if existing.Status == domain.MemoryProposalStatusRejected {
			return existing, nil
		}
		return domain.MemoryProposal{}, ErrMemoryProposalResolved
	}
	return s.GetMemoryProposal(ctx, proposalID)
}

func (s *Store) MemoryProposalSourceMessage(ctx context.Context, run domain.Run) (domain.Message, error) {
	var message domain.Message
	err := s.db.QueryRowContext(ctx, `SELECT id, conversation_id, role, content, created_at,
		COALESCE(kind, ''), COALESCE(author_bot_id, ''), COALESCE(mentions, ''), COALESCE(handoff_id, '')
		FROM messages WHERE conversation_id = ? AND role = 'user' AND created_at <= ?
		ORDER BY created_at DESC, id DESC LIMIT 1`, run.ConversationID, run.CreatedAt).Scan(&message.ID,
		&message.ConversationID, &message.Role, &message.Content, &message.CreatedAt, &message.Kind,
		&message.AuthorBotID, new(string), &message.HandoffID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Message{}, errors.New("current run has no source user message")
	}
	if err != nil {
		return domain.Message{}, fmt.Errorf("load proposal source message: %w", err)
	}
	return message, nil
}

type memoryProposalScanner interface{ Scan(...any) error }

func scanMemoryProposal(scanner memoryProposalScanner) (domain.MemoryProposal, error) {
	var item domain.MemoryProposal
	err := scanner.Scan(&item.ID, &item.BotID, &item.SourceRunID, &item.SourceMessageID, &item.Category,
		&item.Status, &item.Content, &item.Priority, &item.ExpiresAt, &item.MemoryID, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func isMemoryProposalUniqueError(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique constraint") && strings.Contains(text, "memory_proposals")
}
