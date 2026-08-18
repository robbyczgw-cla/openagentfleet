package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
	_ "modernc.org/sqlite"
)

var ErrRunTerminal = errors.New("run is terminal")

type Store struct {
	db      *sql.DB
	agentMu sync.Mutex
}

func Open(path string) (*Store, error) {
	if err := ensurePrivateDataDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	s := &Store{db: db}
	if err := s.configure(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.MigrateRoutines(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func sqliteDSN(path string) string {
	parameters := url.Values{}
	parameters.Add("_pragma", "busy_timeout(5000)")
	parameters.Add("_pragma", "foreign_keys(1)")
	if path == ":memory:" {
		return path + "?" + parameters.Encode()
	}
	uriPath := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	return (&url.URL{Scheme: "file", Path: uriPath, RawQuery: parameters.Encode()}).String()
}

// ensurePrivateDataDirectory fixes an existing directory as well as creating a
// new one. botd stores mobile credentials and its native secret-handoff socket
// beneath this directory, so a default 0755 app-data directory is unsafe.
func ensurePrivateDataDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("restrict data directory permissions: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("restrict data directory permissions: unsafe data directory")
	}
	return nil
}

func (s *Store) configure() error {
	const statement = "PRAGMA journal_mode = WAL"
	if _, err := s.db.Exec(statement); err != nil {
		return fmt.Errorf("configure sqlite: %s: %w", statement, err)
	}
	return nil
}

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS bots (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'idle',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS agent_metadata (
  bot_id TEXT PRIMARY KEY REFERENCES bots(id) ON DELETE CASCADE,
  document TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS conversations (
  id TEXT PRIMARY KEY,
  bot_id TEXT NOT NULL REFERENCES bots(id),
  title TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
  id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES conversations(id),
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT '',
  author_bot_id TEXT NOT NULL DEFAULT '',
  mentions TEXT NOT NULL DEFAULT '',
  handoff_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS messages_conversation_idx ON messages(conversation_id, created_at);
CREATE TABLE IF NOT EXISTS attachments (
  id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES conversations(id),
  message_id TEXT REFERENCES messages(id),
  name TEXT NOT NULL,
  media_type TEXT NOT NULL,
  size INTEGER NOT NULL,
  storage_path TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS attachments_conversation_idx ON attachments(conversation_id, created_at);
CREATE INDEX IF NOT EXISTS attachments_message_idx ON attachments(message_id, created_at);
CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES conversations(id),
  bot_id TEXT NOT NULL REFERENCES bots(id),
  provider TEXT NOT NULL,
  status TEXT NOT NULL,
  prompt TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS runs_conversation_idx ON runs(conversation_id, created_at);
CREATE TABLE IF NOT EXISTS run_events (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES runs(id),
  type TEXT NOT NULL,
  data TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS run_events_run_idx ON run_events(run_id, created_at);
CREATE TABLE IF NOT EXISTS approval_requests (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES runs(id),
  provider TEXT NOT NULL,
  action TEXT NOT NULL,
  payload TEXT NOT NULL,
  status TEXT NOT NULL,
  selected_option_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  resolved_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS approval_requests_status_idx ON approval_requests(status, created_at);
CREATE TABLE IF NOT EXISTS harness_sessions (
  id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES conversations(id),
  provider TEXT NOT NULL,
  native_session_id TEXT NOT NULL,
  workdir TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS harness_sessions_native_idx ON harness_sessions(provider, native_session_id);
CREATE INDEX IF NOT EXISTS harness_sessions_conversation_idx ON harness_sessions(conversation_id, updated_at);
CREATE TABLE IF NOT EXISTS capabilities (
  name TEXT PRIMARY KEY,
  command TEXT NOT NULL,
  available INTEGER NOT NULL,
  version TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '',
  checked_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS remote_host_identity (
  singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
  host_id TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS remote_devices (
  id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL,
  platform TEXT NOT NULL CHECK(platform IN ('ios', 'android')),
  scope_profile TEXT NOT NULL CHECK(scope_profile IN ('observer', 'controller', 'owner')),
  status TEXT NOT NULL CHECK(status IN ('active', 'revoked')),
  created_at TEXT NOT NULL,
  revoked_at TEXT NOT NULL DEFAULT '',
  last_used_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS remote_credentials (
  token_hash BLOB PRIMARY KEY CHECK(length(token_hash) = 32),
  device_id TEXT NOT NULL REFERENCES remote_devices(id) ON DELETE CASCADE,
  auth_version INTEGER NOT NULL DEFAULT 1 CHECK(auth_version = 1),
  expires_at TEXT NOT NULL,
  revoked INTEGER NOT NULL DEFAULT 0 CHECK(revoked IN (0, 1))
);
CREATE INDEX IF NOT EXISTS remote_credentials_device_idx ON remote_credentials(device_id);
CREATE TABLE IF NOT EXISTS remote_pairing_grants (
  id TEXT PRIMARY KEY,
  secret_hash BLOB NOT NULL CHECK(length(secret_hash) = 32),
  scope_profile TEXT NOT NULL CHECK(scope_profile IN ('observer', 'controller', 'owner')),
  status TEXT NOT NULL CHECK(status IN ('pending', 'claimed', 'locked', 'expired')),
  failed_attempts INTEGER NOT NULL DEFAULT 0 CHECK(failed_attempts BETWEEN 0 AND 5),
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  claimed_at TEXT NOT NULL DEFAULT '',
  claimed_device_id TEXT REFERENCES remote_devices(id)
);
CREATE INDEX IF NOT EXISTS remote_pairing_grants_status_expiry_idx ON remote_pairing_grants(status, expires_at);
CREATE TABLE IF NOT EXISTS mobile_message_idempotency (
  device_id TEXT NOT NULL REFERENCES remote_devices(id) ON DELETE CASCADE,
  key_hash BLOB NOT NULL CHECK(length(key_hash) = 32),
  request_hash BLOB NOT NULL CHECK(length(request_hash) = 32),
  response_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(device_id, key_hash)
);
CREATE TABLE IF NOT EXISTS bot_memories (
  id TEXT PRIMARY KEY,
  bot_id TEXT NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  category TEXT NOT NULL CHECK(category IN ('fact', 'preference', 'instruction', 'project')),
  status TEXT NOT NULL CHECK(status IN ('approved', 'archived')),
  source TEXT NOT NULL CHECK(source IN ('user', 'agent_proposal')),
  content TEXT NOT NULL CHECK(length(CAST(content AS BLOB)) BETWEEN 1 AND 4096),
  priority INTEGER NOT NULL CHECK(priority BETWEEN 1 AND 5),
  expires_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS bot_memories_bot_order_idx
  ON bot_memories(bot_id, status, priority DESC, updated_at DESC, created_at DESC, id);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate sqlite: %w", err)
	}
	defaultMetadata, err := agentMetadataDocument(domain.DefaultAgentMetadata())
	if err != nil {
		return fmt.Errorf("encode default agent metadata: %w", err)
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO agent_metadata (bot_id, document, updated_at)
		SELECT id, ?, updated_at FROM bots`, defaultMetadata); err != nil {
		return fmt.Errorf("backfill agent metadata: %w", err)
	}
	if err := s.migrateRemoteAuthVersion(); err != nil {
		return err
	}
	if err := s.migrateLegacyAttachmentSchema(); err != nil {
		return err
	}
	return s.migrateBotParitySchema()
}

func (s *Store) migrateRemoteAuthVersion() error {
	ctx := context.Background()
	found, err := hasRemoteAuthVersion(ctx, s.db)
	if err != nil {
		return err
	}
	if found {
		return nil
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire remote credential migration connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin remote credential migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	found, err = hasRemoteAuthVersion(ctx, conn)
	if err != nil {
		return err
	}
	if !found {
		if _, err := conn.ExecContext(ctx, "ALTER TABLE remote_credentials ADD COLUMN auth_version INTEGER NOT NULL DEFAULT 1 CHECK(auth_version = 1)"); err != nil {
			return fmt.Errorf("add remote credential auth version: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit remote credential migration: %w", err)
	}
	committed = true
	return nil
}

type sqliteQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func hasRemoteAuthVersion(ctx context.Context, queryer sqliteQueryer) (bool, error) {
	rows, err := queryer.QueryContext(ctx, "PRAGMA table_info(remote_credentials)")
	if err != nil {
		return false, fmt.Errorf("inspect remote credential schema: %w", err)
	}
	found := false
	for rows.Next() {
		var (
			ordinal    int
			name       string
			dataType   string
			notNull    int
			defaultVal any
			primaryKey int
		)
		if err := rows.Scan(&ordinal, &name, &dataType, &notNull, &defaultVal, &primaryKey); err != nil {
			_ = rows.Close()
			return false, fmt.Errorf("scan remote credential schema: %w", err)
		}
		if name == "auth_version" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, fmt.Errorf("iterate remote credential schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close remote credential schema: %w", err)
	}
	return found, nil
}

// migrateLegacyAttachmentSchema fixes the first attachment schema, which used
// an empty string as the pending-message marker. That empty string violates the
// messages foreign key. Pending attachments use NULL instead; the public
// domain model still exposes an empty MessageID for the client.
func (s *Store) migrateLegacyAttachmentSchema() error {
	rows, err := s.db.Query("PRAGMA table_info(attachments)")
	if err != nil {
		return fmt.Errorf("inspect attachment schema: %w", err)
	}
	defer rows.Close()
	legacy := false
	for rows.Next() {
		var (
			ordinal    int
			name       string
			dataType   string
			notNull    int
			defaultVal any
			primaryKey int
		)
		if err := rows.Scan(&ordinal, &name, &dataType, &notNull, &defaultVal, &primaryKey); err != nil {
			return fmt.Errorf("scan attachment schema: %w", err)
		}
		if name == "message_id" && notNull != 0 {
			legacy = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate attachment schema: %w", err)
	}
	if !legacy {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin attachment migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`CREATE TABLE attachments_replacement (
  id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES conversations(id),
  message_id TEXT REFERENCES messages(id),
  name TEXT NOT NULL,
  media_type TEXT NOT NULL,
  size INTEGER NOT NULL,
  storage_path TEXT NOT NULL,
  created_at TEXT NOT NULL
)`); err != nil {
		return fmt.Errorf("create replacement attachment table: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO attachments_replacement (id, conversation_id, message_id, name, media_type, size, storage_path, created_at)
SELECT id, conversation_id, NULLIF(message_id, ''), name, media_type, size, storage_path, created_at FROM attachments`); err != nil {
		return fmt.Errorf("copy attachments to replacement table: %w", err)
	}
	if _, err := tx.Exec("DROP TABLE attachments"); err != nil {
		return fmt.Errorf("drop legacy attachment table: %w", err)
	}
	if _, err := tx.Exec("ALTER TABLE attachments_replacement RENAME TO attachments"); err != nil {
		return fmt.Errorf("rename replacement attachment table: %w", err)
	}
	for _, statement := range []string{
		"CREATE INDEX attachments_conversation_idx ON attachments(conversation_id, created_at)",
		"CREATE INDEX attachments_message_idx ON attachments(message_id, created_at)",
	} {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("recreate attachment index: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit attachment migration: %w", err)
	}
	return nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func (s *Store) Seed(ctx context.Context) error {
	const defaultTitle = "Personal operations teammate"
	const defaultDescription = "Your local background teammate for research, files, browser work, and controlled automation."
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bots").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		// Keep user-created bots intact, but migrate only the original generated
		// default from earlier product names.
		_, err := s.db.ExecContext(ctx, "UPDATE bots SET name = ?, updated_at = ? WHERE name IN (?, ?) AND title = ? AND description = ?", "OpenAgentFleet", now(), "Atlas", "OpenAgentFleet", defaultTitle, defaultDescription)
		return err
	}
	timestamp := now()
	botID := id.New("bot")
	conversationID := id.New("conv")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "INSERT INTO bots (id, name, title, description, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)", botID, "OpenAgentFleet", defaultTitle, defaultDescription, "idle", timestamp, timestamp); err != nil {
		return fmt.Errorf("seed bot: %w", err)
	}
	defaultMetadata, err := agentMetadataDocument(domain.DefaultAgentMetadata())
	if err != nil {
		return fmt.Errorf("seed agent metadata document: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO agent_metadata (bot_id, document, updated_at) VALUES (?, ?, ?)", botID, defaultMetadata, timestamp); err != nil {
		return fmt.Errorf("seed agent metadata: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO conversations (id, bot_id, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?)", conversationID, botID, "New conversation", timestamp, timestamp); err != nil {
		return fmt.Errorf("seed conversation: %w", err)
	}
	return tx.Commit()
}

func (s *Store) ListBots(ctx context.Context) ([]domain.Bot, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, name, title, description, status, created_at, updated_at FROM bots ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.Bot, 0)
	for rows.Next() {
		var item domain.Bot
		if err := rows.Scan(&item.ID, &item.Name, &item.Title, &item.Description, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) GetConversation(ctx context.Context, conversationID string) (domain.Conversation, error) {
	query := "SELECT id, bot_id, title, created_at, updated_at FROM conversations WHERE id = ?"
	args := []any{conversationID}
	if conversationID == "" {
		query = "SELECT id, bot_id, title, created_at, updated_at FROM conversations ORDER BY created_at LIMIT 1"
		args = nil
	}
	var result domain.Conversation
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&result.ID, &result.BotID, &result.Title, &result.CreatedAt, &result.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return result, fmt.Errorf("conversation not found")
	}
	return result, err
}

func (s *Store) ListConversations(ctx context.Context, botID string) ([]domain.Conversation, error) {
	query := "SELECT id, bot_id, title, created_at, updated_at FROM conversations ORDER BY updated_at DESC, id DESC"
	args := []any{}
	if botID != "" {
		query = "SELECT id, bot_id, title, created_at, updated_at FROM conversations WHERE bot_id = ? ORDER BY updated_at DESC, id DESC"
		args = append(args, botID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.Conversation, 0)
	for rows.Next() {
		var item domain.Conversation
		if err := rows.Scan(&item.ID, &item.BotID, &item.Title, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) CreateConversation(ctx context.Context, botID, title string) (domain.Conversation, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "New conversation"
	}
	timestamp := now()
	item := domain.Conversation{ID: id.New("conv"), BotID: botID, Title: title, CreatedAt: timestamp, UpdatedAt: timestamp}
	_, err := s.db.ExecContext(ctx, "INSERT INTO conversations (id, bot_id, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?)", item.ID, item.BotID, item.Title, item.CreatedAt, item.UpdatedAt)
	return item, err
}

func (s *Store) RenameConversation(ctx context.Context, conversationID, title string) (domain.Conversation, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return domain.Conversation{}, errors.New("conversation title is required")
	}
	result, err := s.db.ExecContext(ctx, "UPDATE conversations SET title = ?, updated_at = ? WHERE id = ?", title, now(), conversationID)
	if err != nil {
		return domain.Conversation{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return domain.Conversation{}, err
	}
	if count == 0 {
		return domain.Conversation{}, errors.New("conversation not found")
	}
	return s.GetConversation(ctx, conversationID)
}

func (s *Store) Search(ctx context.Context, query string, limit int) ([]domain.SearchHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []domain.SearchHit{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	pattern := "%" + query + "%"
	hits := make([]domain.SearchHit, 0)
	conversationRows, err := s.db.QueryContext(ctx, "SELECT id, title, updated_at FROM conversations WHERE title LIKE ? ORDER BY updated_at DESC, id DESC LIMIT ?", pattern, limit)
	if err != nil {
		return nil, err
	}
	for conversationRows.Next() {
		var item domain.SearchHit
		if err := conversationRows.Scan(&item.ID, &item.Title, &item.CreatedAt); err != nil {
			_ = conversationRows.Close()
			return nil, err
		}
		item.Kind = "conversation"
		item.Snippet = item.Title
		hits = append(hits, item)
	}
	if err := conversationRows.Err(); err != nil {
		_ = conversationRows.Close()
		return nil, err
	}
	_ = conversationRows.Close()
	messageRows, err := s.db.QueryContext(ctx, `SELECT m.id, m.conversation_id, c.title, m.content, m.created_at
		FROM messages m JOIN conversations c ON c.id = m.conversation_id
		WHERE m.content LIKE ? ORDER BY m.created_at DESC, m.id DESC LIMIT ?`, pattern, limit)
	if err != nil {
		return nil, err
	}
	for messageRows.Next() {
		var item domain.SearchHit
		if err := messageRows.Scan(&item.ID, &item.ConversationID, &item.Title, &item.Snippet, &item.CreatedAt); err != nil {
			_ = messageRows.Close()
			return nil, err
		}
		item.Kind = "message"
		hits = append(hits, item)
	}
	if err := messageRows.Err(); err != nil {
		_ = messageRows.Close()
		return nil, err
	}
	_ = messageRows.Close()
	return hits, nil
}

func (s *Store) ListMessages(ctx context.Context, conversationID string) ([]domain.Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, conversation_id, role, content, created_at,
		COALESCE(kind, ''), COALESCE(author_bot_id, ''), COALESCE(mentions, ''), COALESCE(handoff_id, '')
		FROM messages WHERE conversation_id = ? ORDER BY created_at, id`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.Message, 0)
	for rows.Next() {
		item, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) CreateMessage(ctx context.Context, conversationID, role, content string) (domain.Message, error) {
	item := domain.Message{ID: id.New("msg"), ConversationID: conversationID, Role: role, Content: content, CreatedAt: now()}
	_, err := s.db.ExecContext(ctx, "INSERT INTO messages (id, conversation_id, role, content, created_at) VALUES (?, ?, ?, ?, ?)", item.ID, item.ConversationID, item.Role, item.Content, item.CreatedAt)
	return item, err
}

// CreateMessageForActiveRun atomically guards a provider response against a
// user stop. A provider can ignore cancellation and return after /stop has
// durably moved the run to stopped; checking the run and inserting the
// assistant message in one transaction prevents that late answer from
// appearing in the conversation.
func (s *Store) CreateMessageForActiveRun(ctx context.Context, runID, conversationID, role, content string) (domain.Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Message{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	if err := tx.QueryRowContext(ctx, "SELECT status FROM runs WHERE id = ? AND conversation_id = ?", runID, conversationID).Scan(&status); err != nil {
		return domain.Message{}, err
	}
	switch status {
	case "completed", "failed", "stopped", "blocked":
		return domain.Message{}, ErrRunTerminal
	}
	item := domain.Message{ID: id.New("msg"), ConversationID: conversationID, Role: role, Content: content, CreatedAt: now()}
	if _, err := tx.ExecContext(ctx, "INSERT INTO messages (id, conversation_id, role, content, created_at) VALUES (?, ?, ?, ?, ?)", item.ID, item.ConversationID, item.Role, item.Content, item.CreatedAt); err != nil {
		return domain.Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Message{}, err
	}
	return item, nil
}

func (s *Store) CreateAttachment(ctx context.Context, item domain.Attachment) (domain.Attachment, error) {
	if item.ID == "" {
		item.ID = id.New("file")
	}
	if item.CreatedAt == "" {
		item.CreatedAt = now()
	}
	var messageID any
	if item.MessageID != "" {
		messageID = item.MessageID
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO attachments (id, conversation_id, message_id, name, media_type, size, storage_path, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)", item.ID, item.ConversationID, messageID, item.Name, item.MediaType, item.Size, item.StoragePath, item.CreatedAt)
	return item, err
}

func pendingAttachmentsTx(ctx context.Context, tx *sql.Tx, conversationID string, attachmentIDs []string) ([]domain.Attachment, error) {
	attachments := make([]domain.Attachment, 0, len(attachmentIDs))
	seen := make(map[string]struct{}, len(attachmentIDs))
	for _, attachmentID := range attachmentIDs {
		if attachmentID == "" {
			return nil, errors.New("attachment id is required")
		}
		if _, exists := seen[attachmentID]; exists {
			return nil, errors.New("attachment was supplied more than once")
		}
		seen[attachmentID] = struct{}{}
		var attachment domain.Attachment
		err := tx.QueryRowContext(ctx, "SELECT id, conversation_id, COALESCE(message_id, ''), name, media_type, size, storage_path, created_at FROM attachments WHERE id = ? AND conversation_id = ? AND message_id IS NULL", attachmentID, conversationID).Scan(&attachment.ID, &attachment.ConversationID, &attachment.MessageID, &attachment.Name, &attachment.MediaType, &attachment.Size, &attachment.StoragePath, &attachment.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("attachment is not pending for this conversation")
		}
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, nil
}

// CreateMessageWithAttachments atomically claims uploads that are still
// pending for this conversation. A client cannot attach a file from another
// conversation or reuse an already-sent upload.
func (s *Store) CreateMessageWithAttachments(ctx context.Context, conversationID, role, content string, attachmentIDs []string) (domain.Message, []domain.Attachment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Message{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	attachments, err := pendingAttachmentsTx(ctx, tx, conversationID, attachmentIDs)
	if err != nil {
		return domain.Message{}, nil, err
	}
	message := domain.Message{ID: id.New("msg"), ConversationID: conversationID, Role: role, Content: content, CreatedAt: now()}
	if _, err := tx.ExecContext(ctx, "INSERT INTO messages (id, conversation_id, role, content, created_at) VALUES (?, ?, ?, ?, ?)", message.ID, message.ConversationID, message.Role, message.Content, message.CreatedAt); err != nil {
		return domain.Message{}, nil, err
	}
	for index := range attachments {
		if _, err := tx.ExecContext(ctx, "UPDATE attachments SET message_id = ? WHERE id = ?", message.ID, attachments[index].ID); err != nil {
			return domain.Message{}, nil, err
		}
		attachments[index].MessageID = message.ID
	}
	if err := tx.Commit(); err != nil {
		return domain.Message{}, nil, err
	}
	return message, attachments, nil
}

// GetPendingAttachments returns the metadata needed to build a prompt without
// claiming the uploads. The final send must still use the atomic message/run
// method because a draft can change between these two reads.
func (s *Store) GetPendingAttachments(ctx context.Context, conversationID string, attachmentIDs []string) ([]domain.Attachment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	return pendingAttachmentsTx(ctx, tx, conversationID, attachmentIDs)
}

// CreateMessageWithAttachmentsAndRun makes the user message, attachment
// claims, queued run, and first lifecycle event one durable transaction. This
// prevents a provider/DB failure after the upload claim from leaving a file
// consumed without a runnable turn.
func (s *Store) CreateMessageWithAttachmentsAndRun(ctx context.Context, conversationID, botID, provider, content, prompt string, attachmentIDs []string) (domain.Message, []domain.Attachment, domain.Run, domain.RunEvent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Message{}, nil, domain.Run{}, domain.RunEvent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	attachments, err := pendingAttachmentsTx(ctx, tx, conversationID, attachmentIDs)
	if err != nil {
		return domain.Message{}, nil, domain.Run{}, domain.RunEvent{}, err
	}
	timestamp := now()
	message := domain.Message{ID: id.New("msg"), ConversationID: conversationID, Role: "user", Content: content, CreatedAt: timestamp}
	if _, err := tx.ExecContext(ctx, "INSERT INTO messages (id, conversation_id, role, content, created_at) VALUES (?, ?, ?, ?, ?)", message.ID, message.ConversationID, message.Role, message.Content, message.CreatedAt); err != nil {
		return domain.Message{}, nil, domain.Run{}, domain.RunEvent{}, err
	}
	for index := range attachments {
		if _, err := tx.ExecContext(ctx, "UPDATE attachments SET message_id = ? WHERE id = ? AND message_id IS NULL", message.ID, attachments[index].ID); err != nil {
			return domain.Message{}, nil, domain.Run{}, domain.RunEvent{}, err
		}
		attachments[index].MessageID = message.ID
	}
	run := domain.Run{ID: id.New("run"), ConversationID: conversationID, BotID: botID, Provider: provider, Status: "queued", Prompt: prompt, CreatedAt: timestamp, UpdatedAt: timestamp}
	event := domain.RunEvent{ID: id.New("evt"), RunID: run.ID, Type: "run.queued", Data: `{"status":"queued"}`, CreatedAt: timestamp}
	if _, err := tx.ExecContext(ctx, "INSERT INTO runs (id, conversation_id, bot_id, provider, status, prompt, error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", run.ID, run.ConversationID, run.BotID, run.Provider, run.Status, run.Prompt, "", run.CreatedAt, run.UpdatedAt); err != nil {
		return domain.Message{}, nil, domain.Run{}, domain.RunEvent{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO run_events (id, run_id, type, data, created_at) VALUES (?, ?, ?, ?, ?)", event.ID, event.RunID, event.Type, event.Data, event.CreatedAt); err != nil {
		return domain.Message{}, nil, domain.Run{}, domain.RunEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Message{}, nil, domain.Run{}, domain.RunEvent{}, err
	}
	return message, attachments, run, event, nil
}

// DeletePendingAttachmentsBefore removes only unclaimed uploads older than
// cutoff and returns their paths so the HTTP layer can remove the files.
func (s *Store) DeletePendingAttachmentsBefore(ctx context.Context, cutoff time.Time) ([]domain.Attachment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, "SELECT id, conversation_id, COALESCE(message_id, ''), name, media_type, size, storage_path, created_at FROM attachments WHERE message_id IS NULL AND created_at < ? ORDER BY created_at, id", cutoff.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	items := make([]domain.Attachment, 0)
	for rows.Next() {
		var item domain.Attachment
		if err := rows.Scan(&item.ID, &item.ConversationID, &item.MessageID, &item.Name, &item.MediaType, &item.Size, &item.StoragePath, &item.CreatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, "DELETE FROM attachments WHERE id = ? AND message_id IS NULL", item.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) GetAttachment(ctx context.Context, attachmentID string) (domain.Attachment, error) {
	var item domain.Attachment
	err := s.db.QueryRowContext(ctx, "SELECT id, conversation_id, COALESCE(message_id, ''), name, media_type, size, storage_path, created_at FROM attachments WHERE id = ?", attachmentID).Scan(&item.ID, &item.ConversationID, &item.MessageID, &item.Name, &item.MediaType, &item.Size, &item.StoragePath, &item.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, errors.New("attachment not found")
	}
	return item, err
}

func (s *Store) ListAttachments(ctx context.Context, conversationID string) ([]domain.Attachment, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, conversation_id, COALESCE(message_id, ''), name, media_type, size, storage_path, created_at FROM attachments WHERE conversation_id = ? ORDER BY created_at, id", conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Attachment, 0)
	for rows.Next() {
		var item domain.Attachment
		if err := rows.Scan(&item.ID, &item.ConversationID, &item.MessageID, &item.Name, &item.MediaType, &item.Size, &item.StoragePath, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DeletePendingAttachment(ctx context.Context, attachmentID string) (domain.Attachment, error) {
	item, err := s.GetAttachment(ctx, attachmentID)
	if err != nil {
		return item, err
	}
	if item.MessageID != "" {
		return item, errors.New("sent attachments cannot be deleted from the draft")
	}
	result, err := s.db.ExecContext(ctx, "DELETE FROM attachments WHERE id = ? AND message_id IS NULL", attachmentID)
	if err != nil {
		return item, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return item, err
	}
	if count != 1 {
		return item, errors.New("attachment is no longer pending")
	}
	return item, nil
}

func (s *Store) CreateRun(ctx context.Context, conversationID, botID, provider, prompt string) (domain.Run, error) {
	timestamp := now()
	item := domain.Run{ID: id.New("run"), ConversationID: conversationID, BotID: botID, Provider: provider, Status: "queued", Prompt: prompt, CreatedAt: timestamp, UpdatedAt: timestamp}
	_, err := s.db.ExecContext(ctx, "INSERT INTO runs (id, conversation_id, bot_id, provider, status, prompt, error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", item.ID, item.ConversationID, item.BotID, item.Provider, item.Status, item.Prompt, "", item.CreatedAt, item.UpdatedAt)
	return item, err
}

// CreateRunWithQueuedEvent commits the run and its initial durable lifecycle
// event together, so readers can never observe one without the other.
func (s *Store) CreateRunWithQueuedEvent(ctx context.Context, conversationID, botID, provider, prompt string) (domain.Run, domain.RunEvent, error) {
	timestamp := now()
	run := domain.Run{ID: id.New("run"), ConversationID: conversationID, BotID: botID, Provider: provider, Status: "queued", Prompt: prompt, CreatedAt: timestamp, UpdatedAt: timestamp}
	event := domain.RunEvent{ID: id.New("evt"), RunID: run.ID, Type: "run.queued", Data: `{"status":"queued"}`, CreatedAt: timestamp}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Run{}, domain.RunEvent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "INSERT INTO runs (id, conversation_id, bot_id, provider, status, prompt, error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", run.ID, run.ConversationID, run.BotID, run.Provider, run.Status, run.Prompt, "", run.CreatedAt, run.UpdatedAt); err != nil {
		return domain.Run{}, domain.RunEvent{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO run_events (id, run_id, type, data, created_at) VALUES (?, ?, ?, ?, ?)", event.ID, event.RunID, event.Type, event.Data, event.CreatedAt); err != nil {
		return domain.Run{}, domain.RunEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Run{}, domain.RunEvent{}, err
	}
	return run, event, nil
}

func (s *Store) ListRuns(ctx context.Context, conversationID string) ([]domain.Run, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, conversation_id, bot_id, provider, status, prompt, error, created_at, updated_at FROM runs WHERE conversation_id = ? ORDER BY created_at, id", conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.Run, 0)
	for rows.Next() {
		var item domain.Run
		if err := rows.Scan(&item.ID, &item.ConversationID, &item.BotID, &item.Provider, &item.Status, &item.Prompt, &item.Error, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) GetRun(ctx context.Context, runID string) (domain.Run, error) {
	var item domain.Run
	err := s.db.QueryRowContext(ctx, "SELECT id, conversation_id, bot_id, provider, status, prompt, error, created_at, updated_at FROM runs WHERE id = ?", runID).Scan(&item.ID, &item.ConversationID, &item.BotID, &item.Provider, &item.Status, &item.Prompt, &item.Error, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, fmt.Errorf("run not found")
	}
	return item, err
}

func (s *Store) UpsertHarnessSession(ctx context.Context, conversationID, provider, nativeSessionID, workdir, title, status string) (domain.HarnessSession, error) {
	var existing domain.HarnessSession
	err := s.db.QueryRowContext(ctx, "SELECT id, conversation_id, provider, native_session_id, workdir, title, status, created_at, updated_at FROM harness_sessions WHERE provider = ? AND native_session_id = ?", provider, nativeSessionID).Scan(&existing.ID, &existing.ConversationID, &existing.Provider, &existing.NativeSessionID, &existing.Workdir, &existing.Title, &existing.Status, &existing.CreatedAt, &existing.UpdatedAt)
	if err == nil {
		if _, updateErr := s.db.ExecContext(ctx, "UPDATE harness_sessions SET conversation_id = ?, workdir = ?, title = ?, status = ?, updated_at = ? WHERE id = ?", conversationID, workdir, title, status, now(), existing.ID); updateErr != nil {
			return existing, updateErr
		}
		existing.ConversationID, existing.Workdir, existing.Title, existing.Status, existing.UpdatedAt = conversationID, workdir, title, status, now()
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return existing, err
	}
	timestamp := now()
	item := domain.HarnessSession{ID: id.New("session"), ConversationID: conversationID, Provider: provider, NativeSessionID: nativeSessionID, Workdir: workdir, Title: title, Status: status, CreatedAt: timestamp, UpdatedAt: timestamp}
	_, err = s.db.ExecContext(ctx, "INSERT INTO harness_sessions (id, conversation_id, provider, native_session_id, workdir, title, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", item.ID, item.ConversationID, item.Provider, item.NativeSessionID, item.Workdir, item.Title, item.Status, item.CreatedAt, item.UpdatedAt)
	return item, err
}

func (s *Store) GetHarnessSession(ctx context.Context, conversationID, provider string) (domain.HarnessSession, error) {
	var item domain.HarnessSession
	err := s.db.QueryRowContext(ctx, "SELECT id, conversation_id, provider, native_session_id, workdir, title, status, created_at, updated_at FROM harness_sessions WHERE conversation_id = ? AND provider = ? ORDER BY updated_at DESC, id DESC LIMIT 1", conversationID, provider).Scan(&item.ID, &item.ConversationID, &item.Provider, &item.NativeSessionID, &item.Workdir, &item.Title, &item.Status, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, fmt.Errorf("harness session not found")
	}
	return item, err
}

func (s *Store) ListHarnessSessions(ctx context.Context, conversationID string) ([]domain.HarnessSession, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, conversation_id, provider, native_session_id, workdir, title, status, created_at, updated_at FROM harness_sessions WHERE conversation_id = ? ORDER BY updated_at DESC, id DESC", conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.HarnessSession, 0)
	for rows.Next() {
		var item domain.HarnessSession
		if err := rows.Scan(&item.ID, &item.ConversationID, &item.Provider, &item.NativeSessionID, &item.Workdir, &item.Title, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) DeleteHarnessSession(ctx context.Context, provider, nativeSessionID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM harness_sessions WHERE provider = ? AND native_session_id = ?", provider, nativeSessionID)
	return err
}

func (s *Store) ListRunEvents(ctx context.Context, runID string) ([]domain.RunEvent, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, run_id, type, data, created_at FROM run_events WHERE run_id = ? ORDER BY created_at, id", runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.RunEvent, 0)
	for rows.Next() {
		var item domain.RunEvent
		if err := rows.Scan(&item.ID, &item.RunID, &item.Type, &item.Data, &item.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) ListConversationEvents(ctx context.Context, conversationID, afterID string) ([]domain.StreamEvent, error) {
	query := `SELECT e.id, e.run_id, r.conversation_id, e.type, e.data, e.created_at
		FROM run_events e JOIN runs r ON r.id = e.run_id
		WHERE r.conversation_id = ?`
	args := []any{conversationID}
	if afterID != "" {
		var cursorCreatedAt string
		if err := s.db.QueryRowContext(ctx, "SELECT created_at FROM run_events WHERE id = ?", afterID).Scan(&cursorCreatedAt); err == nil {
			query += " AND (e.created_at > ? OR (e.created_at = ? AND e.id > ?))"
			args = append(args, cursorCreatedAt, cursorCreatedAt, afterID)
		}
	}
	query += " ORDER BY e.created_at, e.id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.StreamEvent, 0)
	for rows.Next() {
		var item domain.StreamEvent
		if err := rows.Scan(&item.ID, &item.RunID, &item.ConversationID, &item.Type, &item.Data, &item.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) CreateApproval(ctx context.Context, runID, provider, action, payload string) (domain.ApprovalRequest, error) {
	timestamp := now()
	item := domain.ApprovalRequest{ID: id.New("approval"), RunID: runID, Provider: provider, Action: action, Payload: payload, Status: "pending", CreatedAt: timestamp, UpdatedAt: timestamp}
	_, err := s.db.ExecContext(ctx, "INSERT INTO approval_requests (id, run_id, provider, action, payload, status, selected_option_id, created_at, updated_at, resolved_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", item.ID, item.RunID, item.Provider, item.Action, item.Payload, item.Status, "", item.CreatedAt, item.UpdatedAt, "")
	return item, err
}

func (s *Store) GetApproval(ctx context.Context, approvalID string) (domain.ApprovalRequest, error) {
	var item domain.ApprovalRequest
	err := s.db.QueryRowContext(ctx, "SELECT id, run_id, provider, action, payload, status, selected_option_id, created_at, updated_at, resolved_at FROM approval_requests WHERE id = ?", approvalID).Scan(&item.ID, &item.RunID, &item.Provider, &item.Action, &item.Payload, &item.Status, &item.SelectedOptionID, &item.CreatedAt, &item.UpdatedAt, &item.ResolvedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, fmt.Errorf("approval not found")
	}
	return item, err
}

func (s *Store) ListApprovals(ctx context.Context, status string) ([]domain.ApprovalRequest, error) {
	query := "SELECT id, run_id, provider, action, payload, status, selected_option_id, created_at, updated_at, resolved_at FROM approval_requests ORDER BY created_at, id"
	args := []any{}
	if status != "" {
		query = "SELECT id, run_id, provider, action, payload, status, selected_option_id, created_at, updated_at, resolved_at FROM approval_requests WHERE status = ? ORDER BY created_at, id"
		args = append(args, status)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.ApprovalRequest, 0)
	for rows.Next() {
		var item domain.ApprovalRequest
		if err := rows.Scan(&item.ID, &item.RunID, &item.Provider, &item.Action, &item.Payload, &item.Status, &item.SelectedOptionID, &item.CreatedAt, &item.UpdatedAt, &item.ResolvedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) ResolveApproval(ctx context.Context, approvalID, status, optionID string) error {
	if status != "approved" && status != "denied" && status != "cancelled" {
		return errors.New("invalid approval status")
	}
	timestamp := now()
	result, err := s.db.ExecContext(ctx, "UPDATE approval_requests SET status = ?, selected_option_id = ?, updated_at = ?, resolved_at = ? WHERE id = ? AND status = 'pending'", status, optionID, timestamp, timestamp, approvalID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return errors.New("approval is already resolved or does not exist")
	}
	return nil
}

// RecoverInterruptedRuns closes work that belonged to a previous botd
// process. A run cannot safely be resumed after its in-memory cancellation
// context and harness process have disappeared, so recovery is deliberately
// fail-closed: the run becomes stopped and every pending approval is
// cancelled in the same transaction. The operation is idempotent.
func (s *Store) RecoverInterruptedRuns(ctx context.Context) (int, error) {
	timestamp := now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("recover interrupted runs: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `SELECT id FROM runs
WHERE status IN ('queued', 'running', 'waiting_for_approval')
ORDER BY created_at, id`)
	if err != nil {
		return 0, fmt.Errorf("recover interrupted runs: list: %w", err)
	}
	var runIDs []string
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("recover interrupted runs: scan: %w", err)
		}
		runIDs = append(runIDs, runID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("recover interrupted runs: iterate: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("recover interrupted runs: close: %w", err)
	}

	for _, runID := range runIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE runs
SET status = 'stopped', error = '', updated_at = ?
WHERE id = ? AND status IN ('queued', 'running', 'waiting_for_approval')`, timestamp, runID); err != nil {
			return 0, fmt.Errorf("recover interrupted run %s: stop: %w", runID, err)
		}
		approvals, err := tx.QueryContext(ctx, `SELECT id FROM approval_requests
WHERE run_id = ? AND status = 'pending' ORDER BY created_at, id`, runID)
		if err != nil {
			return 0, fmt.Errorf("recover interrupted run %s: list approvals: %w", runID, err)
		}
		var approvalIDs []string
		for approvals.Next() {
			var approvalID string
			if err := approvals.Scan(&approvalID); err != nil {
				_ = approvals.Close()
				return 0, fmt.Errorf("recover interrupted run %s: scan approval: %w", runID, err)
			}
			approvalIDs = append(approvalIDs, approvalID)
		}
		if err := approvals.Err(); err != nil {
			_ = approvals.Close()
			return 0, fmt.Errorf("recover interrupted run %s: iterate approvals: %w", runID, err)
		}
		if err := approvals.Close(); err != nil {
			return 0, fmt.Errorf("recover interrupted run %s: close approvals: %w", runID, err)
		}
		for _, approvalID := range approvalIDs {
			if _, err := tx.ExecContext(ctx, `UPDATE approval_requests
SET status = 'cancelled', selected_option_id = '', updated_at = ?, resolved_at = ?
WHERE id = ? AND status = 'pending'`, timestamp, timestamp, approvalID); err != nil {
				return 0, fmt.Errorf("recover interrupted approval %s: cancel: %w", approvalID, err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO run_events
(id, run_id, type, data, created_at) VALUES (?, ?, 'approval.resolved', ?, ?)`, id.New("evt"), runID, `{"status":"cancelled","reason":"botd_restart"}`, timestamp); err != nil {
				return 0, fmt.Errorf("recover interrupted approval %s: event: %w", approvalID, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_events
(id, run_id, type, data, created_at) VALUES (?, ?, 'run.stopped', ?, ?)`, id.New("evt"), runID, `{"status":"stopped","reason":"botd_restart"}`, timestamp); err != nil {
			return 0, fmt.Errorf("recover interrupted run %s: event: %w", runID, err)
		}
	}
	// A previous process may have stopped a run after creating an approval but
	// before resolving it. Those terminal-run approvals are just as stale as
	// approvals on queued/running work and must not remain actionable forever.
	staleApprovals, err := tx.QueryContext(ctx, `SELECT a.id, a.run_id FROM approval_requests a
JOIN runs r ON r.id = a.run_id
WHERE a.status = 'pending'
  AND r.status NOT IN ('queued', 'running', 'waiting_for_approval')
ORDER BY a.created_at, a.id`)
	if err != nil {
		return 0, fmt.Errorf("recover stale approvals: list: %w", err)
	}
	var staleApprovalRows [][2]string
	for staleApprovals.Next() {
		var row [2]string
		if err := staleApprovals.Scan(&row[0], &row[1]); err != nil {
			_ = staleApprovals.Close()
			return 0, fmt.Errorf("recover stale approvals: scan: %w", err)
		}
		staleApprovalRows = append(staleApprovalRows, row)
	}
	if err := staleApprovals.Err(); err != nil {
		_ = staleApprovals.Close()
		return 0, fmt.Errorf("recover stale approvals: iterate: %w", err)
	}
	if err := staleApprovals.Close(); err != nil {
		return 0, fmt.Errorf("recover stale approvals: close: %w", err)
	}
	for _, row := range staleApprovalRows {
		if _, err := tx.ExecContext(ctx, `UPDATE approval_requests
SET status = 'cancelled', selected_option_id = '', updated_at = ?, resolved_at = ?
WHERE id = ? AND status = 'pending'`, timestamp, timestamp, row[0]); err != nil {
			return 0, fmt.Errorf("recover stale approval %s: cancel: %w", row[0], err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_events
(id, run_id, type, data, created_at) VALUES (?, ?, 'approval.resolved', ?, ?)`, id.New("evt"), row[1], `{"status":"cancelled","reason":"botd_restart"}`, timestamp); err != nil {
			return 0, fmt.Errorf("recover stale approval %s: event: %w", row[0], err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("recover interrupted runs: commit: %w", err)
	}
	return len(runIDs), nil
}

func (s *Store) UpdateRun(ctx context.Context, runID, status, runError string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE runs SET status = ?, error = ?, updated_at = ? WHERE id = ?", status, runError, now(), runID)
	return err
}

// UpdateRunWithLifecycleEvent commits a state change and the durable event
// describing it in one transaction. Broker publication belongs to the caller
// and must happen only after this method returns successfully.
func (s *Store) UpdateRunWithLifecycleEvent(ctx context.Context, runID, status, runError, eventType, data string) (domain.RunEvent, error) {
	timestamp := now()
	event := domain.RunEvent{ID: id.New("evt"), RunID: runID, Type: eventType, Data: data, CreatedAt: timestamp}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.RunEvent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// Lifecycle writes are monotonic once a run reaches a terminal state. This
	// prevents a provider that returns just after Stop from overwriting the
	// durable stopped/failed/completed result with a late completion.
	result, err := tx.ExecContext(ctx, "UPDATE runs SET status = ?, error = ?, updated_at = ? WHERE id = ? AND status NOT IN ('completed', 'failed', 'stopped', 'blocked')", status, runError, timestamp, runID)
	if err != nil {
		return domain.RunEvent{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return domain.RunEvent{}, err
	}
	if updated != 1 {
		return domain.RunEvent{}, errors.New("run not found")
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO run_events (id, run_id, type, data, created_at) VALUES (?, ?, ?, ?, ?)", event.ID, event.RunID, event.Type, event.Data, event.CreatedAt); err != nil {
		return domain.RunEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RunEvent{}, err
	}
	return event, nil
}

func (s *Store) AppendRunEvent(ctx context.Context, runID, eventType, data string) (domain.RunEvent, error) {
	item := domain.RunEvent{ID: id.New("evt"), RunID: runID, Type: eventType, Data: data, CreatedAt: now()}
	_, err := s.db.ExecContext(ctx, "INSERT INTO run_events (id, run_id, type, data, created_at) VALUES (?, ?, ?, ?, ?)", item.ID, item.RunID, item.Type, item.Data, item.CreatedAt)
	return item, err
}

func (s *Store) UpsertCapabilities(ctx context.Context, items []domain.Capability) error {
	for _, item := range items {
		_, err := s.db.ExecContext(ctx, "INSERT INTO capabilities (name, command, available, version, detail, checked_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(name) DO UPDATE SET command=excluded.command, available=excluded.available, version=excluded.version, detail=excluded.detail, checked_at=excluded.checked_at", item.Name, item.Command, item.Available, item.Version, item.Detail, now())
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListCapabilities(ctx context.Context) ([]domain.Capability, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT name, command, available, version, detail FROM capabilities ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.Capability, 0)
	for rows.Next() {
		var item domain.Capability
		if err := rows.Scan(&item.Name, &item.Command, &item.Available, &item.Version, &item.Detail); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
