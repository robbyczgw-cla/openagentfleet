package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
)

var (
	ErrMobileConversationNotFound = errors.New("mobile conversation not found")
	ErrMobileIdempotencyConflict  = errors.New("mobile idempotency key conflicts with an earlier request")
)

const (
	maxMobileIdempotencyKeyBytes = 128
	maxMobileMessageBytes        = 16 << 10
	maxMobileEventBatch          = 256
)

// AuthenticateMobileCredential returns the auth-versioned public session for
// one active alpha bearer. Ordinary failures intentionally collapse to the
// same error as the existing remote credential API.
func (s *Store) AuthenticateMobileCredential(ctx context.Context, rawBearer string) (domain.RemoteSession, error) {
	if err := validateRemoteBearer(rawBearer); err != nil {
		return domain.RemoteSession{}, ErrRemoteCredentialInvalid
	}
	wantedHash := sha256.Sum256([]byte(rawBearer))
	var (
		storedHash  []byte
		expiresAt   string
		revoked     int
		authVersion int
		device      domain.RemoteDevice
	)
	err := s.db.QueryRowContext(ctx, `SELECT c.token_hash, c.auth_version, c.expires_at, c.revoked,
d.id, d.display_name, d.platform, d.scope_profile, d.status, d.created_at, d.revoked_at, d.last_used_at
FROM remote_credentials c
JOIN remote_devices d ON d.id = c.device_id
WHERE c.token_hash = ?`, wantedHash[:]).Scan(
		&storedHash, &authVersion, &expiresAt, &revoked,
		&device.ID, &device.DisplayName, &device.Platform, &device.ScopeProfile,
		&device.Status, &device.CreatedAt, &device.RevokedAt, &device.LastUsedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RemoteSession{}, ErrRemoteCredentialInvalid
	}
	if err != nil {
		return domain.RemoteSession{}, fmt.Errorf("authenticate mobile credential: %w", err)
	}
	if len(storedHash) != sha256.Size || subtle.ConstantTimeCompare(storedHash, wantedHash[:]) != 1 {
		return domain.RemoteSession{}, ErrRemoteCredentialInvalid
	}
	expiry, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return domain.RemoteSession{}, fmt.Errorf("authenticate mobile credential: parse expiry: %w", err)
	}
	if revoked != 0 || device.Status != domain.RemoteDeviceActive || authVersion != domain.RemoteAuthVersionBearer || !expiry.After(time.Now().UTC()) {
		return domain.RemoteSession{}, ErrRemoteCredentialInvalid
	}
	return domain.RemoteSession{Device: device, AuthVersion: authVersion, ExpiresAt: expiresAt}, nil
}

// RevokeMobileCredential logs out only the presented credential. Device-wide
// revocation remains an explicit local-admin operation.
func (s *Store) RevokeMobileCredential(ctx context.Context, deviceID, rawBearer string) error {
	if strings.TrimSpace(deviceID) == "" || validateRemoteBearer(rawBearer) != nil {
		return ErrRemoteCredentialInvalid
	}
	hash := sha256.Sum256([]byte(rawBearer))
	result, err := s.db.ExecContext(ctx, `UPDATE remote_credentials
SET revoked = 1 WHERE token_hash = ? AND device_id = ? AND revoked = 0`, hash[:], deviceID)
	if err != nil {
		return fmt.Errorf("revoke mobile credential: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke mobile credential: %w", err)
	}
	if updated != 1 {
		return ErrRemoteCredentialInvalid
	}
	return nil
}

// MobileBootstrapSnapshot captures the selected conversation state and the
// durable event high-water cursor in one read transaction.
func (s *Store) MobileBootstrapSnapshot(ctx context.Context, conversationID string) (domain.MobileSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.MobileSnapshot{}, fmt.Errorf("begin mobile snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	conversations, err := listMobileConversations(ctx, tx)
	if err != nil {
		return domain.MobileSnapshot{}, err
	}
	if conversationID == "" && len(conversations) > 0 {
		conversationID = conversations[0].ID
	}
	var conversation domain.Conversation
	err = tx.QueryRowContext(ctx, `SELECT id, bot_id, title, created_at, updated_at
FROM conversations WHERE id = ?`, conversationID).Scan(
		&conversation.ID, &conversation.BotID, &conversation.Title, &conversation.CreatedAt, &conversation.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MobileSnapshot{}, ErrMobileConversationNotFound
	}
	if err != nil {
		return domain.MobileSnapshot{}, fmt.Errorf("load mobile conversation: %w", err)
	}

	messages, err := listMobileMessages(ctx, tx, conversation.ID)
	if err != nil {
		return domain.MobileSnapshot{}, err
	}
	runs, err := listMobileRuns(ctx, tx, conversation.ID)
	if err != nil {
		return domain.MobileSnapshot{}, err
	}
	var cursor uint64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(rowid), 0) FROM run_events").Scan(&cursor); err != nil {
		return domain.MobileSnapshot{}, fmt.Errorf("load mobile event cursor: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.MobileSnapshot{}, fmt.Errorf("commit mobile snapshot: %w", err)
	}
	return domain.MobileSnapshot{
		Conversations: conversations,
		Conversation:  conversation,
		Messages:      messages,
		Runs:          runs,
		EventCursor:   cursor,
	}, nil
}

// CreateMobileMessageRun atomically stores the user message, fixed-provider
// run, initial durable event, and immutable idempotent response.
func (s *Store) CreateMobileMessageRun(
	ctx context.Context,
	deviceID string,
	idempotencyKey string,
	conversationID string,
	content string,
) (domain.MobileMessageResponse, domain.Run, domain.StreamEvent, bool, error) {
	deviceID = strings.TrimSpace(deviceID)
	conversationID = strings.TrimSpace(conversationID)
	content = strings.TrimSpace(content)
	if deviceID == "" || conversationID == "" || content == "" || len(content) > maxMobileMessageBytes {
		return domain.MobileMessageResponse{}, domain.Run{}, domain.StreamEvent{}, false, errors.New("invalid mobile message")
	}
	if len(idempotencyKey) == 0 || len(idempotencyKey) > maxMobileIdempotencyKeyBytes || strings.IndexFunc(idempotencyKey, unicode.IsControl) >= 0 {
		return domain.MobileMessageResponse{}, domain.Run{}, domain.StreamEvent{}, false, errors.New("invalid mobile idempotency key")
	}
	keyHash := sha256.Sum256([]byte(idempotencyKey))
	requestHash := sha256.Sum256([]byte(conversationID + "\x00" + content + "\x00grok"))

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return domain.MobileMessageResponse{}, domain.Run{}, domain.StreamEvent{}, false, fmt.Errorf("acquire mobile message connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return domain.MobileMessageResponse{}, domain.Run{}, domain.StreamEvent{}, false, fmt.Errorf("configure mobile message transaction: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return domain.MobileMessageResponse{}, domain.Run{}, domain.StreamEvent{}, false, fmt.Errorf("begin mobile message transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var storedRequestHash []byte
	var storedResponse string
	err = conn.QueryRowContext(ctx, `SELECT request_hash, response_json
FROM mobile_message_idempotency WHERE device_id = ? AND key_hash = ?`, deviceID, keyHash[:]).Scan(&storedRequestHash, &storedResponse)
	if err == nil {
		if len(storedRequestHash) != sha256.Size || subtle.ConstantTimeCompare(storedRequestHash, requestHash[:]) != 1 {
			return domain.MobileMessageResponse{}, domain.Run{}, domain.StreamEvent{}, false, ErrMobileIdempotencyConflict
		}
		var response domain.MobileMessageResponse
		if err := json.Unmarshal([]byte(storedResponse), &response); err != nil {
			return domain.MobileMessageResponse{}, domain.Run{}, domain.StreamEvent{}, false, fmt.Errorf("decode mobile idempotent response: %w", err)
		}
		return response, domain.Run{}, domain.StreamEvent{}, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.MobileMessageResponse{}, domain.Run{}, domain.StreamEvent{}, false, fmt.Errorf("load mobile idempotency record: %w", err)
	}

	var deviceStatus string
	if err := conn.QueryRowContext(ctx, "SELECT status FROM remote_devices WHERE id = ?", deviceID).Scan(&deviceStatus); err != nil || deviceStatus != domain.RemoteDeviceActive {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return domain.MobileMessageResponse{}, domain.Run{}, domain.StreamEvent{}, false, fmt.Errorf("load mobile device: %w", err)
		}
		return domain.MobileMessageResponse{}, domain.Run{}, domain.StreamEvent{}, false, ErrRemoteCredentialInvalid
	}
	var botID string
	if err := conn.QueryRowContext(ctx, "SELECT bot_id FROM conversations WHERE id = ?", conversationID).Scan(&botID); errors.Is(err, sql.ErrNoRows) {
		return domain.MobileMessageResponse{}, domain.Run{}, domain.StreamEvent{}, false, ErrMobileConversationNotFound
	} else if err != nil {
		return domain.MobileMessageResponse{}, domain.Run{}, domain.StreamEvent{}, false, fmt.Errorf("load mobile message conversation: %w", err)
	}

	timestamp := now()
	message := domain.Message{ID: id.New("msg"), ConversationID: conversationID, Role: "user", Content: content, CreatedAt: timestamp}
	run := domain.Run{ID: id.New("run"), ConversationID: conversationID, BotID: botID, Provider: "grok", Status: "queued", Prompt: content, CreatedAt: timestamp, UpdatedAt: timestamp}
	event := domain.StreamEvent{ID: id.New("evt"), RunID: run.ID, ConversationID: conversationID, Type: "run.queued", Data: `{"status":"queued"}`, CreatedAt: timestamp}
	if _, err := conn.ExecContext(ctx, `INSERT INTO messages
(id, conversation_id, role, content, created_at) VALUES (?, ?, ?, ?, ?)`, message.ID, message.ConversationID, message.Role, message.Content, message.CreatedAt); err != nil {
		return domain.MobileMessageResponse{}, domain.Run{}, domain.StreamEvent{}, false, fmt.Errorf("create mobile message: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO runs
(id, conversation_id, bot_id, provider, status, prompt, error, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, '', ?, ?)`, run.ID, run.ConversationID, run.BotID, run.Provider, run.Status, run.Prompt, run.CreatedAt, run.UpdatedAt); err != nil {
		return domain.MobileMessageResponse{}, domain.Run{}, domain.StreamEvent{}, false, fmt.Errorf("create mobile run: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO run_events
(id, run_id, type, data, created_at) VALUES (?, ?, ?, ?, ?)`, event.ID, event.RunID, event.Type, event.Data, event.CreatedAt); err != nil {
		return domain.MobileMessageResponse{}, domain.Run{}, domain.StreamEvent{}, false, fmt.Errorf("create mobile queued event: %w", err)
	}
	response := domain.MobileMessageResponse{
		Message: message,
		Run: domain.MobileRun{
			ID: run.ID, ConversationID: run.ConversationID, Status: run.Status,
			CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
		},
	}
	responseJSON, err := json.Marshal(response)
	if err != nil {
		return domain.MobileMessageResponse{}, domain.Run{}, domain.StreamEvent{}, false, fmt.Errorf("encode mobile idempotent response: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO mobile_message_idempotency
(device_id, key_hash, request_hash, response_json, created_at) VALUES (?, ?, ?, ?, ?)`, deviceID, keyHash[:], requestHash[:], string(responseJSON), timestamp); err != nil {
		return domain.MobileMessageResponse{}, domain.Run{}, domain.StreamEvent{}, false, fmt.Errorf("store mobile idempotency record: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return domain.MobileMessageResponse{}, domain.Run{}, domain.StreamEvent{}, false, fmt.Errorf("commit mobile message: %w", err)
	}
	committed = true
	return response, run, event, true, nil
}

// ValidateMobileCursor accepts zero or the rowid of a currently retained
// durable event. Missing, future, and deleted cursors require a reset.
func (s *Store) ValidateMobileCursor(ctx context.Context, after uint64) (bool, error) {
	if after == 0 {
		return true, nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM run_events WHERE rowid = ?)", after).Scan(&exists); err != nil {
		return false, fmt.Errorf("validate mobile cursor: %w", err)
	}
	return exists == 1, nil
}

// ListMobileEventsAfter returns only lifecycle records whose payload can be
// rebuilt safely. Provider output, session details, and approvals are omitted.
func (s *Store) ListMobileEventsAfter(ctx context.Context, after uint64) ([]domain.MobileEventRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.rowid, e.type, e.run_id, r.conversation_id, e.created_at
FROM run_events e JOIN runs r ON r.id = e.run_id
WHERE e.rowid > ? AND e.type IN (
  'run.queued', 'run.started', 'run.blocked', 'run.stopped', 'run.failed',
  'run.completed', 'run.waiting_for_approval', 'run.resumed'
)
ORDER BY e.rowid LIMIT ?`, after, maxMobileEventBatch)
	if err != nil {
		return nil, fmt.Errorf("list mobile events: %w", err)
	}
	defer rows.Close()
	items := make([]domain.MobileEventRecord, 0)
	for rows.Next() {
		var item domain.MobileEventRecord
		if err := rows.Scan(&item.Cursor, &item.Type, &item.RunID, &item.ConversationID, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan mobile event: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mobile events: %w", err)
	}
	return items, nil
}

func listMobileConversations(ctx context.Context, tx *sql.Tx) ([]domain.Conversation, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, bot_id, title, created_at, updated_at
FROM conversations ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list mobile conversations: %w", err)
	}
	defer rows.Close()
	items := make([]domain.Conversation, 0)
	for rows.Next() {
		var item domain.Conversation
		if err := rows.Scan(&item.ID, &item.BotID, &item.Title, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan mobile conversation: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func listMobileMessages(ctx context.Context, tx *sql.Tx, conversationID string) ([]domain.Message, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, conversation_id, role, content, created_at
FROM messages WHERE conversation_id = ? ORDER BY created_at, id`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("list mobile messages: %w", err)
	}
	defer rows.Close()
	items := make([]domain.Message, 0)
	for rows.Next() {
		var item domain.Message
		if err := rows.Scan(&item.ID, &item.ConversationID, &item.Role, &item.Content, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan mobile message: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func listMobileRuns(ctx context.Context, tx *sql.Tx, conversationID string) ([]domain.MobileRun, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, conversation_id, status, created_at, updated_at
FROM runs WHERE conversation_id = ? ORDER BY created_at, id`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("list mobile runs: %w", err)
	}
	defer rows.Close()
	items := make([]domain.MobileRun, 0)
	for rows.Next() {
		var item domain.MobileRun
		if err := rows.Scan(&item.ID, &item.ConversationID, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan mobile run: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
