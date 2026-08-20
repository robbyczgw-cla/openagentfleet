package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/robbyczgw-cla/openagentfleet/internal/domain"
)

func TestCreateRemotePairingGrantStoresHashOnlyAndPublicJSONCannotLeakSecret(t *testing.T) {
	ctx := context.Background()
	instance := openRemotePairingTestStore(t)
	grant, rawSecret, err := instance.CreateRemotePairingGrant(ctx, " Controller ", 0)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(rawSecret)
	if err != nil {
		t.Fatalf("pairing secret is not raw base64url: %v", err)
	}
	if len(decoded) != remotePairingSecretSize {
		t.Fatalf("pairing secret entropy bytes = %d, want %d", len(decoded), remotePairingSecretSize)
	}
	if grant.ScopeProfile != domain.RemoteScopeController || grant.Status != domain.RemotePairingPending {
		t.Fatalf("unexpected public grant: %#v", grant)
	}
	createdAt := mustParsePairingTime(t, grant.CreatedAt)
	expiresAt := mustParsePairingTime(t, grant.ExpiresAt)
	if ttl := expiresAt.Sub(createdAt); ttl != DefaultRemotePairingTTL {
		t.Fatalf("default TTL = %s, want %s", ttl, DefaultRemotePairingTTL)
	}

	wantedHash := sha256.Sum256([]byte(rawSecret))
	var storedType string
	var storedHash []byte
	if err := instance.db.QueryRowContext(ctx, `SELECT typeof(secret_hash), secret_hash FROM remote_pairing_grants WHERE id = ?`, grant.ID).Scan(&storedType, &storedHash); err != nil {
		t.Fatal(err)
	}
	if storedType != "blob" || !bytes.Equal(storedHash, wantedHash[:]) {
		t.Fatalf("pairing secret hash storage mismatch: type=%q len=%d", storedType, len(storedHash))
	}
	if bytes.Equal(storedHash, []byte(rawSecret)) {
		t.Fatal("raw pairing secret was persisted")
	}
	encoded, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{rawSecret, hex.EncodeToString(wantedHash[:]), "secret", "secret_hash", "failed_attempts"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("public grant JSON leaked %q: %s", forbidden, encoded)
		}
	}

	if _, _, err := instance.CreateRemotePairingGrant(ctx, domain.RemoteScopeObserver, -time.Second); err == nil {
		t.Fatal("negative pairing TTL unexpectedly succeeded")
	}
	if _, _, err := instance.CreateRemotePairingGrant(ctx, domain.RemoteScopeObserver, MaxRemotePairingTTL+time.Nanosecond); err == nil {
		t.Fatal("pairing TTL above the maximum unexpectedly succeeded")
	}
	if _, _, err := instance.CreateRemotePairingGrant(ctx, "superuser", time.Minute); err == nil {
		t.Fatal("invalid pairing scope unexpectedly succeeded")
	}
}

func TestClaimRemotePairingGrantDesktopCreatesDeviceAndStoresHashOnly(t *testing.T) {
	ctx := context.Background()
	instance := openRemotePairingTestStore(t)
	grant, rawSecret, err := instance.CreateRemotePairingGrant(ctx, domain.RemoteScopeController, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	bearer := "ofb_alpha_desktop_bearer_0123456789abcdef"
	device, err := instance.ClaimRemotePairingGrant(ctx, grant.ID, rawSecret, " Studio Laptop ", "DESKTOP", bearer, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if device.DisplayName != "Studio Laptop" || device.Platform != domain.RemotePlatformDesktop || device.ScopeProfile != domain.RemoteScopeController || device.Status != domain.RemoteDeviceActive {
		t.Fatalf("unexpected paired desktop: %#v", device)
	}
	wantedHash := sha256.Sum256([]byte(rawSecret))
	var storedSecret []byte
	if err := instance.db.QueryRowContext(ctx, "SELECT secret_hash FROM remote_pairing_grants WHERE id = ?", grant.ID).Scan(&storedSecret); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedSecret, wantedHash[:]) {
		t.Fatal("desktop claim did not keep the hashed pairing secret")
	}
	if bytes.Equal(storedSecret, []byte(rawSecret)) {
		t.Fatal("raw pairing secret was persisted for a desktop claim")
	}
	bearerHash := sha256.Sum256([]byte(bearer))
	var storedBearer []byte
	if err := instance.db.QueryRowContext(ctx, "SELECT token_hash FROM remote_credentials WHERE device_id = ?", device.ID).Scan(&storedBearer); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedBearer, bearerHash[:]) {
		t.Fatal("desktop bearer was not stored as a hash")
	}
	authenticated, err := instance.AuthenticateRemoteCredential(ctx, bearer)
	if err != nil {
		t.Fatal(err)
	}
	if authenticated.ID != device.ID || authenticated.Platform != domain.RemotePlatformDesktop {
		t.Fatalf("authenticated desktop = %#v", authenticated)
	}
	if _, err := instance.ClaimRemotePairingGrant(ctx, grant.ID, rawSecret, "Another laptop", domain.RemotePlatformDesktop, "ofb_alpha_desktop_bearer_fedcba9876543210", time.Now().Add(time.Hour)); err != ErrRemotePairingGrantInvalid {
		t.Fatalf("reused desktop grant error = %v, want exact invalid-grant sentinel", err)
	}
}

func TestClaimRemotePairingGrantCreatesDeviceCredentialAndRevocationStillWorks(t *testing.T) {
	ctx := context.Background()
	instance := openRemotePairingTestStore(t)
	grant, rawSecret, err := instance.CreateRemotePairingGrant(ctx, domain.RemoteScopeOwner, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	bearer := "ofb_alpha_pairing_bearer_0123456789abcdef"
	device, err := instance.ClaimRemotePairingGrant(ctx, grant.ID, rawSecret, " Robby's Pixel ", "ANDROID", bearer, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if device.DisplayName != "Robby's Pixel" || device.Platform != domain.RemotePlatformAndroid || device.ScopeProfile != domain.RemoteScopeOwner || device.Status != domain.RemoteDeviceActive {
		t.Fatalf("unexpected paired device: %#v", device)
	}
	publicGrant, err := instance.GetRemotePairingGrant(ctx, grant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if publicGrant.Status != domain.RemotePairingClaimed {
		t.Fatalf("claimed grant status = %q", publicGrant.Status)
	}
	authenticated, err := instance.AuthenticateRemoteCredential(ctx, bearer)
	if err != nil {
		t.Fatal(err)
	}
	if authenticated.ID != device.ID {
		t.Fatalf("authenticated device = %q, want %q", authenticated.ID, device.ID)
	}
	if err := instance.RevokeRemoteDevice(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.AuthenticateRemoteCredential(ctx, bearer); !errors.Is(err, ErrRemoteCredentialInvalid) {
		t.Fatalf("revoked issued bearer error = %v, want %v", err, ErrRemoteCredentialInvalid)
	}
	if _, err := instance.ClaimRemotePairingGrant(ctx, grant.ID, rawSecret, "Another phone", domain.RemotePlatformIOS, "ofb_alpha_pairing_bearer_fedcba9876543210", time.Now().Add(time.Hour)); err != ErrRemotePairingGrantInvalid {
		t.Fatalf("reused grant error = %v, want exact invalid-grant sentinel", err)
	}
}

func TestClaimRemotePairingGrantExpiryCollapsesAndCreatesNothing(t *testing.T) {
	ctx := context.Background()
	instance := openRemotePairingTestStore(t)
	grant, rawSecret, err := instance.CreateRemotePairingGrant(ctx, domain.RemoteScopeObserver, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	if _, err := instance.db.ExecContext(ctx, `UPDATE remote_pairing_grants SET expires_at = ? WHERE id = ?`, past, grant.ID); err != nil {
		t.Fatal(err)
	}
	err = claimPairingForTest(instance, grant.ID, rawSecret, "expired")
	if err != ErrRemotePairingGrantInvalid {
		t.Fatalf("expired claim error = %v, want exact invalid-grant sentinel", err)
	}
	assertPairingCounts(t, instance, 0, 0)
	var status string
	var failedAttempts int
	if err := instance.db.QueryRowContext(ctx, `SELECT status, failed_attempts FROM remote_pairing_grants WHERE id = ?`, grant.ID).Scan(&status, &failedAttempts); err != nil {
		t.Fatal(err)
	}
	if status != domain.RemotePairingExpired || failedAttempts != 0 {
		t.Fatalf("expired grant state = %q/%d", status, failedAttempts)
	}
}

func TestClaimRemotePairingGrantLocksAfterFiveFailures(t *testing.T) {
	ctx := context.Background()
	instance := openRemotePairingTestStore(t)
	grant, rawSecret, err := instance.CreateRemotePairingGrant(ctx, domain.RemoteScopeController, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= maxRemotePairingFails; attempt++ {
		wrongSecret := fmt.Sprintf("wrong-pairing-secret-%d", attempt)
		err := claimPairingForTest(instance, grant.ID, wrongSecret, fmt.Sprintf("failed-%d", attempt))
		if err != ErrRemotePairingGrantInvalid {
			t.Fatalf("attempt %d error = %v, want exact invalid-grant sentinel", attempt, err)
		}
		if strings.Contains(err.Error(), wrongSecret) || strings.Contains(err.Error(), rawSecret) {
			t.Fatalf("attempt %d error leaked a raw secret: %v", attempt, err)
		}
	}
	var status string
	var failedAttempts int
	if err := instance.db.QueryRowContext(ctx, `SELECT status, failed_attempts FROM remote_pairing_grants WHERE id = ?`, grant.ID).Scan(&status, &failedAttempts); err != nil {
		t.Fatal(err)
	}
	if status != domain.RemotePairingLocked || failedAttempts != maxRemotePairingFails {
		t.Fatalf("locked grant state = %q/%d, want locked/%d", status, failedAttempts, maxRemotePairingFails)
	}
	if err := claimPairingForTest(instance, grant.ID, rawSecret, "correct-after-lock"); err != ErrRemotePairingGrantInvalid {
		t.Fatalf("correct secret after lock error = %v", err)
	}
	assertPairingCounts(t, instance, 0, 0)
}

func TestClaimRemotePairingGrantRollsBackDeviceWhenCredentialCannotBeStored(t *testing.T) {
	ctx := context.Background()
	instance := openRemotePairingTestStore(t)
	duplicateBearer := "ofb_duplicate_alpha_bearer_0123456789abcdef"
	existing, err := instance.CreateRemoteDevice(ctx, "Existing phone", domain.RemotePlatformIOS, domain.RemoteScopeObserver)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.StoreRemoteCredential(ctx, existing.ID, duplicateBearer, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	grant, rawSecret, err := instance.CreateRemotePairingGrant(ctx, domain.RemoteScopeController, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = instance.ClaimRemotePairingGrant(ctx, grant.ID, rawSecret, "New phone", domain.RemotePlatformAndroid, duplicateBearer, time.Now().Add(time.Hour))
	if err != ErrRemotePairingGrantInvalid {
		t.Fatalf("duplicate bearer claim error = %v, want exact invalid-grant sentinel", err)
	}
	assertPairingCounts(t, instance, 1, 1)
	publicGrant, err := instance.GetRemotePairingGrant(ctx, grant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if publicGrant.Status != domain.RemotePairingPending {
		t.Fatalf("grant was partially consumed after rollback: %#v", publicGrant)
	}
	var claimedDevice any
	if err := instance.db.QueryRowContext(ctx, `SELECT claimed_device_id FROM remote_pairing_grants WHERE id = ?`, grant.ID).Scan(&claimedDevice); err != nil {
		t.Fatal(err)
	}
	if claimedDevice != nil {
		t.Fatalf("grant retained a partial device reference: %#v", claimedDevice)
	}
}

func TestConcurrentRemotePairingClaimsHaveExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	instance := openRemotePairingTestStore(t)
	grant, rawSecret, err := instance.CreateRemotePairingGrant(ctx, domain.RemoteScopeController, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	const contenders = 16
	start := make(chan struct{})
	errorsByWorker := make(chan error, contenders)
	var successes atomic.Int32
	var workers sync.WaitGroup
	for worker := 0; worker < contenders; worker++ {
		worker := worker
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			bearer := fmt.Sprintf("ofb_concurrent_alpha_bearer_%02d_0123456789abcdef", worker)
			_, err := instance.ClaimRemotePairingGrant(ctx, grant.ID, rawSecret, fmt.Sprintf("Phone %d", worker), domain.RemotePlatformIOS, bearer, time.Now().Add(time.Hour))
			if err == nil {
				successes.Add(1)
				return
			}
			errorsByWorker <- err
		}()
	}
	close(start)
	workers.Wait()
	close(errorsByWorker)
	if successes.Load() != 1 {
		t.Fatalf("successful concurrent claims = %d, want 1", successes.Load())
	}
	losers := 0
	for err := range errorsByWorker {
		losers++
		if err != ErrRemotePairingGrantInvalid {
			t.Fatalf("concurrent loser error = %v, want exact invalid-grant sentinel", err)
		}
	}
	if losers != contenders-1 {
		t.Fatalf("concurrent losers = %d, want %d", losers, contenders-1)
	}
	assertPairingCounts(t, instance, 1, 1)
}

func TestRemotePairingClaimValidationUsesOneNonDisclosingError(t *testing.T) {
	ctx := context.Background()
	instance := openRemotePairingTestStore(t)
	grant, rawSecret, err := instance.CreateRemotePairingGrant(ctx, domain.RemoteScopeObserver, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		id       string
		secret   string
		name     string
		platform string
		bearer   string
		expiry   time.Time
	}{
		{"missing-grant", rawSecret, "Phone", domain.RemotePlatformIOS, "ofb_validation_bearer_000_0123456789abcdef", time.Now().Add(time.Hour)},
		{grant.ID, "incorrect-secret", "Phone", domain.RemotePlatformIOS, "ofb_validation_bearer_001_0123456789abcdef", time.Now().Add(time.Hour)},
		{grant.ID, rawSecret, "", domain.RemotePlatformIOS, "ofb_validation_bearer_002_0123456789abcdef", time.Now().Add(time.Hour)},
		{grant.ID, rawSecret, "Phone", "windows", "ofb_validation_bearer_003_0123456789abcdef", time.Now().Add(time.Hour)},
		{grant.ID, rawSecret, "Phone", domain.RemotePlatformIOS, "short", time.Now().Add(time.Hour)},
		{grant.ID, rawSecret, "Phone", domain.RemotePlatformIOS, "ofb_validation_bearer_004_0123456789abcdef", time.Now().Add(-time.Hour)},
	}
	for index, testCase := range cases {
		_, err := instance.ClaimRemotePairingGrant(ctx, testCase.id, testCase.secret, testCase.name, testCase.platform, testCase.bearer, testCase.expiry)
		if err != ErrRemotePairingGrantInvalid {
			t.Fatalf("case %d error = %v, want exact invalid-grant sentinel", index, err)
		}
		for _, raw := range []string{testCase.secret, rawSecret, testCase.bearer} {
			if raw != "" && strings.Contains(err.Error(), raw) {
				t.Fatalf("case %d error leaked raw input %q: %v", index, raw, err)
			}
		}
	}
}

func openRemotePairingTestStore(t *testing.T) *Store {
	t.Helper()
	instance, err := Open(filepath.Join(t.TempDir(), "botd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	return instance
}

func claimPairingForTest(instance *Store, grantID, rawSecret, suffix string) error {
	_, err := instance.ClaimRemotePairingGrant(
		context.Background(),
		grantID,
		rawSecret,
		"Test phone",
		domain.RemotePlatformIOS,
		"ofb_pairing_test_bearer_"+suffix+"_0123456789abcdef",
		time.Now().Add(time.Hour),
	)
	return err
}

func assertPairingCounts(t *testing.T, instance *Store, wantDevices, wantCredentials int) {
	t.Helper()
	for table, want := range map[string]int{"remote_devices": wantDevices, "remote_credentials": wantCredentials} {
		var got int
		if err := instance.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s count = %d, want %d", table, got, want)
		}
	}
}

func mustParsePairingTime(t *testing.T, raw string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatalf("parse time %q: %v", raw, err)
	}
	return parsed
}
