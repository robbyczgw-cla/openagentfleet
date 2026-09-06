package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
	"github.com/robbyczgw-cla/openagentfleet/internal/id"
)

func (s *Store) MigrateTasks(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS task_results (
 run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
 title TEXT NOT NULL DEFAULT '', result TEXT NOT NULL DEFAULT ''
 ); CREATE TABLE IF NOT EXISTS task_artifacts (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
 name TEXT NOT NULL, media_type TEXT NOT NULL, size INTEGER NOT NULL,
 created_at TEXT NOT NULL, preview_kind TEXT NOT NULL, content BLOB NOT NULL,
 UNIQUE(run_id,name)); CREATE INDEX IF NOT EXISTS task_artifacts_run ON task_artifacts(run_id);`)
	return err
}

type TaskFilter struct {
	BotID, Status, Query      string
	BeforeCreatedAt, BeforeID string
	Limit                     int
}

const taskStatus = `CASE
 WHEN r.status NOT IN ('completed','failed','stopped','blocked') AND EXISTS (SELECT 1 FROM approval_requests p WHERE p.run_id=r.id AND p.status='pending') THEN 'waiting_approval'
 WHEN r.status='waiting_for_approval' THEN 'waiting_approval'
 ELSE r.status END`
const taskColumns = `r.id,r.bot_id,b.name,r.conversation_id,COALESCE(NULLIF(t.title,''),c.title),` + taskStatus + `,r.error,r.provider,r.created_at,r.updated_at,substr(COALESCE(t.result,''),1,280),(SELECT COUNT(*) FROM task_artifacts a WHERE a.run_id=r.id)`
const taskJoins = ` FROM runs r JOIN bots b ON b.id=r.bot_id JOIN conversations c ON c.id=r.conversation_id LEFT JOIN task_results t ON t.run_id=r.id `

const (
	maxTaskArtifacts    = 20
	maxTaskArtifactSize = 10 << 20
)

var ErrTaskArtifactLimit = errors.New("task artifact limit reached")

func scanTask(row interface{ Scan(...any) error }) (domain.TaskSummary, error) {
	var t domain.TaskSummary
	err := row.Scan(&t.ID, &t.BotID, &t.BotName, &t.ConversationID, &t.Title, &t.Status, &t.Error, &t.Provider, &t.CreatedAt, &t.UpdatedAt, &t.ResultPreview, &t.ArtifactCount)
	return t, err
}

func (s *Store) ListTasks(ctx context.Context, f TaskFilter) ([]domain.TaskSummary, bool, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	where := []string{"1=1"}
	args := []any{}
	if f.BotID != "" {
		where = append(where, "r.bot_id=?")
		args = append(args, f.BotID)
	}
	if f.Status != "" {
		where = append(where, taskStatus+"=?")
		args = append(args, f.Status)
	}
	if f.Query != "" {
		where = append(where, `instr(lower(COALESCE(NULLIF(t.title,''),c.title)),lower(?))>0`)
		args = append(args, f.Query)
	}
	if f.BeforeCreatedAt != "" || f.BeforeID != "" {
		if f.BeforeCreatedAt == "" || f.BeforeID == "" {
			return nil, false, errors.New("task cursor requires created_at and id")
		}
		where = append(where, "(r.created_at<? OR (r.created_at=? AND r.id<?))")
		args = append(args, f.BeforeCreatedAt, f.BeforeCreatedAt, f.BeforeID)
	}
	args = append(args, f.Limit+1)
	rows, err := s.db.QueryContext(ctx, "SELECT "+taskColumns+taskJoins+" WHERE "+strings.Join(where, " AND ")+" ORDER BY r.created_at DESC,r.id DESC LIMIT ?", args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := []domain.TaskSummary{}
	for rows.Next() {
		item, err := scanTask(rows)
		if err != nil {
			return nil, false, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(items) > f.Limit
	if hasMore {
		items = items[:f.Limit]
	}
	return items, hasMore, nil
}

func (s *Store) GetTask(ctx context.Context, runID string) (domain.TaskSummary, string, error) {
	item, err := scanTask(s.db.QueryRowContext(ctx, "SELECT "+taskColumns+taskJoins+" WHERE r.id=?", runID))
	if err != nil {
		return item, "", err
	}
	var result string
	err = s.db.QueryRowContext(ctx, `SELECT result FROM task_results WHERE run_id=?`, runID).Scan(&result)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return item, result, err
}

func (s *Store) SetTaskTitle(ctx context.Context, runID, title string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO task_results(run_id,title) VALUES(?,?) ON CONFLICT(run_id) DO UPDATE SET title=excluded.title`, runID, title)
	return err
}

func (s *Store) SaveTaskArtifact(ctx context.Context, runID, name, mediaType, preview string, content []byte) (domain.Artifact, error) {
	a := domain.Artifact{ID: id.New("artifact"), RunID: runID, Name: name, MediaType: mediaType, Size: int64(len(content)), CreatedAt: now(), PreviewKind: preview}
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) || strings.ContainsRune(name, '\x00') {
		return a, errors.New("artifact name must be a basename")
	}
	if len(content) > maxTaskArtifactSize {
		return a, errors.New("artifact exceeds 10 MiB")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO task_artifacts(id,run_id,name,media_type,size,created_at,preview_kind,content) SELECT ?,?,?,?,?,?,?,? WHERE (SELECT count(*) FROM task_artifacts WHERE run_id=?)<? ON CONFLICT(run_id,name) DO NOTHING`, a.ID, a.RunID, a.Name, a.MediaType, a.Size, a.CreatedAt, a.PreviewKind, content, runID, maxTaskArtifacts)
	if err != nil {
		return a, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return a, err
	}
	if inserted == 1 {
		return a, nil
	}
	var existing domain.Artifact
	err = s.db.QueryRowContext(ctx, `SELECT id,run_id,name,media_type,size,created_at,preview_kind FROM task_artifacts WHERE run_id=? AND name=?`, runID, name).Scan(&existing.ID, &existing.RunID, &existing.Name, &existing.MediaType, &existing.Size, &existing.CreatedAt, &existing.PreviewKind)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return a, err
	}
	return a, fmt.Errorf("%w: maximum is %d", ErrTaskArtifactLimit, maxTaskArtifacts)
}

func (s *Store) ListTaskArtifacts(ctx context.Context, runID string) ([]domain.Artifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,run_id,name,media_type,size,created_at,preview_kind FROM task_artifacts WHERE run_id=? ORDER BY created_at,id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.Artifact{}
	for rows.Next() {
		var a domain.Artifact
		if err := rows.Scan(&a.ID, &a.RunID, &a.Name, &a.MediaType, &a.Size, &a.CreatedAt, &a.PreviewKind); err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

func (s *Store) GetTaskArtifact(ctx context.Context, runID, artifactID string) (domain.Artifact, []byte, error) {
	var a domain.Artifact
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT id,run_id,name,media_type,size,created_at,preview_kind,content FROM task_artifacts WHERE run_id=? AND id=?`, runID, artifactID).Scan(&a.ID, &a.RunID, &a.Name, &a.MediaType, &a.Size, &a.CreatedAt, &a.PreviewKind, &data)
	return a, data, err
}
