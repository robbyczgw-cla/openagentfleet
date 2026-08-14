package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/preferences"
)

const preferencesSingleton = 1

func (s *Store) ensurePreferencesTable(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS local_preferences (
  singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
  schema_version INTEGER NOT NULL,
  document TEXT NOT NULL,
  updated_at TEXT NOT NULL
)`)
	if err != nil {
		return fmt.Errorf("create local preferences table: %w", err)
	}
	return nil
}

// GetPreferences returns the one versioned local preferences document. On a
// fresh database it persists the privacy-preserving defaults before returning.
// A malformed existing document is rejected rather than normalized silently.
func (s *Store) GetPreferences(ctx context.Context) (preferences.Preferences, error) {
	if err := s.ensurePreferencesTable(ctx); err != nil {
		return preferences.Defaults(), err
	}
	for attempts := 0; attempts < 2; attempts++ {
		var raw string
		var version int
		err := s.db.QueryRowContext(ctx, "SELECT schema_version, document FROM local_preferences WHERE singleton = ?", preferencesSingleton).Scan(&version, &raw)
		if err == nil {
			value, decodeErr := preferences.Decode([]byte(raw))
			if decodeErr != nil {
				return preferences.Defaults(), fmt.Errorf("load local preferences: %w", decodeErr)
			}
			if version != value.Version {
				return preferences.Defaults(), fmt.Errorf("load local preferences: schema version %d does not match document version %d", version, value.Version)
			}
			return value, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return preferences.Defaults(), fmt.Errorf("load local preferences: %w", err)
		}
		encoded, encodeErr := preferences.Encode(preferences.Defaults())
		if encodeErr != nil {
			return preferences.Defaults(), encodeErr
		}
		if _, insertErr := s.db.ExecContext(ctx, `INSERT INTO local_preferences (singleton, schema_version, document, updated_at)
VALUES (?, ?, ?, ?) ON CONFLICT(singleton) DO NOTHING`, preferencesSingleton, preferences.CurrentVersion, string(encoded), time.Now().UTC().Format(time.RFC3339Nano)); insertErr != nil {
			return preferences.Defaults(), fmt.Errorf("initialize local preferences: %w", insertErr)
		}
	}
	return preferences.Defaults(), errors.New("initialize local preferences: document remained unavailable")
}

// SavePreferences strictly validates and atomically replaces the singleton
// document. It is useful for migrations; HTTP callers should normally use the
// compare-and-swap PatchPreferences method.
func (s *Store) SavePreferences(ctx context.Context, value preferences.Preferences) (preferences.Preferences, error) {
	encoded, err := preferences.Encode(value)
	if err != nil {
		return preferences.Defaults(), err
	}
	canonical, err := preferences.Decode(encoded)
	if err != nil {
		return preferences.Defaults(), err
	}
	if err := s.ensurePreferencesTable(ctx); err != nil {
		return preferences.Defaults(), err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO local_preferences (singleton, schema_version, document, updated_at)
VALUES (?, ?, ?, ?) ON CONFLICT(singleton) DO UPDATE SET
  schema_version = excluded.schema_version,
  document = excluded.document,
  updated_at = excluded.updated_at`, preferencesSingleton, canonical.Version, string(encoded), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return preferences.Defaults(), fmt.Errorf("save local preferences: %w", err)
	}
	return canonical, nil
}

// PatchPreferences applies a strict restricted patch with optimistic locking.
// Concurrent writers cannot silently overwrite each other: a stale read is
// retried against the newly persisted document.
func (s *Store) PatchPreferences(ctx context.Context, patch []byte) (preferences.Preferences, error) {
	if _, err := preferences.DecodePatch(patch); err != nil {
		return preferences.Defaults(), err
	}
	if err := s.ensurePreferencesTable(ctx); err != nil {
		return preferences.Defaults(), err
	}
	if _, err := s.GetPreferences(ctx); err != nil {
		return preferences.Defaults(), err
	}
	for attempts := 0; attempts < 8; attempts++ {
		var raw string
		if err := s.db.QueryRowContext(ctx, "SELECT document FROM local_preferences WHERE singleton = ?", preferencesSingleton).Scan(&raw); err != nil {
			return preferences.Defaults(), fmt.Errorf("load local preferences for patch: %w", err)
		}
		current, err := preferences.Decode([]byte(raw))
		if err != nil {
			return preferences.Defaults(), fmt.Errorf("load local preferences for patch: %w", err)
		}
		updated, err := preferences.MergePatch(current, patch)
		if err != nil {
			return current, err
		}
		encoded, err := preferences.Encode(updated)
		if err != nil {
			return current, err
		}
		result, err := s.db.ExecContext(ctx, `UPDATE local_preferences
SET schema_version = ?, document = ?, updated_at = ?
WHERE singleton = ? AND document = ?`, updated.Version, string(encoded), time.Now().UTC().Format(time.RFC3339Nano), preferencesSingleton, raw)
		if err != nil {
			return current, fmt.Errorf("patch local preferences: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return current, fmt.Errorf("patch local preferences: %w", err)
		}
		if changed == 1 {
			return updated, nil
		}
	}
	return preferences.Defaults(), errors.New("patch local preferences: concurrent update did not settle")
}
