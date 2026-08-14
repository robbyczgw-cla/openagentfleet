// Package nodes implements the in-memory, capability-based control core for
// Mac, iOS, and Android nodes. It deliberately does not pair devices, perform
// network I/O, persist state, or execute computer actions. Those integrations
// must bind an authenticated device to this registry before invoking it.
package nodes

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultLeaseTTL = 30 * time.Second
	MaxLeaseTTL     = 2 * time.Minute
	minLeaseTTL     = time.Second
)

var (
	ErrInvalidConfig          = errors.New("nodes: invalid configuration")
	ErrInvalidNodeID          = errors.New("nodes: invalid node id")
	ErrInvalidComputerID      = errors.New("nodes: invalid computer id")
	ErrInvalidFrameID         = errors.New("nodes: invalid frame id")
	ErrInvalidActionID        = errors.New("nodes: invalid action id")
	ErrInvalidActionHash      = errors.New("nodes: invalid action hash")
	ErrInvalidPlatform        = errors.New("nodes: invalid platform")
	ErrInvalidRole            = errors.New("nodes: invalid role")
	ErrInvalidCapability      = errors.New("nodes: invalid capability")
	ErrNodeExists             = errors.New("nodes: node already exists")
	ErrNodeNotFound           = errors.New("nodes: node not found")
	ErrNodeRevoked            = errors.New("nodes: node is revoked")
	ErrCapabilityDenied       = errors.New("nodes: capability is denied")
	ErrCapabilityNotLeaseable = errors.New("nodes: capability is not leaseable")
	ErrLeaseNotFound          = errors.New("nodes: lease not found")
	ErrLeaseInactive          = errors.New("nodes: lease is not active")
	ErrLeaseBindingMismatch   = errors.New("nodes: lease binding mismatch")
	ErrStaleControlState      = errors.New("nodes: stale control state")
	ErrActionConflict         = errors.New("nodes: action id conflicts with prior action")
	ErrIDGeneration           = errors.New("nodes: id generation unavailable")
	ErrClosed                 = errors.New("nodes: registry is closed")
	ErrPersistenceForbidden   = errors.New("nodes: persistence is forbidden")
)

// Platform is the operating system that owns a node identity.
type Platform string

const (
	PlatformMac     Platform = "mac"
	PlatformIOS     Platform = "ios"
	PlatformAndroid Platform = "android"
)

func (p Platform) Valid() bool {
	switch p {
	case PlatformMac, PlatformIOS, PlatformAndroid:
		return true
	default:
		return false
	}
}

// Role is an authenticated node's maximum authority. Effective authority is
// always the intersection of its role and explicitly advertised capabilities.
type Role string

const (
	RoleOwner      Role = "owner"
	RoleController Role = "controller"
	RoleObserver   Role = "observer"
)

func (r Role) Valid() bool {
	switch r {
	case RoleOwner, RoleController, RoleObserver:
		return true
	default:
		return false
	}
}

// Capability names are canonical dot-separated protocol values. HTTP or
// harness adapters may map older names at their boundary, never in this core.
type Capability string

const (
	CapabilityStateRead         Capability = "state.read"
	CapabilityEventsRead        Capability = "events.read"
	CapabilityChatWrite         Capability = "chat.write"
	CapabilityComputerView      Capability = "computer.view"
	CapabilityScreenSnapshot    Capability = "screen.snapshot"
	CapabilityComputerControl   Capability = "computer.control"
	CapabilityFilesRead         Capability = "files.read"
	CapabilityFilesWrite        Capability = "files.write"
	CapabilityBrowserProfileUse Capability = "browser.profile.use"
	CapabilityAppLaunch         Capability = "app.launch"
	CapabilityNetworkAccess     Capability = "network.access"
	CapabilityMCPInvoke         Capability = "mcp.invoke"
	CapabilityMCPManage         Capability = "mcp.manage"
	CapabilityCameraCapture     Capability = "camera.capture"
	CapabilityAudioRecord       Capability = "audio.record"
	CapabilityNotificationSend  Capability = "notification.send"
)

func (c Capability) Valid() bool {
	switch c {
	case CapabilityStateRead, CapabilityEventsRead, CapabilityChatWrite,
		CapabilityComputerView, CapabilityScreenSnapshot, CapabilityComputerControl,
		CapabilityFilesRead, CapabilityFilesWrite, CapabilityBrowserProfileUse,
		CapabilityAppLaunch, CapabilityNetworkAccess, CapabilityMCPInvoke,
		CapabilityMCPManage, CapabilityCameraCapture, CapabilityAudioRecord,
		CapabilityNotificationSend:
		return true
	default:
		return false
	}
}

// Sensitive reports capabilities that must never become effective merely from
// a role. They require an explicit advertisement and, for computer.control, a
// current exclusive lease too.
func (c Capability) Sensitive() bool {
	switch c {
	case CapabilityComputerControl, CapabilityFilesRead, CapabilityFilesWrite,
		CapabilityBrowserProfileUse, CapabilityAppLaunch, CapabilityNetworkAccess,
		CapabilityMCPInvoke, CapabilityMCPManage, CapabilityCameraCapture,
		CapabilityAudioRecord, CapabilityNotificationSend:
		return true
	default:
		return false
	}
}

func (c Capability) RequiresLease() bool { return c == CapabilityComputerControl }

// NodeStatus is intentionally short: revoked identities cannot be restored
// in-process. A future durable pairing layer must create a fresh identity.
type NodeStatus string

const (
	NodeActive  NodeStatus = "active"
	NodeRevoked NodeStatus = "revoked"
)

// LeaseStatus is a terminal-aware lease lifecycle suitable for UI snapshots.
type LeaseStatus string

const (
	LeaseActive  LeaseStatus = "active"
	LeaseExpired LeaseStatus = "expired"
	LeaseRevoked LeaseStatus = "revoked"
)

// Config controls the in-memory state machine. The injected clock is for
// deterministic expiry testing; Rand is used only to generate opaque lease IDs.
type Config struct {
	LeaseTTL time.Duration
	Clock    func() time.Time
	Rand     io.Reader
}

// RegisterRequest represents an explicit user/device capability advertisement.
// There is no implicit capability grant, including for owners.
type RegisterRequest struct {
	NodeID       string
	Platform     Platform
	Role         Role
	Capabilities []Capability
}

// Node is a safe snapshot. AdvertisedCapabilities records a user-visible
// toggle; EffectiveCapabilities is what this core will currently authorize.
type Node struct {
	ID                     string       `json:"id"`
	Platform               Platform     `json:"platform"`
	Role                   Role         `json:"role"`
	Status                 NodeStatus   `json:"status"`
	AdvertisedCapabilities []Capability `json:"advertised_capabilities"`
	EffectiveCapabilities  []Capability `json:"effective_capabilities"`
	CreatedAt              time.Time    `json:"created_at"`
	UpdatedAt              time.Time    `json:"updated_at"`
}

// Frame is opaque display state. Pixel data never enters this package.
type Frame struct {
	ComputerID  string    `json:"computer_id"`
	ID          string    `json:"id"`
	Epoch       uint64    `json:"epoch"`
	PublishedAt time.Time `json:"published_at"`
}

// Lease grants one controller/owner exclusive control of one computer for a
// short time. It authorizes nothing by itself until AuthorizeAction validates
// the caller's node, epoch, frame, action ID, and action hash.
type Lease struct {
	ID         string      `json:"id"`
	NodeID     string      `json:"node_id"`
	Capability Capability  `json:"capability"`
	ComputerID string      `json:"computer_id"`
	Epoch      uint64      `json:"epoch"`
	FrameID    string      `json:"frame_id"`
	Status     LeaseStatus `json:"status"`
	CreatedAt  time.Time   `json:"created_at"`
	ExpiresAt  time.Time   `json:"expires_at"`
}

type IssueLeaseRequest struct {
	NodeID     string
	Capability Capability
	ComputerID string
	// FrameID must be the fresh frame captured for this exclusive takeover.
	// The registry advances the epoch before binding it, so a prior controller's
	// frame can never be used as the first target of a new lease.
	FrameID string
}

// ActionRequest must be constructed from a canonicalized action payload by
// the integration layer. ActionHash is its lowercase SHA-256 hex digest; the
// raw action payload never enters this state machine.
type ActionRequest struct {
	LeaseID    string
	NodeID     string
	Capability Capability
	ComputerID string
	Epoch      uint64
	FrameID    string
	ActionID   string
	ActionHash string
}

// ActionAuthorization tells an executor whether it may perform exactly one
// action. Duplicate means the same action ID and hash were already authorized
// for this live lease; callers must not execute it a second time.
type ActionAuthorization struct {
	Authorized bool `json:"authorized"`
	Duplicate  bool `json:"duplicate"`
}

type nodeEntry struct {
	node       Node
	advertised map[Capability]struct{}
}

type leaseEntry struct {
	lease   Lease
	actions map[string]string
}

// Registry is a concurrency-safe runtime state machine. It intentionally has
// no persistence API: durable device identity and pairing live elsewhere.
type Registry struct {
	mu                    sync.Mutex
	now                   func() time.Time
	random                io.Reader
	ttl                   time.Duration
	nodes                 map[string]*nodeEntry
	leases                map[string]*leaseEntry
	activeLeaseByComputer map[string]string
	frames                map[string]Frame
	epochs                map[string]uint64
	closed                bool
}

func New(config Config) (*Registry, error) {
	ttl := config.LeaseTTL
	if ttl == 0 {
		ttl = DefaultLeaseTTL
	}
	if ttl < minLeaseTTL || ttl > MaxLeaseTTL {
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
	return &Registry{
		now:                   now,
		random:                random,
		ttl:                   ttl,
		nodes:                 make(map[string]*nodeEntry),
		leases:                make(map[string]*leaseEntry),
		activeLeaseByComputer: make(map[string]string),
		frames:                make(map[string]Frame),
		epochs:                make(map[string]uint64),
	}, nil
}

func (r *Registry) Register(input RegisterRequest) (Node, error) {
	if !validIdentifier(input.NodeID) {
		return Node{}, ErrInvalidNodeID
	}
	if !input.Platform.Valid() {
		return Node{}, ErrInvalidPlatform
	}
	if !input.Role.Valid() {
		return Node{}, ErrInvalidRole
	}
	caps, err := normalizedCapabilities(input.Capabilities)
	if err != nil {
		return Node{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Node{}, ErrClosed
	}
	if _, exists := r.nodes[input.NodeID]; exists {
		return Node{}, ErrNodeExists
	}
	now := r.now().UTC()
	entry := &nodeEntry{
		advertised: capabilityMap(caps),
		node: Node{
			ID:        input.NodeID,
			Platform:  input.Platform,
			Role:      input.Role,
			Status:    NodeActive,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	r.nodes[input.NodeID] = entry
	return snapshotNode(entry), nil
}

func (r *Registry) GetNode(nodeID string) (Node, error) {
	if !validIdentifier(nodeID) {
		return Node{}, ErrNodeNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Node{}, ErrClosed
	}
	entry, ok := r.nodes[nodeID]
	if !ok {
		return Node{}, ErrNodeNotFound
	}
	return snapshotNode(entry), nil
}

func (r *Registry) ListNodes() ([]Node, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	items := make([]Node, 0, len(r.nodes))
	for _, entry := range r.nodes {
		items = append(items, snapshotNode(entry))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

// UpdateCapabilities replaces all toggles atomically. Removing a capability
// immediately revokes an affected active control lease and advances its epoch.
func (r *Registry) UpdateCapabilities(nodeID string, capabilities []Capability) (Node, error) {
	if !validIdentifier(nodeID) {
		return Node{}, ErrNodeNotFound
	}
	caps, err := normalizedCapabilities(capabilities)
	if err != nil {
		return Node{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Node{}, ErrClosed
	}
	entry, err := r.activeNodeLocked(nodeID)
	if err != nil {
		return Node{}, err
	}
	entry.advertised = capabilityMap(caps)
	entry.node.UpdatedAt = r.now().UTC()
	if !effectiveCapability(entry, CapabilityComputerControl) {
		r.revokeLeasesForNodeLocked(nodeID)
	}
	return snapshotNode(entry), nil
}

// SetRole is intentionally authorization-neutral: its caller is responsible
// for deciding who may change a role. Any role change revokes active leases so
// a newly reduced role cannot retain a previously granted control path.
func (r *Registry) SetRole(nodeID string, role Role) (Node, error) {
	if !validIdentifier(nodeID) {
		return Node{}, ErrNodeNotFound
	}
	if !role.Valid() {
		return Node{}, ErrInvalidRole
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Node{}, ErrClosed
	}
	entry, err := r.activeNodeLocked(nodeID)
	if err != nil {
		return Node{}, err
	}
	if entry.node.Role != role {
		entry.node.Role = role
		entry.node.UpdatedAt = r.now().UTC()
		r.revokeLeasesForNodeLocked(nodeID)
	}
	return snapshotNode(entry), nil
}

// RevokeNode permanently disables the in-memory node identity and invalidates
// every active lease it held. Pairing credentials must be revoked separately.
func (r *Registry) RevokeNode(nodeID string) (Node, error) {
	if !validIdentifier(nodeID) {
		return Node{}, ErrNodeNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Node{}, ErrClosed
	}
	entry, ok := r.nodes[nodeID]
	if !ok {
		return Node{}, ErrNodeNotFound
	}
	if entry.node.Status == NodeActive {
		entry.node.Status = NodeRevoked
		entry.node.UpdatedAt = r.now().UTC()
		r.revokeLeasesForNodeLocked(nodeID)
	}
	return snapshotNode(entry), nil
}

// PublishFrame records the latest opaque frame available for a computer. A
// new frame makes actions against an older frame fail closed, but does not by
// itself revoke an otherwise valid short control lease.
func (r *Registry) PublishFrame(computerID, frameID string) (Frame, error) {
	if !validIdentifier(computerID) {
		return Frame{}, ErrInvalidComputerID
	}
	if !validIdentifier(frameID) {
		return Frame{}, ErrInvalidFrameID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Frame{}, ErrClosed
	}
	frame := Frame{ComputerID: computerID, ID: frameID, Epoch: r.epochLocked(computerID), PublishedAt: r.now().UTC()}
	r.frames[computerID] = frame
	return frame, nil
}

// BumpEpoch invalidates every lease and frame associated with a computer. It
// is used for computer restart, manual takeover, or any lost control boundary.
func (r *Registry) BumpEpoch(computerID string) (uint64, error) {
	if !validIdentifier(computerID) {
		return 0, ErrInvalidComputerID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, ErrClosed
	}
	if leaseID, exists := r.activeLeaseByComputer[computerID]; exists {
		if entry, ok := r.leases[leaseID]; ok && entry.lease.Status == LeaseActive {
			r.deactivateLeaseLocked(entry, LeaseRevoked, false)
		}
	}
	return r.bumpEpochLocked(computerID), nil
}

func (r *Registry) IssueLease(input IssueLeaseRequest) (Lease, error) {
	if !validIdentifier(input.NodeID) {
		return Lease{}, ErrInvalidNodeID
	}
	if !validIdentifier(input.ComputerID) {
		return Lease{}, ErrInvalidComputerID
	}
	if !validIdentifier(input.FrameID) {
		return Lease{}, ErrInvalidFrameID
	}
	if !input.Capability.Valid() {
		return Lease{}, ErrInvalidCapability
	}
	if !input.Capability.RequiresLease() {
		return Lease{}, ErrCapabilityNotLeaseable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Lease{}, ErrClosed
	}
	entry, err := r.activeNodeLocked(input.NodeID)
	if err != nil {
		return Lease{}, err
	}
	if !effectiveCapability(entry, input.Capability) {
		return Lease{}, ErrCapabilityDenied
	}
	epochAdvanced := false
	if oldID, exists := r.activeLeaseByComputer[input.ComputerID]; exists {
		if old, ok := r.leases[oldID]; ok {
			epochAdvanced = r.expireLeaseLocked(old)
			if old.lease.Status == LeaseActive {
				r.deactivateLeaseLocked(old, LeaseRevoked, false)
			}
		}
	}
	// A new lease is an exclusive control takeover. Advance the epoch, then bind
	// the supplied fresh frame so it cannot target the prior controller's UI.
	epoch := r.epochLocked(input.ComputerID)
	if !epochAdvanced {
		epoch = r.bumpEpochLocked(input.ComputerID)
	}
	now := r.now().UTC()
	frame := Frame{ComputerID: input.ComputerID, ID: input.FrameID, Epoch: epoch, PublishedAt: now}
	r.frames[input.ComputerID] = frame
	id, err := r.newLeaseIDLocked()
	if err != nil {
		return Lease{}, ErrIDGeneration
	}
	lease := Lease{
		ID: id, NodeID: input.NodeID, Capability: input.Capability, ComputerID: input.ComputerID,
		Epoch: epoch, FrameID: input.FrameID, Status: LeaseActive, CreatedAt: now, ExpiresAt: now.Add(r.ttl),
	}
	r.leases[id] = &leaseEntry{lease: lease, actions: make(map[string]string)}
	r.activeLeaseByComputer[input.ComputerID] = id
	return lease, nil
}

// IssueLeaseWithFrame is a compatibility helper for integrations that carry
// their fresh frame separately from the request body.
func (r *Registry) IssueLeaseWithFrame(input IssueLeaseRequest, frameID string) (Lease, error) {
	input.FrameID = frameID
	return r.IssueLease(input)
}

// GetLease returns status after applying expiry transitions.
func (r *Registry) GetLease(leaseID string) (Lease, error) {
	if !validIdentifier(leaseID) {
		return Lease{}, ErrLeaseNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Lease{}, ErrClosed
	}
	entry, ok := r.leases[leaseID]
	if !ok {
		return Lease{}, ErrLeaseNotFound
	}
	r.expireLeaseLocked(entry)
	return entry.lease, nil
}

// RenewLease is only valid for the node that owns an unexpired lease. Renewal
// keeps the epoch and frame binding intact; a fresh frame is still required
// for each action after PublishFrame changes the current display.
func (r *Registry) RenewLease(leaseID, nodeID string) (Lease, error) {
	if !validIdentifier(leaseID) {
		return Lease{}, ErrLeaseNotFound
	}
	if !validIdentifier(nodeID) {
		return Lease{}, ErrLeaseBindingMismatch
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Lease{}, ErrClosed
	}
	entry, ok := r.leases[leaseID]
	if !ok {
		return Lease{}, ErrLeaseNotFound
	}
	r.expireLeaseLocked(entry)
	if entry.lease.Status != LeaseActive {
		return Lease{}, ErrLeaseInactive
	}
	if entry.lease.NodeID != nodeID {
		return Lease{}, ErrLeaseBindingMismatch
	}
	if _, err := r.activeNodeLocked(nodeID); err != nil {
		return Lease{}, err
	}
	entry.lease.ExpiresAt = r.now().UTC().Add(r.ttl)
	return entry.lease, nil
}

func (r *Registry) RevokeLease(leaseID string) (Lease, error) {
	if !validIdentifier(leaseID) {
		return Lease{}, ErrLeaseNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Lease{}, ErrClosed
	}
	entry, ok := r.leases[leaseID]
	if !ok {
		return Lease{}, ErrLeaseNotFound
	}
	r.expireLeaseLocked(entry)
	if entry.lease.Status == LeaseActive {
		r.deactivateLeaseLocked(entry, LeaseRevoked, true)
	}
	return entry.lease, nil
}

// AuthorizeAction is an idempotency boundary, not an executor. A caller that
// receives Duplicate must treat the action as already dispatched and avoid a
// second click, keystroke, or other side effect.
func (r *Registry) AuthorizeAction(input ActionRequest) (ActionAuthorization, error) {
	if !validIdentifier(input.LeaseID) {
		return ActionAuthorization{}, ErrLeaseNotFound
	}
	if !validIdentifier(input.NodeID) || !validIdentifier(input.ComputerID) {
		return ActionAuthorization{}, ErrLeaseBindingMismatch
	}
	if !input.Capability.Valid() {
		return ActionAuthorization{}, ErrInvalidCapability
	}
	if !validIdentifier(input.FrameID) {
		return ActionAuthorization{}, ErrInvalidFrameID
	}
	if !validIdentifier(input.ActionID) {
		return ActionAuthorization{}, ErrInvalidActionID
	}
	if !validActionHash(input.ActionHash) {
		return ActionAuthorization{}, ErrInvalidActionHash
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ActionAuthorization{}, ErrClosed
	}
	entry, ok := r.leases[input.LeaseID]
	if !ok {
		return ActionAuthorization{}, ErrLeaseNotFound
	}
	r.expireLeaseLocked(entry)
	lease := entry.lease
	if lease.Status != LeaseActive {
		return ActionAuthorization{}, ErrLeaseInactive
	}
	if lease.NodeID != input.NodeID || lease.Capability != input.Capability || lease.ComputerID != input.ComputerID {
		return ActionAuthorization{}, ErrLeaseBindingMismatch
	}
	if _, err := r.activeNodeLocked(input.NodeID); err != nil {
		return ActionAuthorization{}, err
	}
	if !effectiveCapability(r.nodes[input.NodeID], input.Capability) {
		return ActionAuthorization{}, ErrCapabilityDenied
	}
	currentFrame, ok := r.frames[input.ComputerID]
	if !ok || currentFrame.Epoch != lease.Epoch || input.Epoch != lease.Epoch || input.FrameID != currentFrame.ID {
		return ActionAuthorization{}, ErrStaleControlState
	}
	if oldHash, seen := entry.actions[input.ActionID]; seen {
		if oldHash != input.ActionHash {
			return ActionAuthorization{}, ErrActionConflict
		}
		return ActionAuthorization{Authorized: true, Duplicate: true}, nil
	}
	entry.actions[input.ActionID] = input.ActionHash
	return ActionAuthorization{Authorized: true}, nil
}

// CleanupExpired applies terminal expiry transitions and returns the number of
// leases that changed from active to expired. It is optional; every access
// path already expires lazily and fails closed.
func (r *Registry) CleanupExpired() (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, ErrClosed
	}
	count := 0
	for _, entry := range r.leases {
		if r.expireLeaseLocked(entry) {
			count++
		}
	}
	return count, nil
}

func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	for _, entry := range r.leases {
		if entry.lease.Status == LeaseActive {
			entry.lease.Status = LeaseRevoked
		}
		clear(entry.actions)
	}
	clear(r.activeLeaseByComputer)
	clear(r.frames)
	r.closed = true
	return nil
}

func (r *Registry) String() string                 { return "NodeRegistry{state=[redacted]}" }
func (r *Registry) GoString() string               { return r.String() }
func (r *Registry) Format(s fmt.State, _ rune)     { _, _ = io.WriteString(s, r.String()) }
func (r *Registry) MarshalJSON() ([]byte, error)   { return nil, ErrPersistenceForbidden }
func (r *Registry) MarshalText() ([]byte, error)   { return nil, ErrPersistenceForbidden }
func (r *Registry) MarshalBinary() ([]byte, error) { return nil, ErrPersistenceForbidden }
func (r *Registry) GobEncode() ([]byte, error)     { return nil, ErrPersistenceForbidden }

func (r *Registry) activeNodeLocked(nodeID string) (*nodeEntry, error) {
	entry, ok := r.nodes[nodeID]
	if !ok {
		return nil, ErrNodeNotFound
	}
	if entry.node.Status != NodeActive {
		return nil, ErrNodeRevoked
	}
	return entry, nil
}

func (r *Registry) epochLocked(computerID string) uint64 {
	if epoch := r.epochs[computerID]; epoch != 0 {
		return epoch
	}
	r.epochs[computerID] = 1
	return 1
}

func (r *Registry) bumpEpochLocked(computerID string) uint64 {
	epoch := r.epochLocked(computerID) + 1
	r.epochs[computerID] = epoch
	delete(r.frames, computerID)
	return epoch
}

func (r *Registry) expireLeaseLocked(entry *leaseEntry) bool {
	if entry.lease.Status != LeaseActive || r.now().Before(entry.lease.ExpiresAt) {
		return false
	}
	r.deactivateLeaseLocked(entry, LeaseExpired, true)
	return true
}

func (r *Registry) deactivateLeaseLocked(entry *leaseEntry, status LeaseStatus, bumpEpoch bool) {
	if entry.lease.Status != LeaseActive {
		return
	}
	entry.lease.Status = status
	clear(entry.actions)
	if r.activeLeaseByComputer[entry.lease.ComputerID] == entry.lease.ID {
		delete(r.activeLeaseByComputer, entry.lease.ComputerID)
	}
	if bumpEpoch {
		r.bumpEpochLocked(entry.lease.ComputerID)
	}
}

func (r *Registry) revokeLeasesForNodeLocked(nodeID string) int {
	count := 0
	for _, entry := range r.leases {
		if entry.lease.NodeID == nodeID && entry.lease.Status == LeaseActive {
			r.deactivateLeaseLocked(entry, LeaseRevoked, true)
			count++
		}
	}
	return count
}

func (r *Registry) newLeaseIDLocked() (string, error) {
	var raw [16]byte
	for attempts := 0; attempts < 4; attempts++ {
		if _, err := io.ReadFull(r.random, raw[:]); err != nil {
			return "", err
		}
		id := "lease_" + hex.EncodeToString(raw[:])
		if _, exists := r.leases[id]; !exists {
			return id, nil
		}
	}
	return "", errors.New("nodes: id collision")
}

func snapshotNode(entry *nodeEntry) Node {
	item := entry.node
	item.AdvertisedCapabilities = sortedCapabilities(entry.advertised)
	effective := make(map[Capability]struct{}, len(entry.advertised))
	for capability := range entry.advertised {
		if effectiveCapability(entry, capability) {
			effective[capability] = struct{}{}
		}
	}
	item.EffectiveCapabilities = sortedCapabilities(effective)
	return item
}

func effectiveCapability(entry *nodeEntry, capability Capability) bool {
	if entry.node.Status != NodeActive {
		return false
	}
	if _, advertised := entry.advertised[capability]; !advertised {
		return false
	}
	return roleAllows(entry.node.Role, capability)
}

func roleAllows(role Role, capability Capability) bool {
	switch role {
	case RoleObserver:
		switch capability {
		case CapabilityStateRead, CapabilityEventsRead, CapabilityComputerView, CapabilityScreenSnapshot:
			return true
		}
	case RoleController:
		switch capability {
		case CapabilityStateRead, CapabilityEventsRead, CapabilityComputerView, CapabilityScreenSnapshot,
			CapabilityChatWrite, CapabilityComputerControl:
			return true
		}
	case RoleOwner:
		return capability.Valid()
	}
	return false
}

func normalizedCapabilities(items []Capability) ([]Capability, error) {
	seen := make(map[Capability]struct{}, len(items))
	for _, item := range items {
		if !item.Valid() {
			return nil, ErrInvalidCapability
		}
		if _, duplicate := seen[item]; duplicate {
			return nil, ErrInvalidCapability
		}
		seen[item] = struct{}{}
	}
	return sortedCapabilities(seen), nil
}

func capabilityMap(items []Capability) map[Capability]struct{} {
	result := make(map[Capability]struct{}, len(items))
	for _, item := range items {
		result[item] = struct{}{}
	}
	return result
}

func sortedCapabilities(items map[Capability]struct{}) []Capability {
	result := make([]Capability, 0, len(items))
	for item := range items {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
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

func validActionHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return false
	}
	return true
}
