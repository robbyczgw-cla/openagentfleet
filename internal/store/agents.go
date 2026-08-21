package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
)

var ErrAgentNotFound = errors.New("agent not found")

// CreateAgent creates the durable bot and its first durable conversation in
// one transaction. No partially-created bot can be committed if the thread
// insert fails.
func (s *Store) CreateAgent(ctx context.Context, draft domain.AgentDraft) (domain.Agent, error) {
	return s.CreateAgentWithMetadata(ctx, draft, domain.DefaultAgentMetadata())
}

// CreateAgentWithMetadata creates profile, configuration and first chat in one
// transaction. A failed metadata or conversation write cannot leave a partial
// Agent behind.
func (s *Store) CreateAgentWithMetadata(ctx context.Context, draft domain.AgentDraft, metadata domain.AgentMetadata) (domain.Agent, error) {
	draft, err := domain.NormalizeAgentDraft(draft)
	if err != nil {
		return domain.Agent{}, err
	}
	metadata, err = domain.NormalizeAgentMetadata(metadata)
	if err != nil {
		return domain.Agent{}, err
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return domain.Agent{}, fmt.Errorf("encode agent metadata: %w", err)
	}
	timestamp := now()
	bot := domain.Bot{
		ID: id.New("bot"), Name: draft.Name, Title: draft.Title, Description: draft.Description,
		Status: "idle", CreatedAt: timestamp, UpdatedAt: timestamp,
	}
	conversation := domain.Conversation{
		ID: id.New("conv"), BotID: bot.ID, Title: draft.ConversationTitle,
		CreatedAt: timestamp, UpdatedAt: timestamp,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Agent{}, fmt.Errorf("begin agent creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "INSERT INTO bots (id, name, title, description, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)", bot.ID, bot.Name, bot.Title, bot.Description, bot.Status, bot.CreatedAt, bot.UpdatedAt); err != nil {
		return domain.Agent{}, fmt.Errorf("create agent bot: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO agent_metadata (bot_id, document, updated_at) VALUES (?, ?, ?)", bot.ID, string(metadataJSON), timestamp); err != nil {
		return domain.Agent{}, fmt.Errorf("create agent metadata: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO conversations (id, bot_id, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?)", conversation.ID, conversation.BotID, conversation.Title, conversation.CreatedAt, conversation.UpdatedAt); err != nil {
		return domain.Agent{}, fmt.Errorf("create agent conversation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.Agent{}, fmt.Errorf("commit agent creation: %w", err)
	}
	result := newAgent(bot, []domain.Conversation{conversation})
	result.Metadata = &metadata
	return result, nil
}

// ListAgents returns one entry per bot. Its read transaction keeps each agent
// grouping internally consistent while preserving every legacy conversation.
func (s *Store) ListAgents(ctx context.Context) ([]domain.Agent, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin agent listing: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT
		b.id, b.name, b.title, b.description, b.status, b.created_at, b.updated_at,
		c.id, COALESCE(c.bot_id, ''), COALESCE(c.title, ''), COALESCE(c.created_at, ''), COALESCE(c.updated_at, '')
		FROM bots b
		LEFT JOIN conversations c ON c.bot_id = b.id
		ORDER BY b.created_at ASC, b.id ASC, c.created_at ASC, c.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()

	items := make([]domain.Agent, 0)
	byBotID := make(map[string]int)
	for rows.Next() {
		var bot domain.Bot
		var conversation domain.Conversation
		var conversationID sql.NullString
		if err := rows.Scan(
			&bot.ID, &bot.Name, &bot.Title, &bot.Description, &bot.Status, &bot.CreatedAt, &bot.UpdatedAt,
			&conversationID, &conversation.BotID, &conversation.Title, &conversation.CreatedAt, &conversation.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		index, exists := byBotID[bot.ID]
		if !exists {
			items = append(items, newAgent(bot, nil))
			index = len(items) - 1
			byBotID[bot.ID] = index
		}
		if conversationID.Valid {
			conversation.ID = conversationID.String
			items[index].Conversations = append(items[index].Conversations, conversation)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agents: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close agent rows: %w", err)
	}
	for index := range items {
		metadata, metadataErr := loadAgentMetadata(ctx, tx, items[index].Bot.ID)
		if metadataErr != nil {
			return nil, metadataErr
		}
		items[index].Metadata = &metadata
		items[index].MetadataPersistence = domain.AgentMetadataPersisted
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit agent listing: %w", err)
	}
	for index := range items {
		metadata := items[index].Metadata
		items[index] = newAgent(items[index].Bot, items[index].Conversations)
		items[index].Metadata = metadata
	}
	if err := s.applyAgentRoster(ctx, items); err != nil {
		return nil, err
	}
	return items, nil
}

// UpdateAgentProfile atomically changes the durable bot profile.
func (s *Store) UpdateAgentProfile(ctx context.Context, botID string, update domain.AgentProfileUpdate) (domain.Agent, error) {
	return s.UpdateAgent(ctx, botID, update, nil)
}

// UpdateAgent applies profile and optional metadata as one transaction.
func (s *Store) UpdateAgent(ctx context.Context, botID string, update domain.AgentProfileUpdate, metadata *domain.AgentMetadata) (domain.Agent, error) {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	return s.updateAgent(ctx, botID, update, metadata)
}

// PatchAgent serializes the metadata read-merge-write sequence inside the
// Store. OpenAgentFleet uses one Store per local control-plane database, so two
// concurrent partial HTTP patches cannot silently overwrite each other.
func (s *Store) PatchAgent(ctx context.Context, botID string, update domain.AgentProfileUpdate, merge func(domain.AgentMetadata) (domain.AgentMetadata, error)) (domain.Agent, error) {
	if merge == nil {
		return domain.Agent{}, errors.New("agent metadata merge is required")
	}
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	agents, err := s.ListAgents(ctx)
	if err != nil {
		return domain.Agent{}, err
	}
	for _, agent := range agents {
		if agent.Bot.ID != botID {
			continue
		}
		metadata := domain.DefaultAgentMetadata()
		if agent.Metadata != nil {
			metadata = *agent.Metadata
		}
		merged, err := merge(metadata)
		if err != nil {
			return domain.Agent{}, err
		}
		return s.updateAgent(ctx, botID, update, &merged)
	}
	return domain.Agent{}, ErrAgentNotFound
}

func (s *Store) updateAgent(ctx context.Context, botID string, update domain.AgentProfileUpdate, metadata *domain.AgentMetadata) (domain.Agent, error) {
	if botID == "" {
		return domain.Agent{}, ErrAgentNotFound
	}
	if update.Name == nil && update.Title == nil && update.Description == nil && metadata == nil {
		return domain.Agent{}, errors.New("at least one agent field is required")
	}
	var metadataJSON []byte
	var err error
	if metadata != nil {
		normalized, normalizeErr := domain.NormalizeAgentMetadata(*metadata)
		if normalizeErr != nil {
			return domain.Agent{}, normalizeErr
		}
		metadata = &normalized
		metadataJSON, err = json.Marshal(normalized)
		if err != nil {
			return domain.Agent{}, fmt.Errorf("encode agent metadata: %w", err)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Agent{}, fmt.Errorf("begin agent profile update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var existing domain.Bot
	err = tx.QueryRowContext(ctx, "SELECT id, name, title, description, status, created_at, updated_at FROM bots WHERE id = ?", botID).Scan(&existing.ID, &existing.Name, &existing.Title, &existing.Description, &existing.Status, &existing.CreatedAt, &existing.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Agent{}, ErrAgentNotFound
	}
	if err != nil {
		return domain.Agent{}, fmt.Errorf("load agent profile: %w", err)
	}
	name, title, description := existing.Name, existing.Title, existing.Description
	if update.Name != nil {
		name = *update.Name
	}
	if update.Title != nil {
		title = *update.Title
	}
	if update.Description != nil {
		description = *update.Description
	}
	profile, err := domain.NormalizeAgentProfile(name, title, description)
	if err != nil {
		return domain.Agent{}, err
	}
	updatedAt := now()
	if update.Name != nil || update.Title != nil || update.Description != nil {
		if _, err := tx.ExecContext(ctx, "UPDATE bots SET name = ?, title = ?, description = ?, updated_at = ? WHERE id = ?", profile.Name, profile.Title, profile.Description, updatedAt, botID); err != nil {
			return domain.Agent{}, fmt.Errorf("update agent profile: %w", err)
		}
	} else if metadata != nil {
		if _, err := tx.ExecContext(ctx, "UPDATE bots SET updated_at = ? WHERE id = ?", updatedAt, botID); err != nil {
			return domain.Agent{}, fmt.Errorf("update agent timestamp: %w", err)
		}
	}
	if metadata != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_metadata (bot_id, document, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(bot_id) DO UPDATE SET document = excluded.document, updated_at = excluded.updated_at`, botID, string(metadataJSON), updatedAt); err != nil {
			return domain.Agent{}, fmt.Errorf("update agent metadata: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.Agent{}, fmt.Errorf("commit agent profile update: %w", err)
	}
	agents, err := s.ListAgents(ctx)
	if err != nil {
		return domain.Agent{}, fmt.Errorf("list updated agent: %w", err)
	}
	for _, agent := range agents {
		if agent.Bot.ID == botID {
			return agent, nil
		}
	}
	return domain.Agent{}, ErrAgentNotFound
}

type metadataQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadAgentMetadata(ctx context.Context, query metadataQuery, botID string) (domain.AgentMetadata, error) {
	var document string
	err := query.QueryRowContext(ctx, "SELECT document FROM agent_metadata WHERE bot_id = ?", botID).Scan(&document)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DefaultAgentMetadata(), nil
	}
	if err != nil {
		return domain.AgentMetadata{}, fmt.Errorf("load agent metadata: %w", err)
	}
	metadata := domain.DefaultAgentMetadata()
	if err := json.Unmarshal([]byte(document), &metadata); err != nil {
		return domain.AgentMetadata{}, fmt.Errorf("decode agent metadata: %w", err)
	}
	metadata, err = domain.NormalizeAgentMetadata(metadata)
	if err != nil {
		return domain.AgentMetadata{}, fmt.Errorf("validate agent metadata: %w", err)
	}
	return metadata, nil
}

func agentMetadataDocument(metadata domain.AgentMetadata) (string, error) {
	metadata, err := domain.NormalizeAgentMetadata(metadata)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("encode agent metadata: %w", err)
	}
	return string(encoded), nil
}

func newAgent(bot domain.Bot, conversations []domain.Conversation) domain.Agent {
	if conversations == nil {
		conversations = []domain.Conversation{}
	}
	item := domain.Agent{
		Bot:                 bot,
		Conversations:       conversations,
		ConversationMode:    domain.AgentConversationModeSingle,
		MetadataPersistence: domain.AgentMetadataPersisted,
	}
	metadata := domain.DefaultAgentMetadata()
	item.Metadata = &metadata
	if len(conversations) > 0 {
		canonical := conversations[0]
		item.Conversation = &canonical
	}
	if len(conversations) > 1 {
		item.ConversationMode = domain.AgentConversationModeAdvancedMulti
	}
	return item
}
