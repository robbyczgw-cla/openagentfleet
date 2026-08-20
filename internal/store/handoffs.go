package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
)

const activeHandoffStatuses = `'queued', 'running', 'waiting'`

func handoffIsTerminal(status string) bool {
	switch status {
	case "queued", "running", "waiting":
		return false
	default:
		return status != ""
	}
}

func scanHandoff(scanner interface{ Scan(...any) error }) (domain.Handoff, error) {
	var item domain.Handoff
	err := scanner.Scan(
		&item.ID, &item.SourceBotID, &item.SourceConversationID, &item.SourceMessageID,
		&item.TargetBotID, &item.TargetConversationID, &item.TargetMessageID, &item.TargetRunID,
		&item.Content, &item.CreatedAt, &item.Status, &item.Mode, &item.ParentHandoffID, &item.Depth,
		&item.OriginRunID, &item.SourceRunID, &item.Result, &item.CompletedAt, &item.TimeoutSeconds,
	)
	return item, err
}

const handoffSelectColumns = `id, source_bot_id, source_conversation_id, source_message_id,
	target_bot_id, target_conversation_id, target_message_id, target_run_id,
	content, created_at, status, mode, parent_handoff_id, depth,
	origin_run_id, source_run_id, result, completed_at, timeout_seconds`

func (s *Store) GetHandoffByTargetRun(ctx context.Context, runID string) (domain.Handoff, error) {
	var item domain.Handoff
	err := s.db.QueryRowContext(ctx, `SELECT `+handoffSelectColumns+` FROM agent_handoffs WHERE target_run_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`, runID).Scan(
		&item.ID, &item.SourceBotID, &item.SourceConversationID, &item.SourceMessageID,
		&item.TargetBotID, &item.TargetConversationID, &item.TargetMessageID, &item.TargetRunID,
		&item.Content, &item.CreatedAt, &item.Status, &item.Mode, &item.ParentHandoffID, &item.Depth,
		&item.OriginRunID, &item.SourceRunID, &item.Result, &item.CompletedAt, &item.TimeoutSeconds,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Handoff{}, fmt.Errorf("handoff not found")
	}
	return item, err
}

func (s *Store) GetHandoff(ctx context.Context, id string) (domain.Handoff, error) {
	var item domain.Handoff
	err := s.db.QueryRowContext(ctx, `SELECT `+handoffSelectColumns+` FROM agent_handoffs WHERE id = ?`, id).Scan(
		&item.ID, &item.SourceBotID, &item.SourceConversationID, &item.SourceMessageID,
		&item.TargetBotID, &item.TargetConversationID, &item.TargetMessageID, &item.TargetRunID,
		&item.Content, &item.CreatedAt, &item.Status, &item.Mode, &item.ParentHandoffID, &item.Depth,
		&item.OriginRunID, &item.SourceRunID, &item.Result, &item.CompletedAt, &item.TimeoutSeconds,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Handoff{}, fmt.Errorf("handoff not found")
	}
	return item, err
}

func (s *Store) ListHandoffsForConversation(ctx context.Context, conversationID string) ([]domain.Handoff, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+handoffSelectColumns+`
		FROM agent_handoffs
		WHERE source_conversation_id = ? OR target_conversation_id = ?
		ORDER BY created_at, id`, conversationID, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanHandoffRows(rows)
}

func (s *Store) ListActiveHandoffsForOriginRun(ctx context.Context, originRunID string) ([]domain.Handoff, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+handoffSelectColumns+`
		FROM agent_handoffs
		WHERE origin_run_id = ? AND status IN (`+activeHandoffStatuses+`)
		ORDER BY created_at, id`, originRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanHandoffRows(rows)
}

func (s *Store) CountActiveHandoffsForOriginRun(ctx context.Context, originRunID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_handoffs
		WHERE origin_run_id = ? AND status IN (`+activeHandoffStatuses+`)`, originRunID).Scan(&count)
	return count, err
}

func (s *Store) LoadHandoffChain(ctx context.Context, handoffID string) ([]domain.Handoff, error) {
	seen := make(map[string]struct{})
	chain := make([]domain.Handoff, 0)
	currentID := handoffID
	for currentID != "" {
		if _, ok := seen[currentID]; ok {
			return nil, fmt.Errorf("handoff chain cycle at %s", currentID)
		}
		seen[currentID] = struct{}{}
		item, err := s.GetHandoff(ctx, currentID)
		if err != nil {
			return nil, err
		}
		chain = append(chain, item)
		currentID = item.ParentHandoffID
	}
	for left, right := 0, len(chain)-1; left < right; left, right = left+1, right-1 {
		chain[left], chain[right] = chain[right], chain[left]
	}
	return chain, nil
}

func (s *Store) UpdateHandoffStatus(ctx context.Context, id, status, result string) error {
	completedAt := ""
	if handoffIsTerminal(status) {
		completedAt = now()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE agent_handoffs SET status = ?, result = ?, completed_at = CASE
		WHEN ? <> '' THEN ? ELSE completed_at END
		WHERE id = ?`, status, result, completedAt, completedAt, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("handoff not found")
	}
	return nil
}

func (s *Store) AppendHandoffResultToSource(ctx context.Context, handoffID, content string) (domain.Message, error) {
	handoff, err := s.GetHandoff(ctx, handoffID)
	if err != nil {
		return domain.Message{}, err
	}
	message := domain.Message{
		ID:             id.New("msg"),
		ConversationID: handoff.SourceConversationID,
		Role:           "assistant",
		Content:        content,
		CreatedAt:      now(),
		Kind:           domain.MessageKindHandoffResult,
		AuthorBotID:    handoff.TargetBotID,
		HandoffID:      handoff.ID,
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO messages
		(id, conversation_id, role, content, created_at, kind, author_bot_id, mentions, handoff_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		message.ID, message.ConversationID, message.Role, message.Content, message.CreatedAt,
		message.Kind, message.AuthorBotID, encodeMentions(message.Mentions), message.HandoffID); err != nil {
		return domain.Message{}, err
	}
	return message, nil
}

func scanHandoffRows(rows *sql.Rows) ([]domain.Handoff, error) {
	result := make([]domain.Handoff, 0)
	for rows.Next() {
		item, err := scanHandoff(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
