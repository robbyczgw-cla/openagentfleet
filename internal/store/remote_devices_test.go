package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestRemoteHostIDIsStableAcrossCallsAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "botd.sqlite")
	instance, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := instance.GetOrCreateRemoteHostID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	again, err := instance.GetOrCreateRemoteHostID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hostID != again {
		t.Fatalf("host id changed within one store: %q != %q", hostID, again)
	}
	if !strings.HasPrefix(hostID, "host-") || len(hostID) != len("host-")+32 {
		t.Fatalf("unexpected host id format: %q", hostID)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.GetOrCreateRemoteHostID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if persisted != hostID {
		t.Fatalf("host id changed after reopen: %q != %q", persisted, hostID)
	}
}

func TestRemoteCredentialLifecycleStoresOnlyHashAndRevokesAll(t *testing.T) {
	ctx := context.Background()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()

	device, err := instance.CreateRemoteDevice(ctx, "  Robby's iPhone  ", "IOS", "Controller")
	if err != nil {
		t.Fatal(err)
	}
	if device.DisplayName != "Robby's iPhone" || device.Platform != domain.RemotePlatformIOS || device.ScopeProfile != domain.RemoteScopeController || device.Status != domain.RemoteDeviceActive {
		t.Fatalf("unexpected normalized device: %#v", device)
	}
	firstToken := "ofb_test_credential_0123456789abcdef"
	secondToken := "ofb_test_credential_fedcba9876543210"
	expiresAt := time.Now().UTC().Add(time.Hour)
	if err := instance.StoreRemoteCredential(ctx, device.ID, firstToken, expiresAt); err != nil {
		t.Fatal(err)
	}
	if err := instance.StoreRemoteCredential(ctx, device.ID, secondToken, expiresAt); err != nil {
		t.Fatal(err)
	}
	if err := instance.StoreRemoteCredential(ctx, device.ID, firstToken, expiresAt); !errors.Is(err, ErrRemoteCredentialExists) {
		t.Fatalf("duplicate credential error = %v, want %v", err, ErrRemoteCredentialExists)
	}

	wantedHash := sha256.Sum256([]byte(firstToken))
	var (
		storedType string
		storedHash []byte
	)
	if err := instance.db.QueryRowContext(ctx, "SELECT typeof(token_hash), token_hash FROM remote_credentials WHERE device_id = ? AND token_hash = ?", device.ID, wantedHash[:]).Scan(&storedType, &storedHash); err != nil {
		t.Fatal(err)
	}
	if storedType != "blob" || len(storedHash) != sha256.Size || !bytes.Equal(storedHash, wantedHash[:]) {
		t.Fatalf("credential was not stored as the expected fixed-size SHA-256 BLOB: type=%q len=%d", storedType, len(storedHash))
	}
	if bytes.Equal(storedHash, []byte(firstToken)) {
		t.Fatal("raw bearer credential was stored")
	}

	authenticated, err := instance.AuthenticateRemoteCredential(ctx, firstToken)
	if err != nil {
		t.Fatal(err)
	}
	if authenticated != device {
		t.Fatalf("authenticated device = %#v, want %#v", authenticated, device)
	}
	wrongToken := "ofb_test_credential_0123456789abcdeg"
	if _, err := instance.AuthenticateRemoteCredential(ctx, wrongToken); !errors.Is(err, ErrRemoteCredentialInvalid) {
		t.Fatalf("wrong credential error = %v, want %v", err, ErrRemoteCredentialInvalid)
	}

	if err := instance.TouchRemoteDeviceLastUsed(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	devices, err := instance.ListRemoteDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].ID != device.ID || devices[0].LastUsedAt == "" {
		t.Fatalf("unexpected device list after touch: %#v", devices)
	}
	if _, err := time.Parse(time.RFC3339Nano, devices[0].LastUsedAt); err != nil {
		t.Fatalf("invalid last-used timestamp %q: %v", devices[0].LastUsedAt, err)
	}
	assertRemoteJSONContainsNoCredentialMaterial(t, devices[0], firstToken, wantedHash)

	if err := instance.RevokeRemoteDevice(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	if err := instance.RevokeRemoteDevice(ctx, device.ID); err != nil {
		t.Fatalf("idempotent revoke failed: %v", err)
	}
	for _, token := range []string{firstToken, secondToken} {
		if _, err := instance.AuthenticateRemoteCredential(ctx, token); !errors.Is(err, ErrRemoteCredentialInvalid) {
			t.Fatalf("revoked credential %q error = %v, want %v", token, err, ErrRemoteCredentialInvalid)
		}
	}
	if err := instance.TouchRemoteDeviceLastUsed(ctx, device.ID); !errors.Is(err, ErrRemoteDeviceRevoked) {
		t.Fatalf("touch revoked device error = %v, want %v", err, ErrRemoteDeviceRevoked)
	}
	if err := instance.StoreRemoteCredential(ctx, device.ID, "ofb_test_credential_new_0123456789abcdef", expiresAt); !errors.Is(err, ErrRemoteDeviceRevoked) {
		t.Fatalf("store credential for revoked device error = %v, want %v", err, ErrRemoteDeviceRevoked)
	}
	var revokedCredentials int
	if err := instance.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM remote_credentials WHERE device_id = ? AND revoked = 1", device.ID).Scan(&revokedCredentials); err != nil {
		t.Fatal(err)
	}
	if revokedCredentials != 2 {
		t.Fatalf("revoked credential count = %d, want 2", revokedCredentials)
	}
	devices, err = instance.ListRemoteDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Status != domain.RemoteDeviceRevoked || devices[0].RevokedAt == "" {
		t.Fatalf("unexpected revoked device: %#v", devices)
	}
}

func TestRemoteDeviceCredentialPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "botd.sqlite")
	instance, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	device, err := instance.CreateRemoteDevice(ctx, "Robby's Pixel", domain.RemotePlatformAndroid, domain.RemoteScopeOwner)
	if err != nil {
		_ = instance.Close()
		t.Fatal(err)
	}
	token := "ofb_durable_credential_0123456789abcdef"
	if err := instance.StoreRemoteCredential(ctx, device.ID, token, time.Now().Add(time.Hour)); err != nil {
		_ = instance.Close()
		t.Fatal(err)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	authenticated, err := reopened.AuthenticateRemoteCredential(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if authenticated.ID != device.ID || authenticated.DisplayName != device.DisplayName || authenticated.ScopeProfile != domain.RemoteScopeOwner {
		t.Fatalf("authenticated persisted device = %#v, want %#v", authenticated, device)
	}
}

func TestRemoteCredentialExpiryAndValidation(t *testing.T) {
	ctx := context.Background()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()

	invalidDevices := []struct {
		name     string
		platform string
		scope    string
	}{
		{"", domain.RemotePlatformIOS, domain.RemoteScopeObserver},
		{"phone", "windows", domain.RemoteScopeObserver},
		{"phone", domain.RemotePlatformAndroid, "superuser"},
	}
	for _, input := range invalidDevices {
		if _, err := instance.CreateRemoteDevice(ctx, input.name, input.platform, input.scope); err == nil {
			t.Fatalf("CreateRemoteDevice(%q, %q, %q) unexpectedly succeeded", input.name, input.platform, input.scope)
		}
	}

	device, err := instance.CreateRemoteDevice(ctx, "Pixel", domain.RemotePlatformAndroid, domain.RemoteScopeObserver)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.StoreRemoteCredential(ctx, device.ID, "short", time.Now().Add(time.Hour)); err == nil {
		t.Fatal("short credential unexpectedly succeeded")
	}
	if err := instance.StoreRemoteCredential(ctx, device.ID, "ofb_test_credential_with whitespace_123456", time.Now().Add(time.Hour)); err == nil {
		t.Fatal("credential containing whitespace unexpectedly succeeded")
	}
	if err := instance.StoreRemoteCredential(ctx, "missing-device", "ofb_test_credential_0123456789abcdef", time.Now().Add(time.Hour)); !errors.Is(err, ErrRemoteDeviceNotFound) {
		t.Fatalf("missing device error = %v, want %v", err, ErrRemoteDeviceNotFound)
	}
	if err := instance.StoreRemoteCredential(ctx, device.ID, "ofb_test_credential_expired_12345678", time.Now().Add(-time.Minute)); err == nil {
		t.Fatal("already expired credential unexpectedly succeeded")
	}

	token := "ofb_test_credential_expiry_0123456789"
	if err := instance.StoreRemoteCredential(ctx, device.ID, token, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(token))
	if _, err := instance.db.ExecContext(ctx, "UPDATE remote_credentials SET expires_at = ? WHERE token_hash = ?", time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), hash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.AuthenticateRemoteCredential(ctx, token); !errors.Is(err, ErrRemoteCredentialInvalid) {
		t.Fatalf("expired credential error = %v, want %v", err, ErrRemoteCredentialInvalid)
	}
	if err := instance.RevokeRemoteDevice(ctx, "missing-device"); !errors.Is(err, ErrRemoteDeviceNotFound) {
		t.Fatalf("missing revoke error = %v, want %v", err, ErrRemoteDeviceNotFound)
	}
	if err := instance.TouchRemoteDeviceLastUsed(ctx, "missing-device"); !errors.Is(err, ErrRemoteDeviceNotFound) {
		t.Fatalf("missing touch error = %v, want %v", err, ErrRemoteDeviceNotFound)
	}
}

func TestRemoteSchemaMigratesExistingDatabaseWithoutDataLoss(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "existing.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE existing_marker (value TEXT NOT NULL); INSERT INTO existing_marker (value) VALUES ('preserved')"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	instance, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	var marker string
	if err := instance.db.QueryRowContext(ctx, "SELECT value FROM existing_marker").Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker != "preserved" {
		t.Fatalf("existing marker = %q, want preserved", marker)
	}
	if _, err := instance.GetOrCreateRemoteHostID(ctx); err != nil {
		t.Fatalf("new remote schema unavailable after migration: %v", err)
	}
}

func assertRemoteJSONContainsNoCredentialMaterial(t *testing.T, device domain.RemoteDevice, rawToken string, hash [sha256.Size]byte) {
	t.Helper()
	encodedDevice, err := json.Marshal(device)
	if err != nil {
		t.Fatal(err)
	}
	credential := domain.RemoteCredential{TokenHash: hash, DeviceID: device.ID, ExpiresAt: time.Now().UTC().Format(time.RFC3339Nano)}
	encodedCredential, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	for _, encoded := range [][]byte{encodedDevice, encodedCredential} {
		if bytes.Contains(encoded, []byte(rawToken)) || bytes.Contains(encoded, []byte(hex.EncodeToString(hash[:]))) || bytes.Contains(encoded, []byte("token_hash")) {
			t.Fatalf("credential material leaked through JSON: %s", encoded)
		}
	}
}
