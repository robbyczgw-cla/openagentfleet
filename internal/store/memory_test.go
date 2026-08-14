package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	_ "modernc.org/sqlite"
)

func TestBotMemoryCRUDIsScopedAndReviewable(t *testing.T) {
	ctx := context.Background()
	instance := openMemoryTestStore(t)
	firstBot := memoryTestBot(t, instance)
	secondBot := insertMemoryTestBot(t, instance, "Second bot")

	created, err := instance.CreateBotMemory(ctx, firstBot, validMemoryDraft("The project uses Go."))
	if err != nil {
		t.Fatalf("CreateBotMemory: %v", err)
	}
	assertStableMemoryTimestamp(t, created.CreatedAt)
	if created.CreatedAt != created.UpdatedAt || created.Source != domain.MemorySourceUser {
		t.Fatalf("created memory = %#v", created)
	}

	loaded, err := instance.GetBotMemory(ctx, firstBot, created.ID)
	if err != nil || loaded != created {
		t.Fatalf("GetBotMemory = %#v, %v", loaded, err)
	}
	if _, err := instance.GetBotMemory(ctx, secondBot, created.ID); err == nil {
		t.Fatal("cross-bot GetBotMemory resolved a record")
	}

	updated, err := instance.UpdateBotMemory(ctx, firstBot, created.ID, domain.BotMemoryUpdate{
		Category: domain.MemoryCategoryInstruction,
		Status:   domain.MemoryStatusApproved,
		Content:  "Run targeted tests before a release.",
		Priority: 5,
	})
	if err != nil {
		t.Fatalf("UpdateBotMemory: %v", err)
	}
	if updated.CreatedAt != created.CreatedAt || updated.Source != created.Source || updated.UpdatedAt <= created.UpdatedAt {
		t.Fatalf("immutable provenance or timestamps changed incorrectly: before=%#v after=%#v", created, updated)
	}
	if _, err := instance.UpdateBotMemory(ctx, secondBot, created.ID, domain.BotMemoryUpdate{
		Category: updated.Category, Status: updated.Status, Content: updated.Content, Priority: updated.Priority,
	}); err == nil {
		t.Fatal("cross-bot UpdateBotMemory changed a record")
	}

	archived, err := instance.ArchiveBotMemory(ctx, firstBot, created.ID)
	if err != nil || archived.Status != domain.MemoryStatusArchived {
		t.Fatalf("ArchiveBotMemory = %#v, %v", archived, err)
	}
	active, err := instance.ListBotMemories(ctx, firstBot, false)
	if err != nil || len(active) != 0 {
		t.Fatalf("active memories after archive = %#v, %v", active, err)
	}
	reviewable, err := instance.ListBotMemories(ctx, firstBot, true)
	if err != nil || len(reviewable) != 1 || reviewable[0].ID != created.ID {
		t.Fatalf("reviewable memories after archive = %#v, %v", reviewable, err)
	}
	repeated, err := instance.ArchiveBotMemory(ctx, firstBot, created.ID)
	if err != nil || repeated.UpdatedAt != archived.UpdatedAt {
		t.Fatalf("idempotent archive = %#v, %v", repeated, err)
	}

	toDelete := createMemoryTestRecord(t, instance, firstBot, domain.MemoryCategoryFact, "permanently remove this", 2, "")
	deleted, err := instance.DeleteBotMemory(ctx, firstBot, toDelete.ID)
	if err != nil || deleted.ID != toDelete.ID {
		t.Fatalf("DeleteBotMemory = %#v, %v", deleted, err)
	}
	if _, err := instance.GetBotMemory(ctx, firstBot, toDelete.ID); !errors.Is(err, ErrBotMemoryNotFound) {
		t.Fatalf("deleted memory remained addressable: %v", err)
	}
	if _, err := instance.DeleteBotMemory(ctx, secondBot, created.ID); !errors.Is(err, ErrBotMemoryNotFound) {
		t.Fatalf("cross-bot DeleteBotMemory = %v", err)
	}
}

func TestBotMemoryValidationAndSecretProtection(t *testing.T) {
	ctx := context.Background()
	instance := openMemoryTestStore(t)
	botID := memoryTestBot(t, instance)
	valid := validMemoryDraft("Normal prose may mention that API keys must never be stored.")
	if _, err := instance.CreateBotMemory(ctx, botID, valid); err != nil {
		t.Fatalf("normal security prose was rejected: %v", err)
	}
	proposal := validMemoryDraft("The user prefers compact status updates.")
	proposal.Category = domain.MemoryCategoryPreference
	proposal.Source = domain.MemorySourceAgentProposal
	proposal.ExpiresAt = "2027-02-03T10:05:06+02:00"
	createdProposal, err := instance.CreateBotMemory(ctx, botID, proposal)
	if err != nil {
		t.Fatalf("valid agent proposal was rejected: %v", err)
	}
	if createdProposal.Source != domain.MemorySourceAgentProposal || createdProposal.ExpiresAt != "2027-02-03T08:05:06Z" {
		t.Fatalf("agent proposal normalization = %#v", createdProposal)
	}

	tests := []struct {
		name   string
		mutate func(*domain.BotMemoryDraft)
	}{
		{name: "category", mutate: func(value *domain.BotMemoryDraft) { value.Category = "unknown" }},
		{name: "status", mutate: func(value *domain.BotMemoryDraft) { value.Status = "pending" }},
		{name: "source", mutate: func(value *domain.BotMemoryDraft) { value.Source = "system" }},
		{name: "priority low", mutate: func(value *domain.BotMemoryDraft) { value.Priority = 0 }},
		{name: "priority high", mutate: func(value *domain.BotMemoryDraft) { value.Priority = 6 }},
		{name: "empty", mutate: func(value *domain.BotMemoryDraft) { value.Content = " \n\t " }},
		{name: "too large bytes", mutate: func(value *domain.BotMemoryDraft) {
			value.Content = strings.Repeat("é", domain.MemoryContentMaxBytes/2+1)
		}},
		{name: "control", mutate: func(value *domain.BotMemoryDraft) { value.Content = "hello\x00world" }},
		{name: "expiry", mutate: func(value *domain.BotMemoryDraft) { value.ExpiresAt = "tomorrow" }},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			value := validMemoryDraft("Safe content")
			testCase.mutate(&value)
			if _, err := instance.CreateBotMemory(ctx, botID, value); err == nil {
				t.Fatal("invalid memory was accepted")
			}
		})
	}

	unsafe := []string{
		"password is hunter2",
		"PIN: 1234",
		"OPENAI_API_KEY=sk-0123456789abcdefghijklmnop",
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz",
		"Use https://alice:correct-horse@example.com/path",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature123456",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nsecret bytes",
		"xai-0123456789abcdefghijklmnop",
	}
	for _, content := range unsafe {
		t.Run(content[:min(20, len(content))], func(t *testing.T) {
			if _, err := instance.CreateBotMemory(ctx, botID, validMemoryDraft(content)); err == nil {
				t.Fatal("secret-like memory was accepted")
			}
			var count int
			if err := instance.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bot_memories WHERE content = ?", strings.TrimSpace(content)).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("rejected secret-like content reached SQLite")
			}
		})
	}
}

func TestBotMemoryOrderingHasStableTieBreak(t *testing.T) {
	ctx := context.Background()
	instance := openMemoryTestStore(t)
	botID := memoryTestBot(t, instance)
	first := createMemoryTestRecord(t, instance, botID, domain.MemoryCategoryFact, "first tie", 4, "")
	second := createMemoryTestRecord(t, instance, botID, domain.MemoryCategoryFact, "second tie", 4, "")
	const timestamp = "2026-01-01T00:00:00Z"
	setMemoryTestTimestamp(t, instance, first.ID, timestamp)
	setMemoryTestTimestamp(t, instance, second.ID, timestamp)

	items, err := instance.ListBotMemories(ctx, botID, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{first.ID, second.ID}
	sort.Strings(want)
	if got := memoryIDs(items); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tie order = %v, want %v", got, want)
	}
	retrieved, err := instance.RetrieveBotMemories(ctx, botID, 2, domain.MemoryRetrievalMaxBytes)
	if err != nil || strings.Join(memoryIDs(retrieved), ",") != strings.Join(want, ",") {
		t.Fatalf("retrieval tie order = %v, %v", memoryIDs(retrieved), err)
	}
}

func TestBotMemoryExpiryOrderingAndRetrievalBudgets(t *testing.T) {
	ctx := context.Background()
	instance := openMemoryTestStore(t)
	botID := memoryTestBot(t, instance)
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)

	low := createMemoryTestRecord(t, instance, botID, domain.MemoryCategoryFact, "low", 1, "")
	oldHigh := createMemoryTestRecord(t, instance, botID, domain.MemoryCategoryFact, "older high", 5, "")
	newHigh := createMemoryTestRecord(t, instance, botID, domain.MemoryCategoryPreference, "newer high record that exceeds the narrow byte budget", 5, future)
	expired := createMemoryTestRecord(t, instance, botID, domain.MemoryCategoryFact, "expired", 5, past)
	archived := createMemoryTestRecord(t, instance, botID, domain.MemoryCategoryProject, "archived", 5, "")
	if _, err := instance.ArchiveBotMemory(ctx, botID, archived.ID); err != nil {
		t.Fatal(err)
	}

	setMemoryTestTimestamp(t, instance, oldHigh.ID, "2026-01-01T00:00:00Z")
	setMemoryTestTimestamp(t, instance, newHigh.ID, "2026-01-02T00:00:00Z")
	setMemoryTestTimestamp(t, instance, low.ID, "2026-01-03T00:00:00Z")

	active, err := instance.ListBotMemories(ctx, botID, false)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{newHigh.ID, oldHigh.ID, low.ID}
	if got := memoryIDs(active); strings.Join(got, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("active order = %v, want %v", got, wantIDs)
	}
	for _, item := range active {
		if item.ID == expired.ID || item.ID == archived.ID {
			t.Fatalf("inactive memory was listed: %#v", item)
		}
	}

	byCount, err := instance.RetrieveBotMemories(ctx, botID, 2, domain.MemoryRetrievalMaxBytes)
	if err != nil || len(byCount) != 2 || byCount[0].ID != newHigh.ID || byCount[1].ID != oldHigh.ID {
		t.Fatalf("count retrieval = %#v, %v", byCount, err)
	}
	byBytes, err := instance.RetrieveBotMemories(ctx, botID, 3, len(oldHigh.Content)+len(low.Content))
	if err != nil {
		t.Fatal(err)
	}
	if got := memoryIDs(byBytes); strings.Join(got, ",") != strings.Join([]string{oldHigh.ID, low.ID}, ",") {
		t.Fatalf("byte retrieval = %v, want later fitting records", got)
	}
	for _, limits := range [][2]int{{0, 1}, {1, 0}, {domain.MemoryRetrievalMaxCount + 1, 1}, {1, domain.MemoryRetrievalMaxBytes + 1}} {
		if _, err := instance.RetrieveBotMemories(ctx, botID, limits[0], limits[1]); err == nil {
			t.Fatalf("invalid retrieval limits accepted: %v", limits)
		}
	}
}

func TestBotMemoryUpdateRejectsSecretAndPreservesStoredValue(t *testing.T) {
	ctx := context.Background()
	instance := openMemoryTestStore(t)
	botID := memoryTestBot(t, instance)
	created, err := instance.CreateBotMemory(ctx, botID, validMemoryDraft("Safe original"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = instance.UpdateBotMemory(ctx, botID, created.ID, domain.BotMemoryUpdate{
		Category: created.Category, Status: created.Status, Content: "client_secret=abcd1234xyz", Priority: created.Priority,
	})
	if err == nil {
		t.Fatal("secret-like update was accepted")
	}
	loaded, err := instance.GetBotMemory(ctx, botID, created.ID)
	if err != nil || loaded.Content != created.Content || loaded.UpdatedAt != created.UpdatedAt {
		t.Fatalf("rejected update mutated memory: %#v, %v", loaded, err)
	}
}

func TestBotMemorySchemaMigratesExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE bots (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, title TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'idle',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE existing_marker (value TEXT NOT NULL);
INSERT INTO bots VALUES ('bot-legacy', 'Legacy', '', '', 'idle', '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z');
INSERT INTO existing_marker VALUES ('preserved');`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	instance, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy database: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	var marker string
	if err := instance.db.QueryRow("SELECT value FROM existing_marker").Scan(&marker); err != nil || marker != "preserved" {
		t.Fatalf("legacy marker = %q, %v", marker, err)
	}
	if _, err := instance.CreateBotMemory(context.Background(), "bot-legacy", validMemoryDraft("Migrated safely")); err != nil {
		t.Fatalf("memory table unavailable after migration: %v", err)
	}
}

func openMemoryTestStore(t *testing.T) *Store {
	t.Helper()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	return instance
}

func memoryTestBot(t *testing.T, instance *Store) string {
	t.Helper()
	bots, err := instance.ListBots(context.Background())
	if err != nil || len(bots) != 1 {
		t.Fatalf("ListBots = %#v, %v", bots, err)
	}
	return bots[0].ID
}

func insertMemoryTestBot(t *testing.T, instance *Store, name string) string {
	t.Helper()
	botID := "bot-" + strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := instance.db.Exec("INSERT INTO bots (id, name, title, description, status, created_at, updated_at) VALUES (?, ?, '', '', 'idle', ?, ?)", botID, name, timestamp, timestamp)
	if err != nil {
		t.Fatal(err)
	}
	return botID
}

func validMemoryDraft(content string) domain.BotMemoryDraft {
	return domain.BotMemoryDraft{
		Category: domain.MemoryCategoryFact,
		Status:   domain.MemoryStatusApproved,
		Source:   domain.MemorySourceUser,
		Content:  content,
		Priority: 3,
	}
}

func createMemoryTestRecord(t *testing.T, instance *Store, botID string, category domain.MemoryCategory, content string, priority int, expiry string) domain.BotMemory {
	t.Helper()
	draft := validMemoryDraft(content)
	draft.Category = category
	draft.Priority = priority
	draft.ExpiresAt = expiry
	item, err := instance.CreateBotMemory(context.Background(), botID, draft)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func setMemoryTestTimestamp(t *testing.T, instance *Store, memoryID, timestamp string) {
	t.Helper()
	if _, err := instance.db.Exec("UPDATE bot_memories SET created_at = ?, updated_at = ? WHERE id = ?", timestamp, timestamp, memoryID); err != nil {
		t.Fatal(err)
	}
}

func memoryIDs(items []domain.BotMemory) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.ID)
	}
	return result
}

func assertStableMemoryTimestamp(t *testing.T, value string) {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("timestamp %q is not RFC3339: %v", value, err)
	}
	if parsed.Location() != time.UTC {
		t.Fatalf("timestamp %q is not UTC", value)
	}
}
