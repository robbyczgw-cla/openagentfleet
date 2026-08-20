package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

var (
	ErrRemoteDeviceNotFound    = errors.New("remote device not found")
	ErrRemoteDeviceRevoked     = errors.New("remote device is revoked")
	ErrRemoteCredentialInvalid = errors.New("remote credential is invalid")
	ErrRemoteCredentialExists  = errors.New("remote credential already exists")
)

const (
	remoteHostSingleton    = 1
	maxRemoteDisplayName   = 128
	minRemoteCredentialLen = 32
	maxRemoteCredentialLen = 4096
)

// GetOrCreateRemoteHostID returns one durable, random identity for this store.
// Concurrent callers may generate different candidates, but SQLite persists
// only the first one and every caller reads back that same value.
func (s *Store) GetOrCreateRemoteHostID(ctx context.Context) (string, error) {
	candidate, err := newRemoteID("host")
	if err != nil {
		return "", fmt.Errorf("generate remote host id: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO remote_host_identity (singleton, host_id, created_at)
VALUES (?, ?, ?) ON CONFLICT(singleton) DO NOTHING`, remoteHostSingleton, candidate, now()); err != nil {
		return "", fmt.Errorf("persist remote host id: %w", err)
	}
	var hostID string
	if err := s.db.QueryRowContext(ctx, "SELECT host_id FROM remote_host_identity WHERE singleton = ?", remoteHostSingleton).Scan(&hostID); err != nil {
		return "", fmt.Errorf("load remote host id: %w", err)
	}
	if hostID == "" {
		return "", errors.New("load remote host id: stored id is empty")
	}
	return hostID, nil
}

// CreateRemoteDevice persists an active paired-device identity. Pairing codes
// and bearer credentials are intentionally handled separately.
func (s *Store) CreateRemoteDevice(ctx context.Context, displayName, platform, scopeProfile string) (domain.RemoteDevice, error) {
	displayName = strings.TrimSpace(displayName)
	platform = strings.ToLower(strings.TrimSpace(platform))
	scopeProfile = strings.ToLower(strings.TrimSpace(scopeProfile))
	if err := validateRemoteDevice(displayName, platform, scopeProfile); err != nil {
		return domain.RemoteDevice{}, err
	}
	deviceID, err := newRemoteID("device")
	if err != nil {
		return domain.RemoteDevice{}, fmt.Errorf("generate remote device id: %w", err)
	}
	item := domain.RemoteDevice{
		ID:           deviceID,
		DisplayName:  displayName,
		Platform:     platform,
		ScopeProfile: scopeProfile,
		Status:       domain.RemoteDeviceActive,
		CreatedAt:    now(),
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO remote_devices
(id, display_name, platform, scope_profile, status, created_at, revoked_at, last_used_at)
VALUES (?, ?, ?, ?, ?, ?, '', '')`, item.ID, item.DisplayName, item.Platform, item.ScopeProfile, item.Status, item.CreatedAt); err != nil {
		return domain.RemoteDevice{}, fmt.Errorf("create remote device: %w", err)
	}
	return item, nil
}

// StoreRemoteCredential hashes a raw bearer credential before persistence.
// The raw value is used only for this call and is never returned or stored.
func (s *Store) StoreRemoteCredential(ctx context.Context, deviceID, rawBearer string, expiresAt time.Time) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return errors.New("remote device id is required")
	}
	if err := validateRemoteBearer(rawBearer); err != nil {
		return err
	}
	if expiresAt.IsZero() {
		return errors.New("remote credential expiry is required")
	}
	expiresAt = expiresAt.UTC()
	if !expiresAt.After(time.Now().UTC()) {
		return errors.New("remote credential expiry must be in the future")
	}
	hash := sha256.Sum256([]byte(rawBearer))
	result, err := s.db.ExecContext(ctx, `INSERT INTO remote_credentials (token_hash, device_id, expires_at, revoked)
SELECT ?, id, ?, 0 FROM remote_devices WHERE id = ? AND status = ?
ON CONFLICT(token_hash) DO NOTHING`, hash[:], expiresAt.Format(time.RFC3339Nano), deviceID, domain.RemoteDeviceActive)
	if err != nil {
		return fmt.Errorf("store remote credential: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store remote credential: %w", err)
	}
	if inserted == 0 {
		var status string
		err := s.db.QueryRowContext(ctx, "SELECT status FROM remote_devices WHERE id = ?", deviceID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRemoteDeviceNotFound
		}
		if err != nil {
			return fmt.Errorf("load remote device after credential insert: %w", err)
		}
		if status != domain.RemoteDeviceActive {
			return ErrRemoteDeviceRevoked
		}
		return ErrRemoteCredentialExists
	}
	return nil
}

// AuthenticateRemoteCredential resolves an active, unexpired credential to
// its public device identity. All ordinary authentication failures collapse to
// ErrRemoteCredentialInvalid so callers do not disclose credential state.
func (s *Store) AuthenticateRemoteCredential(ctx context.Context, rawBearer string) (domain.RemoteDevice, error) {
	if err := validateRemoteBearer(rawBearer); err != nil {
		return domain.RemoteDevice{}, ErrRemoteCredentialInvalid
	}
	wantedHash := sha256.Sum256([]byte(rawBearer))
	var (
		storedHash []byte
		expiresAt  string
		revoked    int
		device     domain.RemoteDevice
	)
	err := s.db.QueryRowContext(ctx, `SELECT c.token_hash, c.expires_at, c.revoked,
d.id, d.display_name, d.platform, d.scope_profile, d.status, d.created_at, d.revoked_at, d.last_used_at
FROM remote_credentials c
JOIN remote_devices d ON d.id = c.device_id
WHERE c.token_hash = ?`, wantedHash[:]).Scan(
		&storedHash, &expiresAt, &revoked,
		&device.ID, &device.DisplayName, &device.Platform, &device.ScopeProfile,
		&device.Status, &device.CreatedAt, &device.RevokedAt, &device.LastUsedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RemoteDevice{}, ErrRemoteCredentialInvalid
	}
	if err != nil {
		return domain.RemoteDevice{}, fmt.Errorf("authenticate remote credential: %w", err)
	}
	if len(storedHash) != sha256.Size || subtle.ConstantTimeCompare(storedHash, wantedHash[:]) != 1 {
		return domain.RemoteDevice{}, ErrRemoteCredentialInvalid
	}
	expiry, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return domain.RemoteDevice{}, fmt.Errorf("authenticate remote credential: parse expiry: %w", err)
	}
	if revoked != 0 || device.Status != domain.RemoteDeviceActive || !expiry.After(time.Now().UTC()) {
		return domain.RemoteDevice{}, ErrRemoteCredentialInvalid
	}
	return device, nil
}

// ListRemoteDevices returns public device records only; credential hashes are
// neither selected nor represented by the return type.
func (s *Store) ListRemoteDevices(ctx context.Context) ([]domain.RemoteDevice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, display_name, platform, scope_profile, status, created_at, revoked_at, last_used_at
FROM remote_devices ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list remote devices: %w", err)
	}
	defer rows.Close()
	items := make([]domain.RemoteDevice, 0)
	for rows.Next() {
		var item domain.RemoteDevice
		if err := rows.Scan(&item.ID, &item.DisplayName, &item.Platform, &item.ScopeProfile, &item.Status, &item.CreatedAt, &item.RevokedAt, &item.LastUsedAt); err != nil {
			return nil, fmt.Errorf("scan remote device: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate remote devices: %w", err)
	}
	return items, nil
}

// RevokeRemoteDevice atomically marks the device and all of its credentials as
// revoked. Repeating a revoke is safe; an unknown device remains an error.
func (s *Store) RevokeRemoteDevice(ctx context.Context, deviceID string) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return errors.New("remote device id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin remote device revocation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE remote_devices
SET status = ?, revoked_at = CASE WHEN revoked_at = '' THEN ? ELSE revoked_at END
WHERE id = ?`, domain.RemoteDeviceRevoked, now(), deviceID)
	if err != nil {
		return fmt.Errorf("revoke remote device: %w", err)
	}
	matched, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke remote device: %w", err)
	}
	if matched == 0 {
		return ErrRemoteDeviceNotFound
	}
	if _, err := tx.ExecContext(ctx, "UPDATE remote_credentials SET revoked = 1 WHERE device_id = ?", deviceID); err != nil {
		return fmt.Errorf("revoke remote credentials: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit remote device revocation: %w", err)
	}
	return nil
}

// TouchRemoteDeviceLastUsed records successful use without reactivating a
// revoked device.
func (s *Store) TouchRemoteDeviceLastUsed(ctx context.Context, deviceID string) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return errors.New("remote device id is required")
	}
	result, err := s.db.ExecContext(ctx, "UPDATE remote_devices SET last_used_at = ? WHERE id = ? AND status = ?", now(), deviceID, domain.RemoteDeviceActive)
	if err != nil {
		return fmt.Errorf("touch remote device: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("touch remote device: %w", err)
	}
	if updated != 0 {
		return nil
	}
	var status string
	err = s.db.QueryRowContext(ctx, "SELECT status FROM remote_devices WHERE id = ?", deviceID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrRemoteDeviceNotFound
	}
	if err != nil {
		return fmt.Errorf("load remote device after touch: %w", err)
	}
	return ErrRemoteDeviceRevoked
}

func validateRemoteDevice(displayName, platform, scopeProfile string) error {
	if displayName == "" {
		return errors.New("remote device display name is required")
	}
	if len([]rune(displayName)) > maxRemoteDisplayName {
		return fmt.Errorf("remote device display name must be at most %d characters", maxRemoteDisplayName)
	}
	if !domain.ValidRemotePlatform(platform) {
		return fmt.Errorf("unsupported remote device platform %q", platform)
	}
	if scopeProfile != domain.RemoteScopeObserver && scopeProfile != domain.RemoteScopeController && scopeProfile != domain.RemoteScopeOwner {
		return fmt.Errorf("unsupported remote device scope profile %q", scopeProfile)
	}
	return nil
}

func validateRemoteBearer(rawBearer string) error {
	if len(rawBearer) < minRemoteCredentialLen {
		return fmt.Errorf("remote credential must be at least %d bytes", minRemoteCredentialLen)
	}
	if len(rawBearer) > maxRemoteCredentialLen {
		return fmt.Errorf("remote credential must be at most %d bytes", maxRemoteCredentialLen)
	}
	if strings.IndexFunc(rawBearer, unicode.IsSpace) >= 0 || strings.IndexFunc(rawBearer, unicode.IsControl) >= 0 {
		return errors.New("remote credential must not contain whitespace or control characters")
	}
	return nil
}

func newRemoteID(prefix string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%x", prefix, random), nil
}
