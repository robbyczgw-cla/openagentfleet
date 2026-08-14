package secrethandoff

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newTestManager(t *testing.T, clock *fakeClock) *Manager {
	t.Helper()
	m, err := New(Config{TTL: 30 * time.Second, Clock: clock.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func createTestRequest(t *testing.T, m *Manager) Request {
	t.Helper()
	r, err := m.Create(CreateRequest{
		RunID:          "run-123",
		ConversationID: "conversation-456",
		Surface:        "computer:desktop-1",
		ComputerID:     "computer-789",
		TargetID:       "target-012",
		Purpose:        PurposePassword,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return r
}

func claimRequest(r Request) ClaimRequest {
	return ClaimRequest{
		ID:             r.ID,
		RunID:          r.RunID,
		ConversationID: r.ConversationID,
		Surface:        r.Surface,
		ComputerID:     r.ComputerID,
		TargetID:       r.TargetID,
		Purpose:        r.Purpose,
	}
}

func TestClaimIsSingleUseAndWipesStoredAndSubmittedBytes(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	m := newTestManager(t, clock)
	r := createTestRequest(t, m)
	input := []byte("hunter2-secret")
	if err := m.Submit(r.ID, input); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !allZero(input) {
		t.Fatal("Submit did not wipe caller input")
	}
	stored := m.entries[r.ID].secret
	claimed, err := m.Claim(claimRequest(r))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got := string(claimed); got != "hunter2-secret" {
		t.Fatalf("Claim = %q", got)
	}
	if !allZero(stored) || m.entries[r.ID].secret != nil {
		t.Fatal("Claim did not immediately wipe and detach stored bytes")
	}
	if _, err := m.Claim(claimRequest(r)); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second Claim error = %v, want ErrNotPending", err)
	}
	status, err := m.Get(r.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status.Status != StatusClaimed || status.Ready {
		t.Fatalf("status after Claim = %+v", status)
	}
	Wipe(claimed)
}

func TestConcurrentClaimHasExactlyOneWinner(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	m := newTestManager(t, clock)
	r := createTestRequest(t, m)
	if err := m.Submit(r.ID, []byte("one-time-code")); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	var successes atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			secret, err := m.Claim(claimRequest(r))
			if err == nil {
				successes.Add(1)
				if string(secret) != "one-time-code" {
					t.Errorf("winner received unexpected secret")
				}
				Wipe(secret)
				return
			}
			if !errors.Is(err, ErrNotPending) {
				t.Errorf("loser error = %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful claims = %d, want 1", got)
	}
}

func TestExpiryWipesSecret(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	m := newTestManager(t, clock)
	r := createTestRequest(t, m)
	if err := m.Submit(r.ID, []byte("expiring-secret")); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	stored := m.entries[r.ID].secret
	clock.Advance(30 * time.Second)

	status, err := m.Get(r.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status.Status != StatusExpired || status.Ready {
		t.Fatalf("expired status = %+v", status)
	}
	if !allZero(stored) || m.entries[r.ID].secret != nil {
		t.Fatal("expiry did not wipe and detach stored bytes")
	}
	if _, err := m.Claim(claimRequest(r)); !errors.Is(err, ErrNotPending) {
		t.Fatalf("Claim expired error = %v", err)
	}
}

func TestClaimRequiresExactBindingWithoutConsumingSecret(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	m := newTestManager(t, clock)
	r := createTestRequest(t, m)
	if err := m.Submit(r.ID, []byte("bound-secret")); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	wrong := claimRequest(r)
	wrong.TargetID = "another-target"
	if _, err := m.Claim(wrong); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("wrong binding Claim error = %v", err)
	}
	status, err := m.Get(r.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if status.Status != StatusPending || !status.Ready {
		t.Fatalf("wrong binding consumed request: %+v", status)
	}

	secret, err := m.Claim(claimRequest(r))
	if err != nil {
		t.Fatalf("correct binding Claim: %v", err)
	}
	if string(secret) != "bound-secret" {
		t.Fatal("correct binding returned wrong value")
	}
	Wipe(secret)
}

func TestCancelAndCloseWipeSecrets(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	m := newTestManager(t, clock)
	r1 := createTestRequest(t, m)
	r2 := createTestRequest(t, m)
	_ = m.Submit(r1.ID, []byte("cancel-me"))
	_ = m.Submit(r2.ID, []byte("close-me"))
	first := m.entries[r1.ID].secret
	second := m.entries[r2.ID].secret

	status, err := m.Cancel(r1.ID)
	if err != nil || status.Status != StatusCancelled {
		t.Fatalf("Cancel = %+v, %v", status, err)
	}
	if !allZero(first) {
		t.Fatal("Cancel did not wipe secret")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !allZero(second) {
		t.Fatal("Close did not wipe secret")
	}
	if _, err := m.Get(r2.ID); !errors.Is(err, ErrClosed) {
		t.Fatalf("Get after Close = %v", err)
	}
}

func TestCancelPendingWipesEveryReadyValueAndPreservesClaimedHistory(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	m := newTestManager(t, clock)
	first := createTestRequest(t, m)
	second := createTestRequest(t, m)
	claimed := createTestRequest(t, m)
	if err := m.Submit(first.ID, []byte("first-pending-secret")); err != nil {
		t.Fatal(err)
	}
	if err := m.Submit(second.ID, []byte("second-pending-secret")); err != nil {
		t.Fatal(err)
	}
	if err := m.Submit(claimed.ID, []byte("claimed-secret")); err != nil {
		t.Fatal(err)
	}
	claimedValue, err := m.Claim(claimRequest(claimed))
	if err != nil {
		t.Fatal(err)
	}
	Wipe(claimedValue)
	firstStored := m.entries[first.ID].secret
	secondStored := m.entries[second.ID].secret

	if got := m.CancelPending(); got != 2 {
		t.Fatalf("CancelPending count = %d, want 2", got)
	}
	for _, request := range []Request{first, second} {
		status, err := m.Get(request.ID)
		if err != nil || status.Status != StatusCancelled || status.Ready {
			t.Fatalf("pending status after CancelPending = %#v, err=%v", status, err)
		}
	}
	if !allZero(firstStored) || !allZero(secondStored) {
		t.Fatal("CancelPending did not wipe all ready values")
	}
	status, err := m.Get(claimed.ID)
	if err != nil || status.Status != StatusClaimed || status.Ready {
		t.Fatalf("claimed history after CancelPending = %#v, err=%v", status, err)
	}
}

func TestNoSecretLeakThroughStatusDiagnosticsOrSerialization(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	m := newTestManager(t, clock)
	r := createTestRequest(t, m)
	secretText := "ultra-private-value-9a1f"
	if err := m.Submit(r.ID, []byte(secretText)); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	status, err := m.Get(r.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	statusJSON, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("Marshal status: %v", err)
	}
	diagnostics := []string{
		string(statusJSON),
		status.String(),
		fmt.Sprintf("%v", status),
		fmt.Sprintf("%#v", status),
		fmt.Sprintf("%v", m),
		fmt.Sprintf("%+v", m),
		fmt.Sprintf("%#v", m),
		ErrInvalidSecret.Error(),
		ErrSecretUnavailable.Error(),
	}
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic, secretText) {
			t.Fatalf("diagnostic leaked secret: %q", diagnostic)
		}
	}
	if !strings.Contains(status.String(), "[redacted]") {
		t.Fatalf("status String is not visibly redacted: %q", status.String())
	}

	if payload, err := json.Marshal(m); !errors.Is(err, ErrPersistenceForbidden) || payload != nil {
		t.Fatalf("json.Marshal manager = %q, %v", payload, err)
	}
	var gobBuffer bytes.Buffer
	if err := gob.NewEncoder(&gobBuffer).Encode(m); !errors.Is(err, ErrPersistenceForbidden) {
		t.Fatalf("gob Encode error = %v", err)
	}
	if bytes.Contains(gobBuffer.Bytes(), []byte(secretText)) {
		t.Fatal("gob output leaked secret")
	}
}

func TestValidationDoesNotEchoRejectedValuesAndWipesRejectedSecret(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	m := newTestManager(t, clock)
	badValue := "bad value containing private-text"
	_, err := m.Create(CreateRequest{
		RunID:          badValue,
		ConversationID: "conversation",
		Surface:        "browser:tab",
		Purpose:        PurposePassword,
	})
	if !errors.Is(err, ErrInvalidRunID) || strings.Contains(err.Error(), badValue) {
		t.Fatalf("validation error = %v", err)
	}

	rejected := []byte("must-be-wiped")
	if err := m.Submit("not-a-real-id", rejected); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Submit unknown error = %v", err)
	}
	if !allZero(rejected) {
		t.Fatal("rejected Submit did not wipe caller input")
	}

	for _, purpose := range []Purpose{PurposePassword, PurposeTwoFactorCode, PurposeCaptcha, PurposePaymentAuthorization} {
		if !purpose.Valid() {
			t.Fatalf("expected purpose %q to be valid", purpose)
		}
	}
	if Purpose("custom").Valid() {
		t.Fatal("unreviewed custom purpose accepted")
	}
}

func TestConfigTTLBounds(t *testing.T) {
	for _, ttl := range []time.Duration{time.Nanosecond, MaxTTL + time.Second} {
		if _, err := New(Config{TTL: ttl}); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("New TTL %s error = %v", ttl, err)
		}
	}
	m, err := New(Config{})
	if err != nil {
		t.Fatalf("New default: %v", err)
	}
	defer m.Close()
	if m.ttl != DefaultTTL {
		t.Fatalf("default TTL = %s", m.ttl)
	}
}

func allZero(value []byte) bool {
	for _, b := range value {
		if b != 0 {
			return false
		}
	}
	return true
}
