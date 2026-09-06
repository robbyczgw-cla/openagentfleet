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
	"sync"
	"unicode"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
)

const MaxTaskAttempts = 10

var (
	ErrTaskFollowupConflict = errors.New("task followup idempotency key conflicts with an earlier request")
	ErrTaskFollowupState    = errors.New("task status does not allow this followup")
	ErrTaskAttemptLimit     = errors.New("task attempt limit reached")
	taskFollowupMu          sync.Mutex
)

type CreateTaskFollowupInput struct {
	ParentTaskID        string
	Kind                string
	IdempotencyKey      string
	BotID               string
	Provider            string
	Content             string
	Prompt              string
	AttachmentIDs       []string
	SourceAttachmentIDs []string
}

type CreateTaskFollowupResult struct {
	Message     domain.Message
	Attachments []domain.Attachment
	Run         domain.Run
	QueuedEvent domain.RunEvent
	Created     bool
}

// MigrateTaskFollowups is additive so callers with a custom migration sequence
// can install task lineage without changing the core Store migration.
func (s *Store) MigrateTaskFollowups(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS task_attempts (
 run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
 parent_task_id TEXT NOT NULL REFERENCES runs(id),
 root_task_id TEXT NOT NULL REFERENCES runs(id),
 attempt INTEGER NOT NULL CHECK(attempt >= 2 AND attempt <= 10),
 kind TEXT NOT NULL CHECK(kind IN ('retry','revise')),
 message_id TEXT NOT NULL REFERENCES messages(id),
 idempotency_key_hash BLOB NOT NULL,
 request_hash BLOB NOT NULL,
 created_at TEXT NOT NULL,
 UNIQUE(parent_task_id,idempotency_key_hash)
 );
 CREATE INDEX IF NOT EXISTS task_attempts_root ON task_attempts(root_task_id,attempt);`)
	return err
}

// GetTaskInput returns only the message and attachments that formed this run.
// Older runs use the shared message/run timestamp written by the legacy creator.
func (s *Store) GetTaskInput(ctx context.Context, runID string) (domain.TaskInput, error) {
	var input domain.TaskInput
	var messageID string
	err := s.db.QueryRowContext(ctx, `SELECT r.bot_id,
 COALESCE(m.content,NULLIF(t.title,''),c.title),COALESCE(m.id,'')
 FROM runs r
 JOIN conversations c ON c.id=r.conversation_id
 LEFT JOIN task_results t ON t.run_id=r.id
 LEFT JOIN task_attempts a ON a.run_id=r.id
 LEFT JOIN messages m ON m.id=COALESCE(a.message_id,(
   SELECT legacy.id FROM messages legacy
   WHERE legacy.conversation_id=r.conversation_id AND legacy.role='user' AND legacy.created_at=r.created_at
   ORDER BY legacy.id LIMIT 1))
 WHERE r.id=?`, runID).Scan(&input.AgentID, &input.Brief, &messageID)
	if err != nil {
		return input, err
	}
	if messageID == "" {
		input.Attachments = []domain.Attachment{}
		return input, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,conversation_id,COALESCE(message_id,''),name,media_type,size,storage_path,created_at
 FROM attachments WHERE message_id=? ORDER BY created_at,id`, messageID)
	if err != nil {
		return input, err
	}
	defer rows.Close()
	input.Attachments = []domain.Attachment{}
	for rows.Next() {
		var attachment domain.Attachment
		if err := rows.Scan(&attachment.ID, &attachment.ConversationID, &attachment.MessageID, &attachment.Name, &attachment.MediaType, &attachment.Size, &attachment.StoragePath, &attachment.CreatedAt); err != nil {
			return input, err
		}
		input.Attachments = append(input.Attachments, attachment)
	}
	return input, rows.Err()
}

func taskFollowupRequestHash(input CreateTaskFollowupInput) [sha256.Size]byte {
	ids := append([]string{}, input.SourceAttachmentIDs...)
	requestJSON, _ := json.Marshal(struct {
		Kind, BotID, Content string
		SourceAttachmentIDs  []string
	}{strings.TrimSpace(input.Kind), strings.TrimSpace(input.BotID), strings.TrimSpace(input.Content), ids})
	return sha256.Sum256(requestJSON)
}

func (s *Store) FindTaskFollowup(ctx context.Context, input CreateTaskFollowupInput) (CreateTaskFollowupResult, bool, error) {
	keyHash := sha256.Sum256([]byte(strings.TrimSpace(input.IdempotencyKey)))
	requestHash := taskFollowupRequestHash(input)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CreateTaskFollowupResult{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var runID string
	var storedHash []byte
	err = tx.QueryRowContext(ctx, `SELECT run_id,request_hash FROM task_attempts WHERE parent_task_id=? AND idempotency_key_hash=?`, input.ParentTaskID, keyHash[:]).Scan(&runID, &storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return CreateTaskFollowupResult{}, false, nil
	}
	if err != nil {
		return CreateTaskFollowupResult{}, false, err
	}
	if subtle.ConstantTimeCompare(storedHash, requestHash[:]) != 1 {
		return CreateTaskFollowupResult{}, true, ErrTaskFollowupConflict
	}
	result, err := loadTaskFollowupTx(ctx, tx, runID)
	return result, true, err
}

func (s *Store) CreateTaskFollowup(ctx context.Context, input CreateTaskFollowupInput) (CreateTaskFollowupResult, error) {
	input.ParentTaskID = strings.TrimSpace(input.ParentTaskID)
	input.Kind = strings.TrimSpace(input.Kind)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.BotID = strings.TrimSpace(input.BotID)
	input.Content = strings.TrimSpace(input.Content)
	if input.ParentTaskID == "" || input.BotID == "" || input.Content == "" {
		return CreateTaskFollowupResult{}, errors.New("task followup fields are required")
	}
	if input.Kind != "retry" && input.Kind != "revise" {
		return CreateTaskFollowupResult{}, errors.New("task followup kind is invalid")
	}
	if len(input.IdempotencyKey) == 0 || len(input.IdempotencyKey) > 128 || strings.IndexFunc(input.IdempotencyKey, unicode.IsControl) >= 0 {
		return CreateTaskFollowupResult{}, errors.New("invalid task followup idempotency key")
	}
	requestHash := taskFollowupRequestHash(input)
	keyHash := sha256.Sum256([]byte(input.IdempotencyKey))

	taskFollowupMu.Lock()
	defer taskFollowupMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CreateTaskFollowupResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var existingRunID string
	var storedHash []byte
	err = tx.QueryRowContext(ctx, `SELECT run_id,request_hash FROM task_attempts WHERE parent_task_id=? AND idempotency_key_hash=?`, input.ParentTaskID, keyHash[:]).Scan(&existingRunID, &storedHash)
	if err == nil {
		if len(storedHash) != sha256.Size || subtle.ConstantTimeCompare(storedHash, requestHash[:]) != 1 {
			return CreateTaskFollowupResult{}, ErrTaskFollowupConflict
		}
		return loadTaskFollowupTx(ctx, tx, existingRunID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CreateTaskFollowupResult{}, err
	}

	var conversationID, parentBotID, parentStatus, rootTaskID string
	var parentAttempt int
	err = tx.QueryRowContext(ctx, `SELECT r.conversation_id,r.bot_id,r.status,COALESCE(a.root_task_id,r.id),COALESCE(a.attempt,1)
 FROM runs r LEFT JOIN task_attempts a ON a.run_id=r.id WHERE r.id=?`, input.ParentTaskID).Scan(&conversationID, &parentBotID, &parentStatus, &rootTaskID, &parentAttempt)
	if err != nil {
		return CreateTaskFollowupResult{}, err
	}
	if input.BotID != parentBotID {
		return CreateTaskFollowupResult{}, errors.New("agent_id must match the original task")
	}
	allowed := input.Kind == "retry" && (parentStatus == "failed" || parentStatus == "stopped") || input.Kind == "revise" && parentStatus == "completed"
	if !allowed {
		return CreateTaskFollowupResult{}, ErrTaskFollowupState
	}
	if parentAttempt >= MaxTaskAttempts {
		return CreateTaskFollowupResult{}, ErrTaskAttemptLimit
	}
	attachments, err := pendingAttachmentsTx(ctx, tx, conversationID, input.AttachmentIDs)
	if err != nil {
		return CreateTaskFollowupResult{}, err
	}
	timestamp := now()
	message := domain.Message{ID: id.New("msg"), ConversationID: conversationID, Role: "user", Content: input.Content, CreatedAt: timestamp}
	run := domain.Run{ID: id.New("run"), ConversationID: conversationID, BotID: input.BotID, Provider: input.Provider, Status: "queued", Prompt: input.Prompt, CreatedAt: timestamp, UpdatedAt: timestamp}
	event := domain.RunEvent{ID: id.New("evt"), RunID: run.ID, Type: "run.queued", Data: `{"status":"queued"}`, CreatedAt: timestamp}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages(id,conversation_id,role,content,created_at) VALUES(?,?,?,?,?)`, message.ID, message.ConversationID, message.Role, message.Content, message.CreatedAt); err != nil {
		return CreateTaskFollowupResult{}, err
	}
	for index := range attachments {
		result, err := tx.ExecContext(ctx, `UPDATE attachments SET message_id=? WHERE id=? AND message_id IS NULL`, message.ID, attachments[index].ID)
		if err != nil {
			return CreateTaskFollowupResult{}, err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return CreateTaskFollowupResult{}, errors.New("attachment is no longer pending")
		}
		attachments[index].MessageID = message.ID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs(id,conversation_id,bot_id,provider,status,prompt,error,created_at,updated_at) VALUES(?,?,?,?,?,?,'',?,?)`, run.ID, run.ConversationID, run.BotID, run.Provider, run.Status, run.Prompt, run.CreatedAt, run.UpdatedAt); err != nil {
		return CreateTaskFollowupResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_events(id,run_id,type,data,created_at) VALUES(?,?,?,?,?)`, event.ID, event.RunID, event.Type, event.Data, event.CreatedAt); err != nil {
		return CreateTaskFollowupResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_results(run_id,title) VALUES(?,?)`, run.ID, input.Content); err != nil {
		return CreateTaskFollowupResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_attempts(run_id,parent_task_id,root_task_id,attempt,kind,message_id,idempotency_key_hash,request_hash,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, run.ID, input.ParentTaskID, rootTaskID, parentAttempt+1, input.Kind, message.ID, keyHash[:], requestHash[:], timestamp); err != nil {
		return CreateTaskFollowupResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreateTaskFollowupResult{}, err
	}
	return CreateTaskFollowupResult{Message: message, Attachments: attachments, Run: run, QueuedEvent: event, Created: true}, nil
}

func loadTaskFollowupTx(ctx context.Context, tx *sql.Tx, runID string) (CreateTaskFollowupResult, error) {
	var result CreateTaskFollowupResult
	err := tx.QueryRowContext(ctx, `SELECT r.id,r.conversation_id,r.bot_id,r.provider,r.status,r.prompt,r.error,r.created_at,r.updated_at,
 m.id,m.conversation_id,m.role,m.content,m.created_at
 FROM runs r JOIN task_attempts a ON a.run_id=r.id JOIN messages m ON m.id=a.message_id WHERE r.id=?`, runID).Scan(
		&result.Run.ID, &result.Run.ConversationID, &result.Run.BotID, &result.Run.Provider, &result.Run.Status, &result.Run.Prompt, &result.Run.Error, &result.Run.CreatedAt, &result.Run.UpdatedAt,
		&result.Message.ID, &result.Message.ConversationID, &result.Message.Role, &result.Message.Content, &result.Message.CreatedAt)
	if err != nil {
		return result, fmt.Errorf("load task followup: %w", err)
	}
	err = tx.QueryRowContext(ctx, `SELECT id,run_id,type,data,created_at FROM run_events WHERE run_id=? AND type='run.queued' ORDER BY created_at,id LIMIT 1`, runID).Scan(&result.QueuedEvent.ID, &result.QueuedEvent.RunID, &result.QueuedEvent.Type, &result.QueuedEvent.Data, &result.QueuedEvent.CreatedAt)
	if err != nil {
		return result, fmt.Errorf("load task followup event: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,conversation_id,COALESCE(message_id,''),name,media_type,size,storage_path,created_at FROM attachments WHERE message_id=? ORDER BY created_at,id`, result.Message.ID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var attachment domain.Attachment
		if err := rows.Scan(&attachment.ID, &attachment.ConversationID, &attachment.MessageID, &attachment.Name, &attachment.MediaType, &attachment.Size, &attachment.StoragePath, &attachment.CreatedAt); err != nil {
			return result, err
		}
		result.Attachments = append(result.Attachments, attachment)
	}
	return result, rows.Err()
}
