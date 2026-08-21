package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

const routineWebhookSecretBytes = 32

var ErrRoutineWebhookInvalid = errors.New("routine webhook credential is invalid")

// MigrateRoutineWebhooks is additive and idempotent. Only the SHA-256 of the
// delivery secret is stored; the raw secret is returned once on create/rotate.
func (s *Store) MigrateRoutineWebhooks(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS routine_webhooks (
  routine_id TEXT PRIMARY KEY REFERENCES routine_schedules(id) ON DELETE CASCADE,
  secret_hash BLOB NOT NULL CHECK(length(secret_hash) = 32),
  created_at TEXT NOT NULL,
  last_used_at TEXT NOT NULL DEFAULT ''
);`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate routine webhooks: %w", err)
	}
	return nil
}

func (s *Store) GetRoutineWebhook(ctx context.Context, routineID string) (domain.RoutineWebhook, error) {
	routineID = strings.TrimSpace(routineID)
	if routineID == "" {
		return domain.RoutineWebhook{}, ErrRoutineNotFound
	}
	if _, err := s.GetRoutine(ctx, routineID); err != nil {
		return domain.RoutineWebhook{}, err
	}
	var createdAt, lastUsedAt string
	err := s.db.QueryRowContext(ctx, `SELECT created_at, last_used_at FROM routine_webhooks WHERE routine_id = ?`, routineID).Scan(&createdAt, &lastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RoutineWebhook{RoutineID: routineID}, nil
	}
	if err != nil {
		return domain.RoutineWebhook{}, fmt.Errorf("get routine webhook: %w", err)
	}
	return domain.RoutineWebhook{
		RoutineID:  routineID,
		Configured: true,
		CreatedAt:  createdAt,
		LastUsedAt: lastUsedAt,
	}, nil
}

func (s *Store) RotateRoutineWebhook(ctx context.Context, routineID string) (domain.RoutineWebhook, string, error) {
	routineID = strings.TrimSpace(routineID)
	if routineID == "" {
		return domain.RoutineWebhook{}, "", ErrRoutineNotFound
	}
	if _, err := s.GetRoutine(ctx, routineID); err != nil {
		return domain.RoutineWebhook{}, "", err
	}
	var random [routineWebhookSecretBytes]byte
	if _, err := rand.Read(random[:]); err != nil {
		return domain.RoutineWebhook{}, "", fmt.Errorf("generate routine webhook secret: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(random[:])
	hash := sha256.Sum256([]byte(secret))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO routine_webhooks (routine_id, secret_hash, created_at, last_used_at)
VALUES (?, ?, ?, '')
ON CONFLICT(routine_id) DO UPDATE SET secret_hash = excluded.secret_hash, created_at = excluded.created_at, last_used_at = ''`,
		routineID, hash[:], now); err != nil {
		return domain.RoutineWebhook{}, "", fmt.Errorf("rotate routine webhook: %w", err)
	}
	return domain.RoutineWebhook{RoutineID: routineID, Configured: true, CreatedAt: now}, secret, nil
}

func (s *Store) RevokeRoutineWebhook(ctx context.Context, routineID string) error {
	routineID = strings.TrimSpace(routineID)
	if routineID == "" {
		return ErrRoutineNotFound
	}
	if _, err := s.GetRoutine(ctx, routineID); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM routine_webhooks WHERE routine_id = ?`, routineID)
	if err != nil {
		return fmt.Errorf("revoke routine webhook: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke routine webhook: %w", err)
	}
	if changed == 0 {
		return ErrRoutineWebhookInvalid
	}
	return nil
}

func (s *Store) AuthenticateRoutineWebhook(ctx context.Context, routineID, secret string) error {
	routineID = strings.TrimSpace(routineID)
	secret = strings.TrimSpace(secret)
	if routineID == "" || secret == "" {
		return ErrRoutineWebhookInvalid
	}
	var stored []byte
	err := s.db.QueryRowContext(ctx, `SELECT secret_hash FROM routine_webhooks WHERE routine_id = ?`, routineID).Scan(&stored)
	if err != nil {
		return ErrRoutineWebhookInvalid
	}
	sum := sha256.Sum256([]byte(secret))
	if len(stored) != sha256.Size || subtle.ConstantTimeCompare(stored, sum[:]) != 1 {
		return ErrRoutineWebhookInvalid
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `UPDATE routine_webhooks SET last_used_at = ? WHERE routine_id = ?`, now, routineID); err != nil {
		return fmt.Errorf("touch routine webhook: %w", err)
	}
	return nil
}

// ClaimWebhookRoutineRun starts a leased occurrence without requiring the
// routine to be due. It still requires the routine to be enabled.
func (s *Store) ClaimWebhookRoutineRun(ctx context.Context, claim domain.RoutineClaim) (domain.RoutineRun, error) {
	claim.Trigger = domain.RoutineTriggerWebhook
	return s.ClaimRoutineRun(ctx, claim)
}
