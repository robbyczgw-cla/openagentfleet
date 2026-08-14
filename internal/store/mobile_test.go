package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	_ "modernc.org/sqlite"
)

func TestMobileAuthVersionMigratesExistingCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE remote_devices (
  id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL,
  platform TEXT NOT NULL CHECK(platform IN ('ios', 'android')),
  scope_profile TEXT NOT NULL CHECK(scope_profile IN ('observer', 'controller', 'owner')),
  status TEXT NOT NULL CHECK(status IN ('active', 'revoked')),
  created_at TEXT NOT NULL,
  revoked_at TEXT NOT NULL DEFAULT '',
  last_used_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE remote_credentials (
  token_hash BLOB PRIMARY KEY CHECK(length(token_hash) = 32),
  device_id TEXT NOT NULL REFERENCES remote_devices(id) ON DELETE CASCADE,
  expires_at TEXT NOT NULL,
  revoked INTEGER NOT NULL DEFAULT 0 CHECK(revoked IN (0, 1))
);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	instance, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	var authVersionColumn int
	rows, err := instance.db.Query("PRAGMA table_info(remote_credentials)")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var ordinal, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&ordinal, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "auth_version" {
			authVersionColumn++
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if authVersionColumn != 1 {
		t.Fatalf("auth_version columns = %d, want 1", authVersionColumn)
	}

	grant, secret, err := instance.CreateRemotePairingGrant(t.Context(), domain.RemoteScopeController, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token := randomMobileTestToken(t)
	device, err := instance.ClaimRemotePairingGrant(t.Context(), grant.ID, secret, "Migration Phone", domain.RemotePlatformIOS, token, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	session, err := instance.AuthenticateMobileCredential(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	if session.AuthVersion != domain.RemoteAuthVersionBearer || session.Device.ID != device.ID {
		t.Fatalf("session = %#v", session)
	}
}

func TestMobileAuthVersionMigrationIsConcurrentSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE remote_devices (
  id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL,
  platform TEXT NOT NULL CHECK(platform IN ('ios', 'android')),
  scope_profile TEXT NOT NULL CHECK(scope_profile IN ('observer', 'controller', 'owner')),
  status TEXT NOT NULL CHECK(status IN ('active', 'revoked')),
  created_at TEXT NOT NULL,
  revoked_at TEXT NOT NULL DEFAULT '',
  last_used_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE remote_credentials (
  token_hash BLOB PRIMARY KEY CHECK(length(token_hash) = 32),
  device_id TEXT NOT NULL REFERENCES remote_devices(id) ON DELETE CASCADE,
  expires_at TEXT NOT NULL,
  revoked INTEGER NOT NULL DEFAULT 0 CHECK(revoked IN (0, 1))
);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	const openers = 16
	stores := make([]*Store, 0, openers)
	for range openers {
		db, err := sql.Open("sqlite", sqliteDSN(path))
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		if err := db.PingContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		stores = append(stores, &Store{db: db})
	}
	start := make(chan struct{})
	results := make(chan error, openers)
	for _, instance := range stores {
		go func(instance *Store) {
			<-start
			results <- instance.migrateRemoteAuthVersion()
		}(instance)
	}
	close(start)
	failures := make([]error, 0)
	for range openers {
		if err := <-results; err != nil {
			failures = append(failures, err)
		}
	}
	for _, instance := range stores {
		if err := instance.Close(); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) != 0 {
		t.Fatalf("concurrent legacy migrations failed: %v", failures)
	}

	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var authVersionColumns int
	rows, err := db.Query("PRAGMA table_info(remote_credentials)")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var ordinal, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&ordinal, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "auth_version" {
			authVersionColumns++
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if authVersionColumns != 1 {
		t.Fatalf("auth_version columns = %d, want 1", authVersionColumns)
	}
}

func TestCreateMobileMessageRunIsConcurrentAndIdempotent(t *testing.T) {
	instance := openMobileStoreTest(t)
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	device, _ := createMobileStoreDevice(t, instance, domain.RemoteScopeController)

	const callers = 12
	type result struct {
		response domain.MobileMessageResponse
		created  bool
		err      error
	}
	results := make(chan result, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response, _, _, created, err := instance.CreateMobileMessageRun(context.Background(), device.ID, "same-key", conversation.ID, "build the alpha")
			results <- result{response: response, created: created, err: err}
		}()
	}
	wait.Wait()
	close(results)
	createdCount := 0
	var messageID, runID string
	for item := range results {
		if item.err != nil {
			t.Fatal(item.err)
		}
		if item.created {
			createdCount++
		}
		if messageID == "" {
			messageID, runID = item.response.Message.ID, item.response.Run.ID
		}
		if item.response.Message.ID != messageID || item.response.Run.ID != runID {
			t.Fatalf("idempotent response changed: %#v", item.response)
		}
	}
	if createdCount != 1 {
		t.Fatalf("created count = %d, want 1", createdCount)
	}
	var messageCount, runCount, eventCount int
	if err := instance.db.QueryRow("SELECT COUNT(*) FROM messages WHERE id = ?", messageID).Scan(&messageCount); err != nil {
		t.Fatal(err)
	}
	if err := instance.db.QueryRow("SELECT COUNT(*) FROM runs WHERE id = ?", runID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err := instance.db.QueryRow("SELECT COUNT(*) FROM run_events WHERE run_id = ?", runID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if messageCount != 1 || runCount != 1 || eventCount != 1 {
		t.Fatalf("rows = message:%d run:%d event:%d", messageCount, runCount, eventCount)
	}
	if _, _, _, _, err := instance.CreateMobileMessageRun(t.Context(), device.ID, "same-key", conversation.ID, "different request"); !errors.Is(err, ErrMobileIdempotencyConflict) {
		t.Fatalf("different request error = %v", err)
	}
}

func TestMobileSnapshotCursorAndEventProjection(t *testing.T) {
	instance := openMobileStoreTest(t)
	conversation, err := instance.GetConversation(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	run, err := instance.CreateRun(t.Context(), conversation.ID, conversation.BotID, "grok", "private prompt")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := instance.AppendRunEvent(t.Context(), run.ID, "run.queued", `{"status":"queued"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.AppendRunEvent(t.Context(), run.ID, "session.opened", `{"native_session_id":"secret-native-id","workdir":"/private/work"}`); err != nil {
		t.Fatal(err)
	}

	snapshot, err := instance.MobileBootstrapSnapshot(t.Context(), conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.EventCursor < 2 || len(snapshot.Runs) != 1 || snapshot.Runs[0].ID != run.ID {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if valid, err := instance.ValidateMobileCursor(t.Context(), snapshot.EventCursor); err != nil || !valid {
		t.Fatalf("snapshot cursor valid = %v, err = %v", valid, err)
	}
	if valid, err := instance.ValidateMobileCursor(t.Context(), snapshot.EventCursor+100); err != nil || valid {
		t.Fatalf("future cursor valid = %v, err = %v", valid, err)
	}
	items, err := instance.ListMobileEventsAfter(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Type != "run.queued" || items[0].RunID != run.ID {
		t.Fatalf("mobile events = %#v; queued id = %s", items, queued.ID)
	}
}

func openMobileStoreTest(t *testing.T) *Store {
	t.Helper()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Seed(t.Context()); err != nil {
		t.Fatal(err)
	}
	return instance
}

func createMobileStoreDevice(t *testing.T, instance *Store, scope string) (domain.RemoteDevice, string) {
	t.Helper()
	grant, secret, err := instance.CreateRemotePairingGrant(t.Context(), scope, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token := randomMobileTestToken(t)
	device, err := instance.ClaimRemotePairingGrant(t.Context(), grant.ID, secret, "Test Phone", domain.RemotePlatformIOS, token, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return device, token
}

func randomMobileTestToken(t *testing.T) string {
	t.Helper()
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer)
}
