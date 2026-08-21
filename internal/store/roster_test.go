package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestRosterMigrationIsAutomaticAdditiveAndIdempotent(t *testing.T) {
	ctx := t.Context()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	var before int
	if err := instance.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
WHERE type = 'table' AND name = 'agent_roster'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 1 {
		t.Fatalf("roster migration was not wired into Store.Open: tables = %d", before)
	}
	if _, err := instance.db.Exec("CREATE TABLE roster_migration_marker (value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.db.Exec("INSERT INTO roster_migration_marker(value) VALUES ('preserved')"); err != nil {
		t.Fatal(err)
	}
	if err := instance.MigrateRoster(ctx); err != nil {
		t.Fatalf("MigrateRoster first call: %v", err)
	}
	if err := instance.MigrateRoster(ctx); err != nil {
		t.Fatalf("MigrateRoster second call: %v", err)
	}
	var marker string
	if err := instance.db.QueryRow("SELECT value FROM roster_migration_marker").Scan(&marker); err != nil || marker != "preserved" {
		t.Fatalf("migration marker = %q, %v", marker, err)
	}
}

func TestAgentRosterDefaultsAndPatch(t *testing.T) {
	instance := openAgentStore(t)
	ctx := t.Context()
	created, err := instance.CreateAgent(ctx, domain.AgentDraft{Name: "Scout", Title: "Research teammate"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Pinned || created.Hidden || created.Unread {
		t.Fatalf("created roster flags = pinned %v hidden %v unread %v", created.Pinned, created.Hidden, created.Unread)
	}

	listed, err := instance.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Pinned || listed[0].Hidden || listed[0].Unread {
		t.Fatalf("default roster = %#v", listed)
	}

	pinned, unread := true, true
	updated, err := instance.PatchAgentRoster(ctx, created.Bot.ID, AgentRosterUpdate{Pinned: &pinned, Unread: &unread})
	if err != nil {
		t.Fatalf("PatchAgentRoster: %v", err)
	}
	if !updated.Pinned || updated.Hidden || !updated.Unread {
		t.Fatalf("patched roster = %#v", updated)
	}

	hidden, read := true, false
	hiddenUpdate, err := instance.PatchAgentRoster(ctx, created.Bot.ID, AgentRosterUpdate{Hidden: &hidden, Unread: &read})
	if err != nil {
		t.Fatalf("PatchAgentRoster hide/read: %v", err)
	}
	if !hiddenUpdate.Pinned || !hiddenUpdate.Hidden || hiddenUpdate.Unread {
		t.Fatalf("hide/read roster = %#v", hiddenUpdate)
	}

	var lastReadAt string
	if err := instance.db.QueryRowContext(ctx, "SELECT last_read_at FROM agent_roster WHERE bot_id = ?", created.Bot.ID).Scan(&lastReadAt); err != nil {
		t.Fatal(err)
	}
	if lastReadAt == "" {
		t.Fatal("marking read did not set last_read_at")
	}

	if err := instance.MarkAgentUnread(ctx, created.Bot.ID); err != nil {
		t.Fatalf("MarkAgentUnread: %v", err)
	}
	marked, err := instance.getAgent(ctx, created.Bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !marked.Unread || !marked.Pinned || !marked.Hidden {
		t.Fatalf("unread mark cleared other flags: %#v", marked)
	}

	if _, err := instance.PatchAgentRoster(ctx, created.Bot.ID, AgentRosterUpdate{}); err == nil || err.Error() != "at least one roster field is required" {
		t.Fatalf("empty roster patch error = %v", err)
	}
	if _, err := instance.PatchAgentRoster(ctx, "bot-missing", AgentRosterUpdate{Pinned: &pinned}); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("missing agent error = %v", err)
	}
}
