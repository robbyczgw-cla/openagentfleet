package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestOpenRestrictsExistingDataDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory modes are not available on Windows")
	}
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o755); err != nil {
		t.Fatalf("make test directory permissive: %v", err)
	}
	instance, err := Open(filepath.Join(dataDir, "botd.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("stat data directory: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("data directory mode = %#o, want 0700", got)
	}
}

func TestOpenConfiguresEveryPooledConnection(t *testing.T) {
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	instance.db.SetMaxOpenConns(2)

	connections := make([]*sql.Conn, 0, 2)
	for range 2 {
		conn, err := instance.db.Conn(t.Context())
		if err != nil {
			t.Fatalf("acquire dedicated connection: %v", err)
		}
		connections = append(connections, conn)
		t.Cleanup(func() { _ = conn.Close() })
	}
	if got := instance.db.Stats().OpenConnections; got != 2 {
		t.Fatalf("open connections = %d, want 2", got)
	}

	for index, conn := range connections {
		var foreignKeys int
		if err := conn.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatalf("connection %d foreign_keys: %v", index, err)
		}
		if foreignKeys != 1 {
			t.Fatalf("connection %d foreign_keys = %d, want 1", index, foreignKeys)
		}
		var busyTimeout int
		if err := conn.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatalf("connection %d busy_timeout: %v", index, err)
		}
		if busyTimeout != 5000 {
			t.Fatalf("connection %d busy_timeout = %d, want 5000", index, busyTimeout)
		}
		if _, err := conn.ExecContext(t.Context(), `INSERT INTO conversations
			(id, bot_id, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			fmt.Sprintf("invalid-conversation-%d", index), "missing-bot", "Invalid", now(), now()); err == nil {
			t.Fatalf("connection %d accepted a row with a missing foreign key", index)
		}
	}
}

func TestSeedAndPersistConversationWork(t *testing.T) {
	ctx := context.Background()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	bots, err := instance.ListBots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(bots) != 1 || bots[0].Name != "OpenAgentFleet" {
		t.Fatalf("unexpected bots: %#v", bots)
	}
	conversation, err := instance.GetConversation(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	message, err := instance.CreateMessage(ctx, conversation.ID, "user", "test prompt")
	if err != nil {
		t.Fatal(err)
	}
	run, event, err := instance.CreateRunWithQueuedEvent(ctx, conversation.ID, conversation.BotID, "grok", message.Content)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := instance.ListRuns(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("unexpected runs: %#v", runs)
	}
	runEvents, err := instance.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runEvents) != 1 || runEvents[0].ID != event.ID {
		t.Fatalf("unexpected run events: %#v", runEvents)
	}
	streamEvents, err := instance.ListConversationEvents(ctx, conversation.ID, "")
	if err != nil || len(streamEvents) != 1 || streamEvents[0].ID != event.ID || streamEvents[0].ConversationID != conversation.ID {
		t.Fatalf("unexpected stream events: %#v, %v", streamEvents, err)
	}
	if after, err := instance.ListConversationEvents(ctx, conversation.ID, event.ID); err != nil || len(after) != 0 {
		t.Fatalf("unexpected cursor events: %#v, %v", after, err)
	}
	approval, err := instance.CreateApproval(ctx, run.ID, "grok", "terminal", `{"command":"pwd"}`)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := instance.ListApprovals(ctx, "pending")
	if err != nil || len(pending) != 1 || pending[0].ID != approval.ID {
		t.Fatalf("unexpected approvals: %#v, %v", pending, err)
	}
	if err := instance.ResolveApproval(ctx, approval.ID, "approved", "allow-once"); err != nil {
		t.Fatal(err)
	}
	resolved, err := instance.GetApproval(ctx, approval.ID)
	if err != nil || resolved.Status != "approved" || resolved.SelectedOptionID != "allow-once" {
		t.Fatalf("resolved approval = %#v, err = %v", resolved, err)
	}
	session, err := instance.UpsertHarnessSession(ctx, conversation.ID, "grok", "native-session-1", "/workspace", "Atlas session", "ready")
	if err != nil {
		t.Fatal(err)
	}
	loadedSession, err := instance.GetHarnessSession(ctx, conversation.ID, "grok")
	if err != nil || loadedSession.ID != session.ID || loadedSession.NativeSessionID != "native-session-1" {
		t.Fatalf("loaded session = %#v, err = %v", loadedSession, err)
	}
	if err := instance.DeleteHarnessSession(ctx, "grok", "native-session-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.GetHarnessSession(ctx, conversation.ID, "grok"); err == nil {
		t.Fatal("deleted session still resolved")
	}
	if _, err := instance.UpsertHarnessSession(ctx, conversation.ID, "grok", "native-session-1", "/workspace", "Atlas session", "ready"); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.UpdateRunWithLifecycleEvent(ctx, run.ID, "blocked", "disabled", "run.blocked", `{"reason":"disabled"}`); err != nil {
		t.Fatal(err)
	}
	messages, err := instance.ListMessages(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Content != "test prompt" {
		t.Fatalf("unexpected messages: %#v", messages)
	}
	created, err := instance.CreateConversation(ctx, conversation.BotID, "Research thread")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.CreateMessage(ctx, created.ID, "user", "find this research marker"); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.RenameConversation(ctx, created.ID, "Renamed research thread"); err != nil {
		t.Fatal(err)
	}
	hits, err := instance.Search(ctx, "research marker", 10)
	if err != nil || len(hits) != 1 || hits[0].Kind != "message" || hits[0].ConversationID != created.ID {
		t.Fatalf("search hits = %#v, err = %v", hits, err)
	}
}

func TestRecoverInterruptedRunsStopsRunsAndCancelsApprovalsIdempotently(t *testing.T) {
	ctx := t.Context()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := instance.CreateRunWithQueuedEvent(ctx, conversation.ID, conversation.BotID, "grok", "recover me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.UpdateRunWithLifecycleEvent(ctx, run.ID, "waiting_for_approval", "", "run.waiting_for_approval", `{"status":"waiting_for_approval"}`); err != nil {
		t.Fatal(err)
	}
	approval, err := instance.CreateApproval(ctx, run.ID, "grok", "terminal", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if recovered, err := instance.RecoverInterruptedRuns(ctx); err != nil || recovered != 1 {
		t.Fatalf("first recovery = %d, %v", recovered, err)
	}
	storedRun, err := instance.GetRun(ctx, run.ID)
	if err != nil || storedRun.Status != "stopped" {
		t.Fatalf("recovered run = %#v, %v", storedRun, err)
	}
	storedApproval, err := instance.GetApproval(ctx, approval.ID)
	if err != nil || storedApproval.Status != "cancelled" {
		t.Fatalf("recovered approval = %#v, %v", storedApproval, err)
	}
	events, err := instance.ListRunEvents(ctx, run.ID)
	if err != nil || len(events) != 4 {
		t.Fatalf("recovery events = %#v, %v", events, err)
	}
	lateApproval, err := instance.CreateApproval(ctx, run.ID, "grok", "late terminal approval", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if recovered, err := instance.RecoverInterruptedRuns(ctx); err != nil || recovered != 0 {
		t.Fatalf("terminal approval recovery = %d, %v", recovered, err)
	}
	lateStored, err := instance.GetApproval(ctx, lateApproval.ID)
	if err != nil || lateStored.Status != "cancelled" {
		t.Fatalf("terminal approval = %#v, %v", lateStored, err)
	}
	events, err = instance.ListRunEvents(ctx, run.ID)
	if err != nil || len(events) != 5 {
		t.Fatalf("terminal approval recovery events = %#v, %v", events, err)
	}
	if recovered, err := instance.RecoverInterruptedRuns(ctx); err != nil || recovered != 0 {
		t.Fatalf("second recovery = %d, %v", recovered, err)
	}
}

func TestCreateRunWithQueuedEventRollsBackRunWhenEventInsertFails(t *testing.T) {
	ctx := t.Context()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.db.ExecContext(ctx, `CREATE TRIGGER fail_queued_event
		BEFORE INSERT ON run_events WHEN NEW.type = 'run.queued'
		BEGIN SELECT RAISE(FAIL, 'forced queued event failure'); END`); err != nil {
		t.Fatal(err)
	}

	if _, _, err := instance.CreateRunWithQueuedEvent(ctx, conversation.ID, conversation.BotID, "grok", "atomic prompt"); err == nil {
		t.Fatal("CreateRunWithQueuedEvent succeeded despite forced event failure")
	}
	runs, err := instance.ListRuns(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("run insert survived rolled-back queued event: %#v", runs)
	}
}

func TestUpdateRunWithLifecycleEventRollsBackStateWhenEventInsertFails(t *testing.T) {
	ctx := t.Context()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	run, queuedEvent, err := instance.CreateRunWithQueuedEvent(ctx, conversation.ID, conversation.BotID, "grok", "atomic prompt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.db.ExecContext(ctx, `CREATE TRIGGER fail_started_event
		BEFORE INSERT ON run_events WHEN NEW.type = 'run.started'
		BEGIN SELECT RAISE(FAIL, 'forced started event failure'); END`); err != nil {
		t.Fatal(err)
	}

	if _, err := instance.UpdateRunWithLifecycleEvent(ctx, run.ID, "running", "", "run.started", `{"status":"running"}`); err == nil {
		t.Fatal("UpdateRunWithLifecycleEvent succeeded despite forced event failure")
	}
	stored, err := instance.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "queued" || stored.Error != "" || stored.UpdatedAt != run.UpdatedAt {
		t.Fatalf("run state survived rolled-back event: %#v", stored)
	}
	events, err := instance.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != queuedEvent.ID {
		t.Fatalf("events after rollback = %#v, want only queued event", events)
	}
}

func TestLifecycleCannotOverwriteTerminalRun(t *testing.T) {
	ctx := context.Background()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := instance.CreateRunWithQueuedEvent(ctx, conversation.ID, conversation.BotID, "grok", "stop me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.UpdateRunWithLifecycleEvent(ctx, run.ID, "running", "", "run.started", `{"status":"running"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.UpdateRunWithLifecycleEvent(ctx, run.ID, "stopped", "", "run.stopped", `{"status":"stopped"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.UpdateRunWithLifecycleEvent(ctx, run.ID, "completed", "", "run.completed", `{"status":"completed"}`); err == nil {
		t.Fatal("late completion overwrote terminal stopped run")
	}
	stored, err := instance.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "stopped" {
		t.Fatalf("terminal run status = %q, want stopped", stored.Status)
	}
}

func TestCreateMessageWithAttachmentsClaimsPendingUpload(t *testing.T) {
	ctx := context.Background()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Seed(ctx); err != nil {
		t.Fatal(err)
	}

	conversation, err := instance.GetConversation(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := instance.CreateAttachment(ctx, domain.Attachment{
		ConversationID: conversation.ID,
		Name:           "brief.txt",
		MediaType:      "text/plain",
		Size:           12,
		StoragePath:    filepath.Join(t.TempDir(), "brief.txt"),
	})
	if err != nil {
		t.Fatalf("create pending attachment: %v", err)
	}
	if attachment.MessageID != "" {
		t.Fatalf("pending attachment message ID = %q, want empty", attachment.MessageID)
	}

	message, claimed, err := instance.CreateMessageWithAttachments(ctx, conversation.ID, "user", "Please review the file", []string{attachment.ID})
	if err != nil {
		t.Fatalf("claim pending attachment: %v", err)
	}
	if message.ConversationID != conversation.ID || message.Content != "Please review the file" {
		t.Fatalf("message = %#v", message)
	}
	if len(claimed) != 1 || claimed[0].ID != attachment.ID || claimed[0].MessageID != message.ID {
		t.Fatalf("claimed attachments = %#v", claimed)
	}

	persisted, err := instance.GetAttachment(ctx, attachment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.MessageID != message.ID {
		t.Fatalf("persisted attachment message ID = %q, want %q", persisted.MessageID, message.ID)
	}
	if _, _, err := instance.CreateMessageWithAttachments(ctx, conversation.ID, "user", "reuse", []string{attachment.ID}); err == nil {
		t.Fatal("reused sent attachment without an error")
	}
	messages, err := instance.ListMessages(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("message count after rejected reuse = %d, want 1", len(messages))
	}
}

func TestCreateMessageWithAttachmentsAndRunRollsBackAttachmentClaim(t *testing.T) {
	ctx := context.Background()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := instance.CreateAttachment(ctx, domain.Attachment{
		ConversationID: conversation.ID,
		Name:           "rollback.txt",
		MediaType:      "text/plain",
		Size:           1,
		StoragePath:    filepath.Join(t.TempDir(), "rollback.txt"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.db.Exec(`CREATE TRIGGER fail_atomic_run BEFORE INSERT ON runs BEGIN SELECT RAISE(FAIL, 'forced run insert failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, err = instance.CreateMessageWithAttachmentsAndRun(ctx, conversation.ID, conversation.BotID, "pi", "Review this", "Review this\nattachment", []string{attachment.ID})
	if err == nil {
		t.Fatal("atomic message/run creation succeeded despite forced run failure")
	}
	stored, err := instance.GetAttachment(ctx, attachment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MessageID != "" {
		t.Fatalf("attachment was claimed after rollback: %#v", stored)
	}
	messages, err := instance.ListMessages(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("messages after atomic rollback = %#v", messages)
	}
}

func TestDeletePendingAttachmentsBeforeKeepsClaimedAndFreshFiles(t *testing.T) {
	ctx := context.Background()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	if err := instance.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	conversation, err := instance.GetConversation(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	old, err := instance.CreateAttachment(ctx, domain.Attachment{
		ConversationID: conversation.ID,
		Name:           "old.txt",
		MediaType:      "text/plain",
		Size:           1,
		StoragePath:    filepath.Join(t.TempDir(), "old.txt"),
		CreatedAt:      time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := instance.CreateAttachment(ctx, domain.Attachment{
		ConversationID: conversation.ID,
		Name:           "fresh.txt",
		MediaType:      "text/plain",
		Size:           1,
		StoragePath:    filepath.Join(t.TempDir(), "fresh.txt"),
	})
	if err != nil {
		t.Fatal(err)
	}
	claimedMessage, err := instance.CreateMessage(ctx, conversation.ID, "user", "claim")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.db.Exec("UPDATE attachments SET message_id = ? WHERE id = ?", claimedMessage.ID, fresh.ID); err != nil {
		t.Fatal(err)
	}
	removed, err := instance.DeletePendingAttachmentsBefore(ctx, time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0].ID != old.ID {
		t.Fatalf("removed attachments = %#v, want only old pending upload", removed)
	}
	if _, err := instance.GetAttachment(ctx, old.ID); err == nil {
		t.Fatal("old pending attachment remained")
	}
	if _, err := instance.GetAttachment(ctx, fresh.ID); err != nil {
		t.Fatalf("claimed/fresh attachment was removed: %v", err)
	}
}
