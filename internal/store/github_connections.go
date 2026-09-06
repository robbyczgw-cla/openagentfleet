package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type GitHubConnection struct {
	Enabled      bool     `json:"enabled"`
	Login        string   `json:"login"`
	Repositories []string `json:"repositories"`
	AgentIDs     []string `json:"agent_ids"`
}

func (s *Store) InitGitHubConnections(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS github_connection (singleton INTEGER PRIMARY KEY CHECK(singleton=1), document TEXT NOT NULL)`)
	return err
}

func (s *Store) GetGitHubConnection(ctx context.Context) (GitHubConnection, error) {
	value := GitHubConnection{Repositories: []string{}, AgentIDs: []string{}}
	if err := s.InitGitHubConnections(ctx); err != nil {
		return value, err
	}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT document FROM github_connection WHERE singleton=1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return value, nil
	}
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return value, err
	}
	if value.Repositories == nil {
		value.Repositories = []string{}
	}
	if value.AgentIDs == nil {
		value.AgentIDs = []string{}
	}
	return value, nil
}

func (s *Store) SaveGitHubConnection(ctx context.Context, value GitHubConnection) error {
	if err := s.InitGitHubConnections(ctx); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO github_connection(singleton,document) VALUES(1,?) ON CONFLICT(singleton) DO UPDATE SET document=excluded.document`, string(raw))
	return err
}
