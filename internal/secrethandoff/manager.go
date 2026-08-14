// Package secrethandoff provides an in-memory, short-lived handoff channel for
// sensitive values that must never enter chat, traces, logs, or durable state.
package secrethandoff

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	DefaultTTL = 2 * time.Minute
	MaxTTL     = 15 * time.Minute
	MaxSecret  = 64 * 1024
)

var (
	ErrInvalidConfig        = errors.New("secret handoff: invalid configuration")
	ErrInvalidRunID         = errors.New("secret handoff: invalid run id")
	ErrInvalidConversation  = errors.New("secret handoff: invalid conversation id")
	ErrInvalidSurface       = errors.New("secret handoff: invalid surface")
	ErrInvalidComputerID    = errors.New("secret handoff: invalid computer id")
	ErrInvalidTargetID      = errors.New("secret handoff: invalid target id")
	ErrInvalidPurpose       = errors.New("secret handoff: invalid purpose")
	ErrInvalidSecret        = errors.New("secret handoff: invalid secret")
	ErrIDGeneration         = errors.New("secret handoff: request id generation unavailable")
	ErrNotFound             = errors.New("secret handoff: request not found")
	ErrNotPending           = errors.New("secret handoff: request is not pending")
	ErrSecretUnavailable    = errors.New("secret handoff: secret is not available")
	ErrAlreadySubmitted     = errors.New("secret handoff: secret was already submitted")
	ErrBindingMismatch      = errors.New("secret handoff: request binding mismatch")
	ErrClosed               = errors.New("secret handoff: manager is closed")
	ErrPersistenceForbidden = errors.New("secret handoff: persistence is forbidden")
)

// Purpose is deliberately closed: callers cannot invent a purpose that was
// not reviewed as part of the human-takeover security boundary.
type Purpose string

const (
	PurposePassword             Purpose = "password"
	PurposeTwoFactorCode        Purpose = "two_factor_code"
	PurposeCaptcha              Purpose = "captcha"
	PurposePaymentAuthorization Purpose = "payment_authorization"
)

func (p Purpose) Valid() bool {
	switch p {
	case PurposePassword, PurposeTwoFactorCode, PurposeCaptcha, PurposePaymentAuthorization:
		return true
	default:
		return false
	}
}

type Status string

const (
	StatusPending   Status = "pending"
	StatusClaimed   Status = "claimed"
	StatusExpired   Status = "expired"
	StatusCancelled Status = "cancelled"
)

// Request is a secret-free snapshot suitable for UI/API transport. Ready only
// says that a value is waiting; the value itself never leaves Manager except
// through Claim.
type Request struct {
	ID             string    `json:"id"`
	RunID          string    `json:"run_id"`
	ConversationID string    `json:"conversation_id"`
	Surface        string    `json:"surface"`
	ComputerID     string    `json:"-"`
	TargetID       string    `json:"-"`
	Purpose        Purpose   `json:"purpose"`
	Status         Status    `json:"status"`
	Ready          bool      `json:"ready"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// String intentionally redacts identifiers as well as the secret-presence
// detail so accidental diagnostics cannot correlate or disclose a handoff.
func (r Request) String() string {
	return fmt.Sprintf("SecretHandoffRequest{id=[redacted] run=[redacted] conversation=[redacted] surface=[redacted] computer=[redacted] target=[redacted] purpose=%s status=%s secret=[redacted]}", r.Purpose, r.Status)
}

func (r Request) GoString() string { return r.String() }

type Config struct {
	TTL   time.Duration
	Clock func() time.Time
	Rand  io.Reader
}

type CreateRequest struct {
	RunID          string
	ConversationID string
	Surface        string
	ComputerID     string
	TargetID       string
	Purpose        Purpose
}

// ClaimRequest requires the consuming run to prove the same binding that was
// approved when the handoff was created. Errors never disclose which field did
// not match.
type ClaimRequest struct {
	ID             string
	RunID          string
	ConversationID string
	Surface        string
	ComputerID     string
	TargetID       string
	Purpose        Purpose
}

type entry struct {
	request Request
	secret  []byte
}

// Manager owns all stored secret bytes. It is safe for concurrent use and has
// no persistence API. Close wipes every still-resident value.
type Manager struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	random  io.Reader
	entries map[string]*entry
	closed  bool
}

func New(config Config) (*Manager, error) {
	ttl := config.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if ttl < time.Second || ttl > MaxTTL {
		return nil, ErrInvalidConfig
	}
	now := config.Clock
	if now == nil {
		now = time.Now
	}
	random := config.Rand
	if random == nil {
		random = rand.Reader
	}
	return &Manager{
		ttl:     ttl,
		now:     now,
		random:  random,
		entries: make(map[string]*entry),
	}, nil
}

func (m *Manager) Create(input CreateRequest) (Request, error) {
	if err := validateBinding(input); err != nil {
		return Request{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Request{}, ErrClosed
	}

	id, err := m.newIDLocked()
	if err != nil {
		return Request{}, ErrIDGeneration
	}
	now := m.now().UTC()
	request := Request{
		ID:             id,
		RunID:          input.RunID,
		ConversationID: input.ConversationID,
		Surface:        input.Surface,
		ComputerID:     input.ComputerID,
		TargetID:       input.TargetID,
		Purpose:        input.Purpose,
		Status:         StatusPending,
		CreatedAt:      now,
		ExpiresAt:      now.Add(m.ttl),
	}
	m.entries[id] = &entry{request: request}
	return request, nil
}

// Submit consumes secret. The supplied buffer is wiped before Submit returns,
// including on validation or state-transition failure.
func (m *Manager) Submit(id string, secret []byte) error {
	defer Wipe(secret)
	if !validIdentifier(id) {
		return ErrNotFound
	}
	if len(secret) == 0 || len(secret) > MaxSecret {
		return ErrInvalidSecret
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	e, ok := m.entries[id]
	if !ok {
		return ErrNotFound
	}
	m.expireLocked(e)
	if e.request.Status != StatusPending {
		return ErrNotPending
	}
	if len(e.secret) != 0 {
		return ErrAlreadySubmitted
	}
	e.secret = append(make([]byte, 0, len(secret)), secret...)
	e.request.Ready = true
	return nil
}

// Claim returns a caller-owned copy exactly once. Before Claim returns, the
// Manager's stored bytes are wiped and detached. The caller must call Wipe on
// the returned slice immediately after use.
func (m *Manager) Claim(input ClaimRequest) ([]byte, error) {
	if !validIdentifier(input.ID) {
		return nil, ErrNotFound
	}
	if err := validateBinding(CreateRequest{
		RunID:          input.RunID,
		ConversationID: input.ConversationID,
		Surface:        input.Surface,
		ComputerID:     input.ComputerID,
		TargetID:       input.TargetID,
		Purpose:        input.Purpose,
	}); err != nil {
		return nil, ErrBindingMismatch
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	e, ok := m.entries[input.ID]
	if !ok {
		return nil, ErrNotFound
	}
	m.expireLocked(e)
	if e.request.Status != StatusPending {
		return nil, ErrNotPending
	}
	if e.request.RunID != input.RunID ||
		e.request.ConversationID != input.ConversationID ||
		e.request.Surface != input.Surface ||
		e.request.ComputerID != input.ComputerID ||
		e.request.TargetID != input.TargetID ||
		e.request.Purpose != input.Purpose {
		return nil, ErrBindingMismatch
	}
	if len(e.secret) == 0 {
		return nil, ErrSecretUnavailable
	}

	claimed := append(make([]byte, 0, len(e.secret)), e.secret...)
	Wipe(e.secret)
	e.secret = nil
	e.request.Ready = false
	e.request.Status = StatusClaimed
	return claimed, nil
}

func (m *Manager) Get(id string) (Request, error) {
	if !validIdentifier(id) {
		return Request{}, ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Request{}, ErrClosed
	}
	e, ok := m.entries[id]
	if !ok {
		return Request{}, ErrNotFound
	}
	m.expireLocked(e)
	return e.request, nil
}

func (m *Manager) Cancel(id string) (Request, error) {
	if !validIdentifier(id) {
		return Request{}, ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Request{}, ErrClosed
	}
	e, ok := m.entries[id]
	if !ok {
		return Request{}, ErrNotFound
	}
	m.expireLocked(e)
	if e.request.Status != StatusPending {
		return Request{}, ErrNotPending
	}
	m.wipeEntryLocked(e, StatusCancelled)
	return e.request, nil
}

// CancelPending wipes every pending handoff and makes it unusable. It is used
// when the human/agent computer-control boundary changes, so a prompt created
// under a previous control state cannot be submitted after control is released
// and later re-enabled.
func (m *Manager) CancelPending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0
	}
	count := 0
	for _, e := range m.entries {
		m.expireLocked(e)
		if e.request.Status != StatusPending {
			continue
		}
		m.wipeEntryLocked(e, StatusCancelled)
		count++
	}
	return count
}

// CleanupExpired transitions expired pending requests and wipes their values.
// Metadata remains available for status polling until Manager is closed.
func (m *Manager) CleanupExpired() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0
	}
	count := 0
	for _, e := range m.entries {
		if m.expireLocked(e) {
			count++
		}
	}
	return count
}

// Close wipes all resident values and permanently disables the Manager.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	for _, e := range m.entries {
		if e.request.Status == StatusPending {
			m.wipeEntryLocked(e, StatusCancelled)
		} else {
			Wipe(e.secret)
			e.secret = nil
			e.request.Ready = false
		}
	}
	m.closed = true
	return nil
}

// String and Format deliberately expose no request metadata or secret bytes.
func (m *Manager) String() string                 { return "SecretHandoffManager{state=[redacted]}" }
func (m *Manager) GoString() string               { return m.String() }
func (m *Manager) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, m.String()) }

// The manager is intentionally non-serializable. Safe Request snapshots can be
// transported independently; live handoff state must remain in memory.
func (m *Manager) MarshalJSON() ([]byte, error)   { return nil, ErrPersistenceForbidden }
func (m *Manager) MarshalText() ([]byte, error)   { return nil, ErrPersistenceForbidden }
func (m *Manager) MarshalBinary() ([]byte, error) { return nil, ErrPersistenceForbidden }
func (m *Manager) GobEncode() ([]byte, error)     { return nil, ErrPersistenceForbidden }

func (m *Manager) expireLocked(e *entry) bool {
	if e.request.Status != StatusPending || m.now().Before(e.request.ExpiresAt) {
		return false
	}
	m.wipeEntryLocked(e, StatusExpired)
	return true
}

func (m *Manager) wipeEntryLocked(e *entry, status Status) {
	Wipe(e.secret)
	e.secret = nil
	e.request.Ready = false
	e.request.Status = status
}

func (m *Manager) newIDLocked() (string, error) {
	var raw [16]byte
	for attempts := 0; attempts < 4; attempts++ {
		if _, err := io.ReadFull(m.random, raw[:]); err != nil {
			return "", err
		}
		id := "handoff_" + hex.EncodeToString(raw[:])
		if _, exists := m.entries[id]; !exists {
			return id, nil
		}
	}
	return "", errors.New("request id collision")
}

func validateBinding(input CreateRequest) error {
	if !validIdentifier(input.RunID) {
		return ErrInvalidRunID
	}
	if !validIdentifier(input.ConversationID) {
		return ErrInvalidConversation
	}
	if !validIdentifier(input.Surface) {
		return ErrInvalidSurface
	}
	if (input.ComputerID == "") != (input.TargetID == "") {
		return ErrInvalidTargetID
	}
	if input.ComputerID != "" && !validIdentifier(input.ComputerID) {
		return ErrInvalidComputerID
	}
	if input.TargetID != "" && !validIdentifier(input.TargetID) {
		return ErrInvalidTargetID
	}
	if !input.Purpose.Valid() {
		return ErrInvalidPurpose
	}
	return nil
}

func validIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '-', '_', '.', ':', '/':
		default:
			return false
		}
	}
	return true
}

// Wipe clears a sensitive caller-owned byte slice. It should be deferred as
// soon as Claim succeeds.
func Wipe(value []byte) {
	clear(value)
	runtime.KeepAlive(value)
}
