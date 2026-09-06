package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestMemoryProposalLifecycleAndRetrievalBoundary(t *testing.T) {
	instance, botID, _, run, message := openMemoryProposalTestStore(t)
	draft := domain.MemoryProposalDraft{
		BotID: botID, SourceRunID: run.ID, SourceMessageID: message.ID,
		Category: domain.MemoryCategoryFact, Content: "  The project uses Go.  ", Priority: 4,
	}
	proposal, err := instance.CreateMemoryProposal(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Status != domain.MemoryProposalStatusPending || proposal.Content != "The project uses Go." || proposal.MemoryID != "" {
		t.Fatalf("proposal = %#v", proposal)
	}
	memories, err := instance.RetrieveBotMemories(t.Context(), botID, 20, 12*1024)
	if err != nil || len(memories) != 0 {
		t.Fatalf("pending proposal entered retrieval: %#v, %v", memories, err)
	}

	updated, err := instance.UpdateMemoryProposal(t.Context(), proposal.ID, domain.MemoryProposalUpdate{
		Category: domain.MemoryCategoryProject, Content: "The service is written in Go.", Priority: 5,
	})
	if err != nil || updated.Category != domain.MemoryCategoryProject {
		t.Fatalf("update = %#v, %v", updated, err)
	}
	accepted, memory, err := instance.AcceptMemoryProposal(t.Context(), proposal.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.MemoryID != memory.ID || memory.Source != domain.MemorySourceAgentProposal || memory.Status != domain.MemoryStatusApproved {
		t.Fatalf("acceptance = %#v, %#v", accepted, memory)
	}
	memories, err = instance.RetrieveBotMemories(t.Context(), botID, 20, 12*1024)
	if err != nil || len(memories) != 1 || memories[0].ID != memory.ID {
		t.Fatalf("accepted memory retrieval = %#v, %v", memories, err)
	}
	repeated, repeatedMemory, err := instance.AcceptMemoryProposal(t.Context(), proposal.ID, nil)
	if err != nil || repeated.MemoryID != memory.ID || repeatedMemory.ID != memory.ID {
		t.Fatalf("idempotent accept = %#v, %#v, %v", repeated, repeatedMemory, err)
	}
}

func TestMemoryProposalDedupeCapsAndProvenance(t *testing.T) {
	instance, botID, conversation, run, message := openMemoryProposalTestStore(t)
	base := domain.MemoryProposalDraft{
		BotID: botID, SourceRunID: run.ID, SourceMessageID: message.ID,
		Category: domain.MemoryCategoryFact, Content: "One  durable\n fact", Priority: 3,
	}
	if _, err := instance.CreateMemoryProposal(t.Context(), base); err != nil {
		t.Fatal(err)
	}
	duplicate := base
	duplicate.Content = " one durable fact "
	if _, err := instance.CreateMemoryProposal(t.Context(), duplicate); !errors.Is(err, ErrMemoryProposalDuplicate) {
		t.Fatalf("duplicate error = %v", err)
	}
	other, err := instance.CreateAgent(t.Context(), domain.AgentDraft{Name: "Other", Title: "Other", Description: "Other agent."})
	if err != nil {
		t.Fatal(err)
	}
	forged := base
	forged.BotID = other.Bot.ID
	forged.Content = "forged bot scope"
	if _, err := instance.CreateMemoryProposal(t.Context(), forged); err == nil {
		t.Fatal("proposal accepted a bot that did not own its run")
	}
	foreignMessage, err := instance.CreateMessage(t.Context(), conversation.ID, "assistant", "not user source")
	if err != nil {
		t.Fatal(err)
	}
	forged = base
	forged.SourceMessageID = foreignMessage.ID
	forged.Content = "forged message scope"
	if _, err := instance.CreateMemoryProposal(t.Context(), forged); err == nil {
		t.Fatal("proposal accepted an assistant source message")
	}
	for index := 1; index < domain.MemoryProposalPerRun; index++ {
		item := base
		item.Content = "bounded fact " + string(rune('a'+index))
		if _, err := instance.CreateMemoryProposal(t.Context(), item); err != nil {
			t.Fatalf("proposal %d: %v", index, err)
		}
	}
	over := base
	over.Content = "one too many"
	if _, err := instance.CreateMemoryProposal(t.Context(), over); !errors.Is(err, ErrMemoryProposalLimit) {
		t.Fatalf("run cap error = %v", err)
	}
}

func TestMemoryProposalAcceptRaceCreatesOneMemory(t *testing.T) {
	instance, botID, _, run, message := openMemoryProposalTestStore(t)
	proposal, err := instance.CreateMemoryProposal(t.Context(), domain.MemoryProposalDraft{
		BotID: botID, SourceRunID: run.ID, SourceMessageID: message.ID,
		Category: domain.MemoryCategoryPreference, Content: "Prefer concise output.", Priority: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, acceptErr := instance.AcceptMemoryProposal(context.Background(), proposal.ID, nil)
			errorsSeen <- acceptErr
		}()
	}
	close(start)
	wg.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil && !errors.Is(err, ErrMemoryProposalResolved) {
			t.Fatalf("accept race error = %v", err)
		}
	}
	var count int
	if err := instance.db.QueryRow("SELECT COUNT(*) FROM bot_memories WHERE bot_id = ? AND source = ?", botID, domain.MemorySourceAgentProposal).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("accepted memory count = %d", count)
	}
}

func TestMemoryProposalAcceptanceRechecksSecrets(t *testing.T) {
	instance, botID, _, run, message := openMemoryProposalTestStore(t)
	proposal, err := instance.CreateMemoryProposal(t.Context(), domain.MemoryProposalDraft{
		BotID: botID, SourceRunID: run.ID, SourceMessageID: message.ID,
		Category: domain.MemoryCategoryFact, Content: "Safe proposal", Priority: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	unsafe := domain.MemoryProposalUpdate{
		Category: domain.MemoryCategoryFact, Content: "API_KEY=sk-1234567890abcdefghijklmnop", Priority: 3,
	}
	if _, _, err := instance.AcceptMemoryProposal(t.Context(), proposal.ID, &unsafe); err == nil {
		t.Fatal("acceptance stored a reviewer edit containing a secret")
	}
	loaded, err := instance.GetMemoryProposal(t.Context(), proposal.ID)
	if err != nil || loaded.Status != domain.MemoryProposalStatusPending || loaded.MemoryID != "" {
		t.Fatalf("proposal changed after rejected secret = %#v, %v", loaded, err)
	}
	var count int
	if err := instance.db.QueryRow("SELECT COUNT(*) FROM bot_memories WHERE bot_id = ?", botID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("secret acceptance created %d memories", count)
	}
}

func TestAcceptedProposalSurvivesMemoryDeletionWithoutRecreation(t *testing.T) {
	instance, botID, _, run, message := openMemoryProposalTestStore(t)
	proposal, err := instance.CreateMemoryProposal(t.Context(), domain.MemoryProposalDraft{
		BotID: botID, SourceRunID: run.ID, SourceMessageID: message.ID,
		Category: domain.MemoryCategoryFact, Content: "Temporary approved context", Priority: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, memory, err := instance.AcceptMemoryProposal(t.Context(), proposal.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.DeleteBotMemory(t.Context(), botID, memory.ID); err != nil {
		t.Fatal(err)
	}
	memories, err := instance.RetrieveBotMemories(t.Context(), botID, 20, 12*1024)
	if err != nil || len(memories) != 0 {
		t.Fatalf("deleted accepted memory remained retrievable: %#v, %v", memories, err)
	}
	loaded, err := instance.GetMemoryProposal(t.Context(), proposal.ID)
	if err != nil || loaded.Status != domain.MemoryProposalStatusAccepted || loaded.MemoryID != accepted.MemoryID {
		t.Fatalf("historical proposal link = %#v, %v", loaded, err)
	}
	if _, _, err := instance.AcceptMemoryProposal(t.Context(), proposal.ID, nil); !errors.Is(err, ErrMemoryProposalAcceptedMemoryDeleted) {
		t.Fatalf("repeat accept after deletion = %v", err)
	}
	var count int
	if err := instance.db.QueryRow("SELECT COUNT(*) FROM bot_memories WHERE bot_id = ?", botID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("repeat acceptance recreated %d memories", count)
	}
}

func TestMemoryProposalSchemaRemovesLegacyAcceptedMemoryForeignKey(t *testing.T) {
	instance, botID, _, run, message := openMemoryProposalTestStore(t)
	if _, err := instance.db.Exec(`DROP TABLE memory_proposals;
	CREATE TABLE memory_proposals (
		id TEXT PRIMARY KEY,
		bot_id TEXT NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
		source_run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
		source_message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
		category TEXT NOT NULL CHECK(category IN ('fact', 'preference', 'instruction', 'project')),
		status TEXT NOT NULL CHECK(status IN ('pending', 'accepted', 'rejected')),
		content TEXT NOT NULL,
		normalized_content TEXT NOT NULL,
		priority INTEGER NOT NULL,
		expires_at TEXT NOT NULL DEFAULT '',
		memory_id TEXT REFERENCES bot_memories(id),
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE(bot_id, normalized_content)
	);`); err != nil {
		t.Fatal(err)
	}
	proposal, err := instance.CreateMemoryProposal(t.Context(), domain.MemoryProposalDraft{
		BotID: botID, SourceRunID: run.ID, SourceMessageID: message.ID,
		Category: domain.MemoryCategoryFact, Content: "Legacy linked memory", Priority: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, memory, err := instance.AcceptMemoryProposal(t.Context(), proposal.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.EnsureMemoryProposalsSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	hasFK, err := instance.memoryProposalsHasMemoryFK(t.Context())
	if err != nil || hasFK {
		t.Fatalf("legacy memory foreign key remains: %t, %v", hasFK, err)
	}
	if _, err := instance.DeleteBotMemory(t.Context(), botID, memory.ID); err != nil {
		t.Fatalf("delete accepted memory after migration: %v", err)
	}
	loaded, err := instance.GetMemoryProposal(t.Context(), proposal.ID)
	if err != nil || loaded.MemoryID != memory.ID {
		t.Fatalf("migration lost historical memory id = %#v, %v", loaded, err)
	}
}

func openMemoryProposalTestStore(t *testing.T) (*Store, string, domain.Conversation, domain.Run, domain.Message) {
	t.Helper()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := instance.EnsureMemoryProposalsSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	message, _, run, _, err := instance.CreateMessageWithAttachmentsAndRun(t.Context(), conversation.ID, conversation.BotID, "grok", "Remember this", "Remember this", nil)
	if err != nil {
		t.Fatal(err)
	}
	return instance, conversation.BotID, conversation, run, message
}
