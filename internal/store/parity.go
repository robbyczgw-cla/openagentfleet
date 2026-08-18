package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
	"github.com/robbyczgw-cla/openagentfleet/internal/policy"
)

func (s *Store) migrateBotParitySchema() error {
	if err := s.ensureMessageColumn("kind", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureMessageColumn("author_bot_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureMessageColumn("mentions", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureMessageColumn("handoff_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	const extra = `
CREATE TABLE IF NOT EXISTS agent_handoffs (
  id TEXT PRIMARY KEY,
  source_bot_id TEXT NOT NULL REFERENCES bots(id),
  source_conversation_id TEXT NOT NULL REFERENCES conversations(id),
  source_message_id TEXT NOT NULL REFERENCES messages(id),
  target_bot_id TEXT NOT NULL REFERENCES bots(id),
  target_conversation_id TEXT NOT NULL REFERENCES conversations(id),
  target_message_id TEXT NOT NULL REFERENCES messages(id),
  target_run_id TEXT NOT NULL REFERENCES runs(id),
  content TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS agent_handoffs_source_idx ON agent_handoffs(source_conversation_id, created_at);
CREATE INDEX IF NOT EXISTS agent_handoffs_target_idx ON agent_handoffs(target_conversation_id, created_at);
CREATE TABLE IF NOT EXISTS policy_rules (
  id TEXT PRIMARY KEY,
  document TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
`
	if _, err := s.db.Exec(extra); err != nil {
		return fmt.Errorf("migrate bot parity schema: %w", err)
	}
	return nil
}

func (s *Store) ensureMessageColumn(name, definition string) error {
	found, err := hasSQLiteColumn(s.db, "messages", name)
	if err != nil {
		return err
	}
	if found {
		return nil
	}
	if _, err := s.db.Exec("ALTER TABLE messages ADD COLUMN " + name + " " + definition); err != nil {
		return fmt.Errorf("add messages.%s: %w", name, err)
	}
	return nil
}

func hasSQLiteColumn(db *sql.DB, table, name string) (bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, fmt.Errorf("inspect %s schema: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			ordinal    int
			column     string
			dataType   string
			notNull    int
			defaultVal any
			primaryKey int
		)
		if err := rows.Scan(&ordinal, &column, &dataType, &notNull, &defaultVal, &primaryKey); err != nil {
			return false, fmt.Errorf("scan %s schema: %w", table, err)
		}
		if column == name {
			return true, nil
		}
	}
	return false, rows.Err()
}

func scanMessage(scanner interface{ Scan(...any) error }) (domain.Message, error) {
	var item domain.Message
	var mentions string
	if err := scanner.Scan(&item.ID, &item.ConversationID, &item.Role, &item.Content, &item.CreatedAt, &item.Kind, &item.AuthorBotID, &mentions, &item.HandoffID); err != nil {
		return domain.Message{}, err
	}
	if mentions != "" && mentions != "[]" {
		if err := json.Unmarshal([]byte(mentions), &item.Mentions); err != nil {
			return domain.Message{}, fmt.Errorf("decode message mentions: %w", err)
		}
	}
	return item, nil
}

func encodeMentions(mentions []string) string {
	if len(mentions) == 0 {
		return ""
	}
	encoded, err := json.Marshal(mentions)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func (s *Store) CanonicalConversationForBot(ctx context.Context, botID string) (domain.Conversation, error) {
	var result domain.Conversation
	err := s.db.QueryRowContext(ctx, `SELECT id, bot_id, title, created_at, updated_at
		FROM conversations WHERE bot_id = ? ORDER BY created_at ASC, id ASC LIMIT 1`, botID).
		Scan(&result.ID, &result.BotID, &result.Title, &result.CreatedAt, &result.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return result, fmt.Errorf("conversation not found")
	}
	return result, err
}

func (s *Store) GetBot(ctx context.Context, botID string) (domain.Bot, error) {
	var item domain.Bot
	err := s.db.QueryRowContext(ctx, "SELECT id, name, title, description, status, created_at, updated_at FROM bots WHERE id = ?", botID).
		Scan(&item.ID, &item.Name, &item.Title, &item.Description, &item.Status, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, fmt.Errorf("agent not found")
	}
	return item, err
}

type CreateAgentHandoffInput struct {
	SourceConversationID string
	SourceBotID          string
	TargetBotID          string
	TargetConversationID string
	Content              string
	TargetProvider       string
	TargetPrompt         string
}

type CreateAgentHandoffResult struct {
	Handoff       domain.Handoff
	SourceMessage domain.Message
	TargetMessage domain.Message
	Run           domain.Run
	QueuedEvent   domain.RunEvent
}

func (s *Store) CreateAgentHandoff(ctx context.Context, input CreateAgentHandoffInput) (CreateAgentHandoffResult, error) {
	var empty CreateAgentHandoffResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer func() { _ = tx.Rollback() }()

	var sourceBot, targetBot string
	if err := tx.QueryRowContext(ctx, "SELECT bot_id FROM conversations WHERE id = ?", input.SourceConversationID).Scan(&sourceBot); err != nil {
		return empty, fmt.Errorf("load source conversation: %w", err)
	}
	if sourceBot != input.SourceBotID {
		return empty, errors.New("source conversation does not belong to the source agent")
	}
	if err := tx.QueryRowContext(ctx, "SELECT bot_id FROM conversations WHERE id = ?", input.TargetConversationID).Scan(&targetBot); err != nil {
		return empty, fmt.Errorf("load target conversation: %w", err)
	}
	if targetBot != input.TargetBotID {
		return empty, errors.New("target conversation does not belong to the target agent")
	}

	timestamp := now()
	handoffID := id.New("handoff")
	source := domain.Message{
		ID: id.New("msg"), ConversationID: input.SourceConversationID, Role: "user",
		Content: input.Content, CreatedAt: timestamp, Kind: domain.MessageKindHandoff,
		Mentions: []string{input.TargetBotID}, HandoffID: handoffID,
	}
	target := domain.Message{
		ID: id.New("msg"), ConversationID: input.TargetConversationID, Role: "user",
		Content: input.Content, CreatedAt: timestamp, Kind: domain.MessageKindHandoff,
		AuthorBotID: input.SourceBotID, Mentions: []string{input.TargetBotID}, HandoffID: handoffID,
	}
	run := domain.Run{
		ID: id.New("run"), ConversationID: input.TargetConversationID, BotID: input.TargetBotID,
		Provider: input.TargetProvider, Status: "queued", Prompt: input.TargetPrompt,
		CreatedAt: timestamp, UpdatedAt: timestamp,
	}
	event := domain.RunEvent{ID: id.New("evt"), RunID: run.ID, Type: "run.queued", Data: `{"status":"queued"}`, CreatedAt: timestamp}
	handoff := domain.Handoff{
		ID: handoffID, SourceBotID: input.SourceBotID, SourceConversationID: input.SourceConversationID,
		SourceMessageID: source.ID, TargetBotID: input.TargetBotID, TargetConversationID: input.TargetConversationID,
		TargetMessageID: target.ID, TargetRunID: run.ID, Content: input.Content, CreatedAt: timestamp,
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO messages
		(id, conversation_id, role, content, created_at, kind, author_bot_id, mentions, handoff_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		source.ID, source.ConversationID, source.Role, source.Content, source.CreatedAt,
		source.Kind, source.AuthorBotID, encodeMentions(source.Mentions), source.HandoffID); err != nil {
		return empty, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages
		(id, conversation_id, role, content, created_at, kind, author_bot_id, mentions, handoff_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		target.ID, target.ConversationID, target.Role, target.Content, target.CreatedAt,
		target.Kind, target.AuthorBotID, encodeMentions(target.Mentions), target.HandoffID); err != nil {
		return empty, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs
		(id, conversation_id, bot_id, provider, status, prompt, error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.ConversationID, run.BotID, run.Provider, run.Status, run.Prompt, "", run.CreatedAt, run.UpdatedAt); err != nil {
		return empty, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_events (id, run_id, type, data, created_at) VALUES (?, ?, ?, ?, ?)`,
		event.ID, event.RunID, event.Type, event.Data, event.CreatedAt); err != nil {
		return empty, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_handoffs
		(id, source_bot_id, source_conversation_id, source_message_id, target_bot_id, target_conversation_id, target_message_id, target_run_id, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		handoff.ID, handoff.SourceBotID, handoff.SourceConversationID, handoff.SourceMessageID,
		handoff.TargetBotID, handoff.TargetConversationID, handoff.TargetMessageID, handoff.TargetRunID,
		handoff.Content, handoff.CreatedAt); err != nil {
		return empty, err
	}
	if err := tx.Commit(); err != nil {
		return empty, err
	}
	return CreateAgentHandoffResult{Handoff: handoff, SourceMessage: source, TargetMessage: target, Run: run, QueuedEvent: event}, nil
}

func (s *Store) ListPolicyRules(ctx context.Context) ([]policy.Rule, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT document FROM policy_rules ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := make([]policy.Rule, 0)
	for rows.Next() {
		var document string
		if err := rows.Scan(&document); err != nil {
			return nil, err
		}
		var rule policy.Rule
		if err := json.Unmarshal([]byte(document), &rule); err != nil {
			return nil, fmt.Errorf("decode policy rule: %w", err)
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (s *Store) UpsertPolicyRule(ctx context.Context, rule policy.Rule) error {
	if err := policy.ValidateConfig(policy.Config{Version: policy.CurrentVersion, Enabled: true, Rules: []policy.Rule{rule}}); err != nil {
		return err
	}
	document, err := json.Marshal(rule)
	if err != nil {
		return err
	}
	timestamp := now()
	_, err = s.db.ExecContext(ctx, `INSERT INTO policy_rules (id, document, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET document = excluded.document, updated_at = excluded.updated_at`,
		rule.ID, string(document), timestamp, timestamp)
	return err
}
