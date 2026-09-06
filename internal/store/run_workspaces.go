package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
)

func (s *Store) migrateRunWorkspaces() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS run_workspaces (
		run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
		workdir TEXT NOT NULL
	)`)
	return err
}

// SetRunWorkdir binds a queued run to its prepared working directory.
func (s *Store) SetRunWorkdir(ctx context.Context, runID, workdir string) error {
	if !filepath.IsAbs(workdir) || filepath.Clean(workdir) != workdir {
		return errors.New("run working directory must be an absolute clean path")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO run_workspaces(run_id, workdir)
		SELECT id, ? FROM runs WHERE id = ? AND status = 'queued'
		ON CONFLICT(run_id) DO NOTHING`, workdir, runID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n == 1 {
		return nil
	}
	var existing, status string
	err = s.db.QueryRowContext(ctx, `SELECT w.workdir, r.status FROM run_workspaces w
		JOIN runs r ON r.id = w.run_id WHERE w.run_id = ?`, runID).Scan(&existing, &status)
	if err == nil && existing == workdir && status == "queued" {
		return nil
	}
	return fmt.Errorf("working directory can only be assigned once to a queued run")
}

func (s *Store) GetRunWorkdir(ctx context.Context, runID string) (string, error) {
	var workdir string
	err := s.db.QueryRowContext(ctx, `SELECT workdir FROM run_workspaces WHERE run_id = ?`, runID).Scan(&workdir)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return workdir, err
}
