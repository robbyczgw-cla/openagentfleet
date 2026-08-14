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
	moderncsqlite "modernc.org/sqlite"
)

var (
	// ErrRemotePairingGrantInvalid intentionally collapses all expected remote
	// claim failures so callers cannot distinguish unknown, expired, locked,
	// already-used, or incorrectly presented grants.
	ErrRemotePairingGrantInvalid = errors.New("remote pairing grant is invalid")
	ErrRemotePairingGrantMissing = errors.New("remote pairing grant not found")
)

const (
	DefaultRemotePairingTTL = 5 * time.Minute
	MaxRemotePairingTTL     = 5 * time.Minute
	maxRemotePairingFails   = 5
	remotePairingSecretSize = 32
)

// CreateRemotePairingGrant creates a short-lived public grant and returns its
// base64url-encoded 32-random-byte secret exactly once. Only the SHA-256 hash
// of that returned string is persisted.
func (s *Store) CreateRemotePairingGrant(ctx context.Context, scopeProfile string, ttl time.Duration) (domain.RemotePairingGrant, string, error) {
	scopeProfile = strings.ToLower(strings.TrimSpace(scopeProfile))
	if !validRemoteScopeProfile(scopeProfile) {
		return domain.RemotePairingGrant{}, "", errors.New("invalid remote pairing scope profile")
	}
	if ttl == 0 {
		ttl = DefaultRemotePairingTTL
	}
	if ttl < 0 || ttl > MaxRemotePairingTTL {
		return domain.RemotePairingGrant{}, "", fmt.Errorf("remote pairing TTL must be greater than zero and at most %s", MaxRemotePairingTTL)
	}

	grantID, err := newRemoteID("pair")
	if err != nil {
		return domain.RemotePairingGrant{}, "", fmt.Errorf("generate remote pairing grant id: %w", err)
	}
	var randomSecret [remotePairingSecretSize]byte
	if _, err := rand.Read(randomSecret[:]); err != nil {
		return domain.RemotePairingGrant{}, "", fmt.Errorf("generate remote pairing secret: %w", err)
	}
	rawSecret := base64.RawURLEncoding.EncodeToString(randomSecret[:])
	secretHash := sha256.Sum256([]byte(rawSecret))
	createdAt := time.Now().UTC()
	grant := domain.RemotePairingGrant{
		ID:           grantID,
		ScopeProfile: scopeProfile,
		Status:       domain.RemotePairingPending,
		CreatedAt:    createdAt.Format(time.RFC3339Nano),
		ExpiresAt:    createdAt.Add(ttl).Format(time.RFC3339Nano),
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO remote_pairing_grants
(id, secret_hash, scope_profile, status, failed_attempts, created_at, expires_at, claimed_at, claimed_device_id)
VALUES (?, ?, ?, ?, 0, ?, ?, '', NULL)`, grant.ID, secretHash[:], grant.ScopeProfile, grant.Status, grant.CreatedAt, grant.ExpiresAt); err != nil {
		return domain.RemotePairingGrant{}, "", fmt.Errorf("create remote pairing grant: %w", err)
	}
	return grant, rawSecret, nil
}

// ClaimRemotePairingGrant atomically consumes a valid grant and creates both
// the active device and its hashed alpha bearer credential. Expected claim
// failures always return ErrRemotePairingGrantInvalid.
func (s *Store) ClaimRemotePairingGrant(
	ctx context.Context,
	grantID string,
	rawSecret string,
	displayName string,
	platform string,
	rawBearer string,
	bearerExpiresAt time.Time,
) (domain.RemoteDevice, error) {
	grantID = strings.TrimSpace(grantID)
	displayName = strings.TrimSpace(displayName)
	platform = strings.ToLower(strings.TrimSpace(platform))
	bearerExpiresAt = bearerExpiresAt.UTC()
	if grantID == "" || validateRemoteDevice(displayName, platform, domain.RemoteScopeObserver) != nil || validateRemoteBearer(rawBearer) != nil || bearerExpiresAt.IsZero() || !bearerExpiresAt.After(time.Now().UTC()) {
		return domain.RemoteDevice{}, ErrRemotePairingGrantInvalid
	}

	deviceID, err := newRemoteID("device")
	if err != nil {
		return domain.RemoteDevice{}, fmt.Errorf("claim remote pairing grant: generate device id: %w", err)
	}
	wantedSecretHash := sha256.Sum256([]byte(rawSecret))
	bearerHash := sha256.Sum256([]byte(rawBearer))

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return domain.RemoteDevice{}, fmt.Errorf("claim remote pairing grant: acquire database connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return domain.RemoteDevice{}, fmt.Errorf("claim remote pairing grant: configure transaction: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return domain.RemoteDevice{}, fmt.Errorf("claim remote pairing grant: begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var (
		storedSecretHash []byte
		scopeProfile     string
		status           string
		failedAttempts   int
		expiresAtText    string
	)
	err = conn.QueryRowContext(ctx, `SELECT secret_hash, scope_profile, status, failed_attempts, expires_at
FROM remote_pairing_grants WHERE id = ?`, grantID).Scan(&storedSecretHash, &scopeProfile, &status, &failedAttempts, &expiresAtText)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RemoteDevice{}, ErrRemotePairingGrantInvalid
	}
	if err != nil {
		return domain.RemoteDevice{}, fmt.Errorf("claim remote pairing grant: load grant: %w", err)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expiresAtText)
	if err != nil {
		return domain.RemoteDevice{}, fmt.Errorf("claim remote pairing grant: parse grant expiry: %w", err)
	}
	if status != domain.RemotePairingPending {
		return domain.RemoteDevice{}, ErrRemotePairingGrantInvalid
	}
	if !expiresAt.After(time.Now().UTC()) {
		if _, err := conn.ExecContext(ctx, `UPDATE remote_pairing_grants SET status = ? WHERE id = ? AND status = ?`, domain.RemotePairingExpired, grantID, domain.RemotePairingPending); err != nil {
			return domain.RemoteDevice{}, fmt.Errorf("claim remote pairing grant: expire grant: %w", err)
		}
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return domain.RemoteDevice{}, fmt.Errorf("claim remote pairing grant: commit expiry: %w", err)
		}
		committed = true
		return domain.RemoteDevice{}, ErrRemotePairingGrantInvalid
	}

	secretMatches := len(storedSecretHash) == sha256.Size && subtle.ConstantTimeCompare(storedSecretHash, wantedSecretHash[:]) == 1
	if !secretMatches {
		failedAttempts++
		if failedAttempts > maxRemotePairingFails {
			failedAttempts = maxRemotePairingFails
		}
		nextStatus := domain.RemotePairingPending
		if failedAttempts == maxRemotePairingFails {
			nextStatus = domain.RemotePairingLocked
		}
		if _, err := conn.ExecContext(ctx, `UPDATE remote_pairing_grants
SET failed_attempts = ?, status = ? WHERE id = ? AND status = ?`, failedAttempts, nextStatus, grantID, domain.RemotePairingPending); err != nil {
			return domain.RemoteDevice{}, fmt.Errorf("claim remote pairing grant: record failed attempt: %w", err)
		}
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return domain.RemoteDevice{}, fmt.Errorf("claim remote pairing grant: commit failed attempt: %w", err)
		}
		committed = true
		return domain.RemoteDevice{}, ErrRemotePairingGrantInvalid
	}
	if !validRemoteScopeProfile(scopeProfile) {
		return domain.RemoteDevice{}, fmt.Errorf("claim remote pairing grant: stored scope profile is invalid")
	}

	claimedAt := time.Now().UTC().Format(time.RFC3339Nano)
	device := domain.RemoteDevice{
		ID:           deviceID,
		DisplayName:  displayName,
		Platform:     platform,
		ScopeProfile: scopeProfile,
		Status:       domain.RemoteDeviceActive,
		CreatedAt:    claimedAt,
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO remote_devices
(id, display_name, platform, scope_profile, status, created_at, revoked_at, last_used_at)
VALUES (?, ?, ?, ?, ?, ?, '', '')`, device.ID, device.DisplayName, device.Platform, device.ScopeProfile, device.Status, device.CreatedAt); err != nil {
		return domain.RemoteDevice{}, fmt.Errorf("claim remote pairing grant: create device: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO remote_credentials
(token_hash, device_id, expires_at, revoked) VALUES (?, ?, ?, 0)`, bearerHash[:], device.ID, bearerExpiresAt.Format(time.RFC3339Nano)); err != nil {
		if isSQLiteConstraintError(err) {
			return domain.RemoteDevice{}, ErrRemotePairingGrantInvalid
		}
		return domain.RemoteDevice{}, fmt.Errorf("claim remote pairing grant: create credential: %w", err)
	}
	result, err := conn.ExecContext(ctx, `UPDATE remote_pairing_grants
SET status = ?, claimed_at = ?, claimed_device_id = ?
WHERE id = ? AND status = ?`, domain.RemotePairingClaimed, claimedAt, device.ID, grantID, domain.RemotePairingPending)
	if err != nil {
		return domain.RemoteDevice{}, fmt.Errorf("claim remote pairing grant: consume grant: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return domain.RemoteDevice{}, fmt.Errorf("claim remote pairing grant: inspect consume result: %w", err)
	}
	if updated != 1 {
		return domain.RemoteDevice{}, ErrRemotePairingGrantInvalid
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return domain.RemoteDevice{}, fmt.Errorf("claim remote pairing grant: commit: %w", err)
	}
	committed = true
	return device, nil
}

// GetRemotePairingGrant returns only public metadata. It is intended for the
// local Mac pairing UI, not remote authentication decisions.
func (s *Store) GetRemotePairingGrant(ctx context.Context, grantID string) (domain.RemotePairingGrant, error) {
	grantID = strings.TrimSpace(grantID)
	if grantID == "" {
		return domain.RemotePairingGrant{}, ErrRemotePairingGrantMissing
	}
	var grant domain.RemotePairingGrant
	err := s.db.QueryRowContext(ctx, `SELECT id, scope_profile, status, created_at, expires_at
FROM remote_pairing_grants WHERE id = ?`, grantID).Scan(&grant.ID, &grant.ScopeProfile, &grant.Status, &grant.CreatedAt, &grant.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RemotePairingGrant{}, ErrRemotePairingGrantMissing
	}
	if err != nil {
		return domain.RemotePairingGrant{}, fmt.Errorf("get remote pairing grant: %w", err)
	}
	if grant.Status == domain.RemotePairingPending {
		expiresAt, err := time.Parse(time.RFC3339Nano, grant.ExpiresAt)
		if err != nil {
			return domain.RemotePairingGrant{}, fmt.Errorf("get remote pairing grant: parse expiry: %w", err)
		}
		if !expiresAt.After(time.Now().UTC()) {
			if _, err := s.db.ExecContext(ctx, `UPDATE remote_pairing_grants SET status = ? WHERE id = ? AND status = ?`, domain.RemotePairingExpired, grant.ID, domain.RemotePairingPending); err != nil {
				return domain.RemotePairingGrant{}, fmt.Errorf("get remote pairing grant: expire grant: %w", err)
			}
			grant.Status = domain.RemotePairingExpired
		}
	}
	return grant, nil
}

func validRemoteScopeProfile(scopeProfile string) bool {
	return scopeProfile == domain.RemoteScopeObserver || scopeProfile == domain.RemoteScopeController || scopeProfile == domain.RemoteScopeOwner
}

func isSQLiteConstraintError(err error) bool {
	var sqliteErr *moderncsqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 19
}
