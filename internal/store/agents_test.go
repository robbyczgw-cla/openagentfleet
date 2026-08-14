package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func openAgentStore(t *testing.T) *Store {
	t.Helper()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	return instance
}

func TestCreateAgentCreatesBotAndFirstConversationAtomically(t *testing.T) {
	instance := openAgentStore(t)
	ctx := context.Background()
	created, err := instance.CreateAgent(ctx, domain.AgentDraft{Name: "Research", Title: "Research teammate", Description: "Finds primary sources."})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if created.Bot.ID == "" || created.Conversation == nil || created.Conversation.BotID != created.Bot.ID {
		t.Fatalf("created agent does not own its first conversation: %#v", created)
	}
	if len(created.Conversations) != 1 || created.Conversation.ID != created.Conversations[0].ID {
		t.Fatalf("created conversations = %#v", created.Conversations)
	}

	if _, err := instance.db.Exec(`CREATE TRIGGER reject_agent_conversation BEFORE INSERT ON conversations
		WHEN NEW.title = 'Refuse conversation' BEGIN SELECT RAISE(ABORT, 'conversation refused'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.CreateAgent(ctx, domain.AgentDraft{Name: "Rejected", Title: "Refuse conversation"}); err == nil {
		t.Fatal("CreateAgent succeeded despite a rejected conversation insert")
	}
	var rejectedBots int
	if err := instance.db.QueryRow("SELECT COUNT(*) FROM bots WHERE name = 'Rejected'").Scan(&rejectedBots); err != nil {
		t.Fatal(err)
	}
	if rejectedBots != 0 {
		t.Fatalf("failed agent left %d bot rows behind", rejectedBots)
	}
}

func TestListAgentsGroupsLegacyConversationsWithoutLoss(t *testing.T) {
	instance := openAgentStore(t)
	ctx := context.Background()
	created, err := instance.CreateAgent(ctx, domain.AgentDraft{Name: "Planner", Title: "Planning teammate"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := instance.CreateConversation(ctx, created.Bot.ID, "Advanced thread")
	if err != nil {
		t.Fatal(err)
	}
	agents, err := instance.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Fatalf("agents = %#v", agents)
	}
	got := agents[0]
	if got.Conversation == nil || got.Conversation.ID != created.Conversation.ID {
		t.Fatalf("canonical conversation = %#v, want %s", got.Conversation, created.Conversation.ID)
	}
	if got.ConversationMode != domain.AgentConversationModeAdvancedMulti || len(got.Conversations) != 2 {
		t.Fatalf("legacy conversations were not losslessly grouped: %#v", got)
	}
	if got.Conversations[1].ID != second.ID {
		t.Fatalf("second conversation = %#v, want %s", got.Conversations, second.ID)
	}
}

func TestListAgentsPreservesLegacyBotWithoutConversation(t *testing.T) {
	instance := openAgentStore(t)
	timestamp := now()
	if _, err := instance.db.Exec("INSERT INTO bots (id, name, title, description, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)", "bot-legacy-empty", "Legacy", "Legacy teammate", "", "idle", timestamp, timestamp); err != nil {
		t.Fatal(err)
	}
	agents, err := instance.ListAgents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Bot.ID != "bot-legacy-empty" || agents[0].Conversation != nil || len(agents[0].Conversations) != 0 {
		t.Fatalf("legacy bot was hidden or given an invented conversation: %#v", agents)
	}
}

func TestCreateAgentValidatesDurableFields(t *testing.T) {
	instance := openAgentStore(t)
	_, err := instance.CreateAgent(context.Background(), domain.AgentDraft{Name: " ", Title: "Title"})
	if err == nil || !strings.Contains(err.Error(), "agent name is required") {
		t.Fatalf("empty name error = %v", err)
	}
	_, err = instance.CreateAgent(context.Background(), domain.AgentDraft{Name: "Name", Title: strings.Repeat("x", domain.MaxAgentTitleBytes+1)})
	if err == nil || !strings.Contains(err.Error(), "agent title") {
		t.Fatalf("oversized title error = %v", err)
	}
}

func TestUpdateAgentProfilePersistsOnlyProfile(t *testing.T) {
	instance := openAgentStore(t)
	created, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Original", Title: "Original teammate", Description: "Before."})
	if err != nil {
		t.Fatal(err)
	}
	name, description := "Updated", "After."
	updated, err := instance.UpdateAgentProfile(t.Context(), created.Bot.ID, domain.AgentProfileUpdate{Name: &name, Description: &description})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Bot.Name != name || updated.Bot.Title != created.Bot.Title || updated.Bot.Description != description {
		t.Fatalf("updated profile = %#v", updated.Bot)
	}
	if updated.Conversation == nil || updated.Conversation.ID != created.Conversation.ID {
		t.Fatalf("profile update changed canonical conversation: %#v", updated)
	}
	if _, err := instance.UpdateAgentProfile(t.Context(), "missing", domain.AgentProfileUpdate{Name: &name}); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("missing agent error = %v", err)
	}
}

func TestAgentMetadataPersistsAcrossListingAndAtomicPatch(t *testing.T) {
	instance := openAgentStore(t)
	metadata := domain.DefaultAgentMetadata()
	metadata.Lead = &domain.AgentExecutionProfile{
		Harness: "grok_build", Model: "grok-4.5", Reasoning: "high", ServiceTier: "default", Permission: "ask",
	}
	metadata.Workers = []domain.AgentExecutionProfile{
		{ID: "reviewer", Harness: "claude", Model: "claude-opus", Reasoning: "xhigh", ServiceTier: "default", Permission: "read_only", MaxTurns: 12, TimeoutSeconds: 600},
		{ID: "researcher", Harness: "pi", Model: "deepseek-v4", Reasoning: "high", ServiceTier: "flex", Permission: "workspace", MaxTurns: 20, TimeoutSeconds: 900},
	}
	metadata.NotifyFinished = false
	created, err := instance.CreateAgentWithMetadata(t.Context(), domain.AgentDraft{
		Name: "Builder", Title: "Builds the project",
	}, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if created.Metadata == nil || created.Metadata.Lead == nil || created.Metadata.Lead.Model != "grok-4.5" || len(created.Metadata.Workers) != 2 || created.MetadataPersistence != domain.AgentMetadataPersisted {
		t.Fatalf("created metadata = %#v", created)
	}

	listed, err := instance.ListAgents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Metadata == nil || listed[0].Metadata.Lead == nil || listed[0].Metadata.Lead.Model != "grok-4.5" || listed[0].Metadata.Workers[1].ServiceTier != "flex" || listed[0].Metadata.NotifyFinished {
		t.Fatalf("listed metadata = %#v", listed)
	}

	updatedMetadata := *listed[0].Metadata
	updatedMetadata.Lead.Model = "grok-4.5-fast"
	name := "Builder Pro"
	updated, err := instance.UpdateAgent(t.Context(), created.Bot.ID, domain.AgentProfileUpdate{Name: &name}, &updatedMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Bot.Name != name || updated.Metadata == nil || updated.Metadata.Lead == nil || updated.Metadata.Lead.Model != "grok-4.5-fast" {
		t.Fatalf("atomic update = %#v", updated)
	}
}

func TestListAgentsMigratesLegacyMetadataWithoutInventingWorkerAuthority(t *testing.T) {
	instance := openAgentStore(t)
	created, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Legacy", Title: "Legacy agent"})
	if err != nil {
		t.Fatal(err)
	}
	legacy := `{"lead_harness":"grok","model":"grok-4.5","worker_ids":["reviewer-a","custom-worker"]}`
	if _, err := instance.db.ExecContext(t.Context(), "UPDATE agent_metadata SET document = ? WHERE bot_id = ?", legacy, created.Bot.ID); err != nil {
		t.Fatal(err)
	}
	agents, err := instance.ListAgents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Metadata == nil || agents[0].Metadata.Lead == nil {
		t.Fatalf("legacy agent = %#v", agents)
	}
	if agents[0].Metadata.Lead.Harness != "grok_build" || len(agents[0].Metadata.Workers) != 0 || len(agents[0].Metadata.WorkerIDs) != 2 {
		t.Fatalf("legacy metadata migration = %#v", agents[0].Metadata)
	}
	if !agents[0].Metadata.NotifyFinished || !agents[0].Metadata.NotifyNeedsInput {
		t.Fatalf("legacy metadata lost notification defaults: %#v", agents[0].Metadata)
	}
}
