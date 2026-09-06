package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
)

func (s *Store) migrateSessionContexts() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS harness_session_contexts (
 provider TEXT NOT NULL, native_session_id TEXT NOT NULL, context_key TEXT NOT NULL,
 PRIMARY KEY(provider,native_session_id));`)
	return err
}

func (s *Store) runSessionContext(ctx context.Context, runID, workdir string) (string, bool, error) {
	var conversationID, projectID string
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT r.conversation_id,COALESCE(p.project_id,''),COALESCE(p.brief_revision,0)
 FROM runs r LEFT JOIN project_task_snapshots p ON p.run_id=r.id WHERE r.id=?`, runID).Scan(&conversationID, &projectID, &revision)
	if err != nil {
		return "", false, err
	}
	raw, _ := json.Marshal([]any{conversationID, projectID, revision, filepath.Clean(workdir)})
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), projectID != "", nil
}

func (s *Store) BindHarnessSessionContext(ctx context.Context, runID, provider, nativeID, workdir string) error {
	key, _, err := s.runSessionContext(ctx, runID, workdir)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO harness_session_contexts(provider,native_session_id,context_key) VALUES(?,?,?) ON CONFLICT(provider,native_session_id) DO NOTHING`, provider, nativeID, key); err != nil {
		return err
	}
	var existing string
	if err := s.db.QueryRowContext(ctx, `SELECT context_key FROM harness_session_contexts WHERE provider=? AND native_session_id=?`, provider, nativeID).Scan(&existing); err != nil {
		return err
	}
	if existing != key {
		return errors.New("provider session belongs to a different project context")
	}
	return nil
}

func (s *Store) HarnessSessionMatchesRun(ctx context.Context, runID, provider, nativeID, workdir string) (bool, error) {
	key, hasProject, err := s.runSessionContext(ctx, runID, workdir)
	if err != nil {
		return false, err
	}
	var existing string
	err = s.db.QueryRowContext(ctx, `SELECT context_key FROM harness_session_contexts WHERE provider=? AND native_session_id=?`, provider, nativeID).Scan(&existing)
	if err == nil {
		return key == existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if hasProject {
		return false, nil
	}
	var conversationID, sessionConversation, sessionWorkdir string
	if err := s.db.QueryRowContext(ctx, `SELECT conversation_id FROM runs WHERE id=?`, runID).Scan(&conversationID); err != nil {
		return false, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT conversation_id,workdir FROM harness_sessions WHERE provider=? AND native_session_id=?`, provider, nativeID).Scan(&sessionConversation, &sessionWorkdir)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return conversationID == sessionConversation && filepath.Clean(workdir) == filepath.Clean(sessionWorkdir), nil
}
