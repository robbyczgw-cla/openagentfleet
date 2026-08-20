package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
)

var (
	ErrGroupNotFound      = errors.New("group not found")
	ErrGroupAgentNotFound = errors.New("group agent not found")
	ErrGroupMemberMissing = errors.New("mentioned agent is not a group member")
)

const groupSelectColumns = `id, title, created_at, updated_at`

const groupMessageSelectColumns = `id, group_id, role, content, created_at,
	COALESCE(kind, ''), COALESCE(author_bot_id, ''), COALESCE(mentions, '')`

const groupRunSelectColumns = `id, group_id, bot_id, message_id, status, prompt, error, created_at, updated_at`

// MigrateGroups is additive and idempotent. Store.Open does not call it yet;
// group store methods invoke it so Phase 2 can land without editing store.go.
func (s *Store) MigrateGroups(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS group_conversations (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS group_members (
  group_id TEXT NOT NULL REFERENCES group_conversations(id) ON DELETE CASCADE,
  bot_id TEXT NOT NULL REFERENCES bots(id),
  created_at TEXT NOT NULL,
  PRIMARY KEY (group_id, bot_id)
);
CREATE INDEX IF NOT EXISTS group_members_bot_idx ON group_members(bot_id);
CREATE TABLE IF NOT EXISTS group_messages (
  id TEXT PRIMARY KEY,
  group_id TEXT NOT NULL REFERENCES group_conversations(id) ON DELETE CASCADE,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT '',
  author_bot_id TEXT NOT NULL DEFAULT '',
  mentions TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS group_messages_group_idx ON group_messages(group_id, created_at, id);
CREATE TABLE IF NOT EXISTS group_runs (
  id TEXT PRIMARY KEY,
  group_id TEXT NOT NULL REFERENCES group_conversations(id) ON DELETE CASCADE,
  bot_id TEXT NOT NULL REFERENCES bots(id),
  message_id TEXT NOT NULL REFERENCES group_messages(id),
  status TEXT NOT NULL,
  prompt TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS group_runs_group_idx ON group_runs(group_id, created_at, id);
CREATE INDEX IF NOT EXISTS group_runs_message_idx ON group_runs(message_id);
CREATE INDEX IF NOT EXISTS group_runs_bot_idx ON group_runs(bot_id, created_at);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate group schema: %w", err)
	}
	return nil
}

func (s *Store) CreateGroup(ctx context.Context, title string, agentIDs []string) (domain.Group, error) {
	if err := s.MigrateGroups(ctx); err != nil {
		return domain.Group{}, err
	}
	agentIDs, err := domain.NormalizeGroupAgentIDs(agentIDs)
	if err != nil {
		return domain.Group{}, err
	}
	title = domain.NormalizeGroupTitle(title)
	timestamp := now()
	group := domain.Group{
		ID: id.New("grp"), Title: title, CreatedAt: timestamp, UpdatedAt: timestamp, AgentIDs: agentIDs,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Group{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO group_conversations (id, title, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		group.ID, group.Title, group.CreatedAt, group.UpdatedAt); err != nil {
		return domain.Group{}, err
	}
	members := make([]domain.GroupMember, 0, len(agentIDs))
	for _, botID := range agentIDs {
		bot, err := s.getBotTx(ctx, tx, botID)
		if err != nil {
			return domain.Group{}, err
		}
		member := domain.GroupMember{GroupID: group.ID, BotID: bot.ID, Name: bot.Name, Title: bot.Title, CreatedAt: timestamp}
		if _, err := tx.ExecContext(ctx, `INSERT INTO group_members (group_id, bot_id, created_at) VALUES (?, ?, ?)`,
			member.GroupID, member.BotID, member.CreatedAt); err != nil {
			return domain.Group{}, err
		}
		members = append(members, member)
	}
	if err := tx.Commit(); err != nil {
		return domain.Group{}, err
	}
	group.Members = members
	return group, nil
}

func (s *Store) ListGroups(ctx context.Context) ([]domain.Group, error) {
	if err := s.MigrateGroups(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+groupSelectColumns+` FROM group_conversations ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := make([]domain.Group, 0)
	for rows.Next() {
		item, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		groups = append(groups, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range groups {
		members, err := s.ListGroupMembers(ctx, groups[i].ID)
		if err != nil {
			return nil, err
		}
		groups[i].Members = members
		groups[i].AgentIDs = agentIDsFromMembers(members)
	}
	return groups, nil
}

func (s *Store) GetGroup(ctx context.Context, groupID string) (domain.Group, error) {
	if err := s.MigrateGroups(ctx); err != nil {
		return domain.Group{}, err
	}
	item, err := scanGroup(s.db.QueryRowContext(ctx, `SELECT `+groupSelectColumns+` FROM group_conversations WHERE id = ?`, groupID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Group{}, ErrGroupNotFound
	}
	if err != nil {
		return domain.Group{}, err
	}
	members, err := s.ListGroupMembers(ctx, item.ID)
	if err != nil {
		return domain.Group{}, err
	}
	item.Members = members
	item.AgentIDs = agentIDsFromMembers(members)
	return item, nil
}

func (s *Store) ListGroupMembers(ctx context.Context, groupID string) ([]domain.GroupMember, error) {
	if err := s.MigrateGroups(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.group_id, m.bot_id, b.name, b.title, m.created_at
		FROM group_members m JOIN bots b ON b.id = m.bot_id
		WHERE m.group_id = ? ORDER BY m.created_at, m.bot_id`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := make([]domain.GroupMember, 0)
	for rows.Next() {
		var item domain.GroupMember
		if err := rows.Scan(&item.GroupID, &item.BotID, &item.Name, &item.Title, &item.CreatedAt); err != nil {
			return nil, err
		}
		members = append(members, item)
	}
	return members, rows.Err()
}

type CreateGroupMessageInput struct {
	GroupID       string
	Role          string
	Content       string
	AuthorBotID   string
	MentionBotIDs []string
	Kind          string
}

type CreateGroupMessageResult struct {
	Message domain.GroupMessage
	Runs    []domain.GroupRun
}

// CreateGroupMessage writes only group_messages (and optional group_runs).
// It never inserts into messages, conversations, runs, or bot_memories.
func (s *Store) CreateGroupMessage(ctx context.Context, input CreateGroupMessageInput) (CreateGroupMessageResult, error) {
	var empty CreateGroupMessageResult
	if err := s.MigrateGroups(ctx); err != nil {
		return empty, err
	}
	content := strings.TrimSpace(input.Content)
	if content == "" {
		return empty, errors.New("content is required")
	}
	group, err := s.GetGroup(ctx, input.GroupID)
	if err != nil {
		return empty, err
	}
	role := strings.TrimSpace(input.Role)
	if role == "" {
		if strings.TrimSpace(input.AuthorBotID) == "" {
			role = "user"
		} else {
			role = "assistant"
		}
	}
	mentions := domain.UniqueMentionBotIDs(input.MentionBotIDs)
	memberSet := domain.GroupMemberSet(group.Members)
	for _, botID := range mentions {
		if _, ok := memberSet[botID]; !ok {
			return empty, ErrGroupMemberMissing
		}
	}
	authorBotID := strings.TrimSpace(input.AuthorBotID)
	if authorBotID != "" {
		if _, ok := memberSet[authorBotID]; !ok {
			return empty, ErrGroupMemberMissing
		}
	}
	kind := input.Kind
	if kind == "" {
		kind = domain.MessageKindGroup
	}
	timestamp := now()
	message := domain.GroupMessage{
		ID: id.New("gmsg"), GroupID: group.ID, Role: role, Content: content,
		CreatedAt: timestamp, Kind: kind, AuthorBotID: authorBotID, Mentions: mentions,
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO group_messages
		(id, group_id, role, content, created_at, kind, author_bot_id, mentions)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		message.ID, message.GroupID, message.Role, message.Content, message.CreatedAt,
		message.Kind, message.AuthorBotID, encodeMentions(message.Mentions)); err != nil {
		return empty, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE group_conversations SET updated_at = ? WHERE id = ?`, timestamp, group.ID); err != nil {
		return empty, err
	}

	history, err := listGroupMessagesTx(ctx, tx, group.ID)
	if err != nil {
		return empty, err
	}
	contextText := domain.FormatGroupContext(history, group.Members)
	runs := make([]domain.GroupRun, 0, len(mentions))
	if authorBotID == "" {
		for _, botID := range mentions {
			bot, err := s.getBotTx(ctx, tx, botID)
			if err != nil {
				return empty, err
			}
			prompt := groupRunPrompt(bot, contextText, content)
			run := domain.GroupRun{
				ID: id.New("grun"), GroupID: group.ID, BotID: botID, MessageID: message.ID,
				Status: domain.GroupRunStatusQueued, Prompt: prompt, CreatedAt: timestamp, UpdatedAt: timestamp,
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO group_runs
				(id, group_id, bot_id, message_id, status, prompt, error, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				run.ID, run.GroupID, run.BotID, run.MessageID, run.Status, run.Prompt, "", run.CreatedAt, run.UpdatedAt); err != nil {
				return empty, err
			}
			runs = append(runs, run)
		}
	}
	if err := tx.Commit(); err != nil {
		return empty, err
	}
	return CreateGroupMessageResult{Message: message, Runs: runs}, nil
}

// CreateGroupAgentReply records an assistant row with the producing Agent's
// author_bot_id. Mentions on a reply do not auto-start extra user-mention runs;
// peer routing belongs on RouteGroupAgentMention + CreateAgentHandoff.
func (s *Store) CreateGroupAgentReply(ctx context.Context, groupID, authorBotID, content string, mentionBotIDs []string) (domain.GroupMessage, error) {
	result, err := s.CreateGroupMessage(ctx, CreateGroupMessageInput{
		GroupID:       groupID,
		Role:          "assistant",
		Content:       content,
		AuthorBotID:   authorBotID,
		MentionBotIDs: mentionBotIDs,
	})
	return result.Message, err
}

func (s *Store) ListGroupMessages(ctx context.Context, groupID string) ([]domain.GroupMessage, error) {
	if err := s.MigrateGroups(ctx); err != nil {
		return nil, err
	}
	if _, err := s.GetGroup(ctx, groupID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+groupMessageSelectColumns+`
		FROM group_messages WHERE group_id = ? ORDER BY created_at, id`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGroupMessageRows(rows)
}

func (s *Store) UpdateGroupRunStatus(ctx context.Context, id, status, errText string) (domain.GroupRun, error) {
	if err := s.MigrateGroups(ctx); err != nil {
		return domain.GroupRun{}, err
	}
	switch status {
	case domain.GroupRunStatusQueued, domain.GroupRunStatusRunning, domain.GroupRunStatusCompleted, domain.GroupRunStatusFailed:
	default:
		return domain.GroupRun{}, fmt.Errorf("invalid group run status %q", status)
	}
	timestamp := now()
	result, err := s.db.ExecContext(ctx, `UPDATE group_runs SET status = ?, error = ?, updated_at = ? WHERE id = ?`,
		status, errText, timestamp, id)
	if err != nil {
		return domain.GroupRun{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return domain.GroupRun{}, err
	}
	if updated != 1 {
		return domain.GroupRun{}, fmt.Errorf("group run not found")
	}
	item, err := scanGroupRun(s.db.QueryRowContext(ctx, `SELECT `+groupRunSelectColumns+` FROM group_runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GroupRun{}, fmt.Errorf("group run not found")
	}
	return item, err
}

func (s *Store) ListGroupRuns(ctx context.Context, groupID string) ([]domain.GroupRun, error) {
	if err := s.MigrateGroups(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+groupRunSelectColumns+`
		FROM group_runs WHERE group_id = ? ORDER BY created_at, id`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]domain.GroupRun, 0)
	for rows.Next() {
		item, err := scanGroupRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, item)
	}
	return runs, rows.Err()
}

func (s *Store) CountCanonicalMessagesForBot(ctx context.Context, botID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages m
		JOIN conversations c ON c.id = m.conversation_id WHERE c.bot_id = ?`, botID).Scan(&count)
	return count, err
}

func (s *Store) CountCanonicalRunsForBot(ctx context.Context, botID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE bot_id = ?`, botID).Scan(&count)
	return count, err
}

func groupRunPrompt(bot domain.Bot, contextText, userContent string) string {
	var b strings.Builder
	b.WriteString(domain.GroupSpeakerSystemPrompt(bot))
	b.WriteString("\n\nGroup conversation (bounded, not this Agent's canonical chat; do not ingest as memory):\n")
	if strings.TrimSpace(contextText) != "" {
		b.WriteString(contextText)
		b.WriteString("\n")
	}
	b.WriteString("\nYou were mentioned. Reply only as ")
	b.WriteString(bot.Name)
	b.WriteString(" to:\n")
	b.WriteString(userContent)
	return b.String()
}

func (s *Store) getBotTx(ctx context.Context, tx *sql.Tx, botID string) (domain.Bot, error) {
	var item domain.Bot
	err := tx.QueryRowContext(ctx, "SELECT id, name, title, description, status, created_at, updated_at FROM bots WHERE id = ?", botID).
		Scan(&item.ID, &item.Name, &item.Title, &item.Description, &item.Status, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrGroupAgentNotFound
	}
	return item, err
}

func listGroupMessagesTx(ctx context.Context, tx *sql.Tx, groupID string) ([]domain.GroupMessage, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+groupMessageSelectColumns+`
		FROM group_messages WHERE group_id = ? ORDER BY created_at, id`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGroupMessageRows(rows)
}

func scanGroup(scanner interface{ Scan(...any) error }) (domain.Group, error) {
	var item domain.Group
	err := scanner.Scan(&item.ID, &item.Title, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func scanGroupMessageRows(rows *sql.Rows) ([]domain.GroupMessage, error) {
	result := make([]domain.GroupMessage, 0)
	for rows.Next() {
		item, err := scanGroupMessage(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func scanGroupMessage(scanner interface{ Scan(...any) error }) (domain.GroupMessage, error) {
	var item domain.GroupMessage
	var mentions string
	if err := scanner.Scan(&item.ID, &item.GroupID, &item.Role, &item.Content, &item.CreatedAt, &item.Kind, &item.AuthorBotID, &mentions); err != nil {
		return domain.GroupMessage{}, err
	}
	if mentions != "" && mentions != "[]" {
		if err := json.Unmarshal([]byte(mentions), &item.Mentions); err != nil {
			return domain.GroupMessage{}, fmt.Errorf("decode group message mentions: %w", err)
		}
	}
	return item, nil
}

func scanGroupRun(scanner interface{ Scan(...any) error }) (domain.GroupRun, error) {
	var item domain.GroupRun
	err := scanner.Scan(&item.ID, &item.GroupID, &item.BotID, &item.MessageID, &item.Status, &item.Prompt, &item.Error, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func agentIDsFromMembers(members []domain.GroupMember) []string {
	ids := make([]string, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.BotID)
	}
	return ids
}
