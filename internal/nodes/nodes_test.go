package nodes

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
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

func (c *fakeClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func newTestRegistry(t *testing.T, clock *fakeClock) *Registry {
	t.Helper()
	registry, err := New(Config{LeaseTTL: 30 * time.Second, Clock: clock.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	return registry
}

func register(t *testing.T, registry *Registry, id string, role Role, capabilities ...Capability) Node {
	t.Helper()
	node, err := registry.Register(RegisterRequest{
		NodeID:       id,
		Platform:     PlatformIOS,
		Role:         role,
		Capabilities: capabilities,
	})
	if err != nil {
		t.Fatalf("Register(%s): %v", id, err)
	}
	return node
}

func controlLease(t *testing.T, registry *Registry, nodeID, computerID, frameID string) Lease {
	t.Helper()
	lease, err := registry.IssueLease(IssueLeaseRequest{
		NodeID: nodeID, Capability: CapabilityComputerControl, ComputerID: computerID, FrameID: frameID,
	})
	if err != nil {
		t.Fatalf("IssueLease: %v", err)
	}
	return lease
}

func actionFor(lease Lease, frameID, actionID, hash string) ActionRequest {
	return ActionRequest{
		LeaseID: lease.ID, NodeID: lease.NodeID, Capability: lease.Capability, ComputerID: lease.ComputerID,
		Epoch: lease.Epoch, FrameID: frameID, ActionID: actionID, ActionHash: hash,
	}
}

func hash(letter byte) string { return string(bytes.Repeat([]byte{letter}, 64)) }

func hasCapability(items []Capability, want Capability) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestRolesAndExplicitSensitiveAdvertisements(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	registry := newTestRegistry(t, clock)

	observer := register(t, registry, "ios-observer", RoleObserver,
		CapabilityStateRead, CapabilityScreenSnapshot, CapabilityComputerControl, CapabilityCameraCapture)
	if !hasCapability(observer.EffectiveCapabilities, CapabilityStateRead) || !hasCapability(observer.EffectiveCapabilities, CapabilityScreenSnapshot) {
		t.Fatalf("observer safe capabilities missing: %#v", observer.EffectiveCapabilities)
	}
	if hasCapability(observer.EffectiveCapabilities, CapabilityComputerControl) || hasCapability(observer.EffectiveCapabilities, CapabilityCameraCapture) {
		t.Fatalf("observer received sensitive effective capabilities: %#v", observer.EffectiveCapabilities)
	}
	if _, err := registry.IssueLease(IssueLeaseRequest{NodeID: observer.ID, Capability: CapabilityComputerControl, ComputerID: "computer-1", FrameID: "frame-1"}); !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("observer control lease error = %v", err)
	}

	controller := register(t, registry, "ios-controller", RoleController, CapabilityComputerControl)
	if !hasCapability(controller.EffectiveCapabilities, CapabilityComputerControl) {
		t.Fatalf("explicit controller toggle not effective: %#v", controller.EffectiveCapabilities)
	}
	_ = controlLease(t, registry, controller.ID, "computer-1", "frame-1")

	owner := register(t, registry, "mac-owner", RoleOwner)
	if len(owner.EffectiveCapabilities) != 0 {
		t.Fatalf("owner got implicit capabilities: %#v", owner.EffectiveCapabilities)
	}
	if _, err := registry.IssueLease(IssueLeaseRequest{NodeID: owner.ID, Capability: CapabilityComputerControl, ComputerID: "computer-2", FrameID: "frame-1"}); !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("owner without explicit toggle error = %v", err)
	}
}

func TestMacIOSAndAndroidNodesUseTheSameExplicitCapabilityModel(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	registry := newTestRegistry(t, clock)
	for _, item := range []struct {
		id       string
		platform Platform
	}{
		{id: "mac-node", platform: PlatformMac},
		{id: "ios-node", platform: PlatformIOS},
		{id: "android-node", platform: PlatformAndroid},
	} {
		node, err := registry.Register(RegisterRequest{
			NodeID: item.id, Platform: item.platform, Role: RoleObserver,
			Capabilities: []Capability{CapabilityStateRead, CapabilityCameraCapture},
		})
		if err != nil {
			t.Fatalf("Register(%s): %v", item.platform, err)
		}
		if !hasCapability(node.EffectiveCapabilities, CapabilityStateRead) || hasCapability(node.EffectiveCapabilities, CapabilityCameraCapture) {
			t.Fatalf("%s effective capabilities = %#v", item.platform, node.EffectiveCapabilities)
		}
	}
}

func TestActionAuthorizationIsBoundAndIdempotent(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	registry := newTestRegistry(t, clock)
	register(t, registry, "ios-controller", RoleController, CapabilityComputerControl)
	lease := controlLease(t, registry, "ios-controller", "computer-1", "frame-1")

	request := actionFor(lease, "frame-1", "action-1", hash('a'))
	first, err := registry.AuthorizeAction(request)
	if err != nil || !first.Authorized || first.Duplicate {
		t.Fatalf("first authorization = %#v, %v", first, err)
	}
	second, err := registry.AuthorizeAction(request)
	if err != nil || !second.Authorized || !second.Duplicate {
		t.Fatalf("replay authorization = %#v, %v", second, err)
	}

	tampered := request
	tampered.ActionHash = hash('b')
	if _, err := registry.AuthorizeAction(tampered); !errors.Is(err, ErrActionConflict) {
		t.Fatalf("same action id with different hash = %v", err)
	}
	wrongNode := request
	wrongNode.NodeID = "another-node"
	if _, err := registry.AuthorizeAction(wrongNode); !errors.Is(err, ErrLeaseBindingMismatch) {
		t.Fatalf("wrong node = %v", err)
	}
	wrongEpoch := request
	wrongEpoch.ActionID = "action-2"
	wrongEpoch.Epoch++
	if _, err := registry.AuthorizeAction(wrongEpoch); !errors.Is(err, ErrStaleControlState) {
		t.Fatalf("wrong epoch = %v", err)
	}
}

func TestFrameEpochAndExclusiveControlFailClosed(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	registry := newTestRegistry(t, clock)
	register(t, registry, "controller-a", RoleController, CapabilityComputerControl)
	register(t, registry, "controller-b", RoleController, CapabilityComputerControl)
	first := controlLease(t, registry, "controller-a", "computer-1", "frame-a")

	if _, err := registry.PublishFrame("computer-1", "frame-b"); err != nil {
		t.Fatalf("PublishFrame: %v", err)
	}
	if _, err := registry.AuthorizeAction(actionFor(first, "frame-a", "action-a", hash('a'))); !errors.Is(err, ErrStaleControlState) {
		t.Fatalf("stale frame authorization = %v", err)
	}
	if granted, err := registry.AuthorizeAction(actionFor(first, "frame-b", "action-b", hash('b'))); err != nil || !granted.Authorized {
		t.Fatalf("latest frame authorization = %#v, %v", granted, err)
	}

	second := controlLease(t, registry, "controller-b", "computer-1", "frame-c")
	if second.Epoch <= first.Epoch {
		t.Fatalf("exclusive takeover epoch = %d, want > %d", second.Epoch, first.Epoch)
	}
	old, err := registry.GetLease(first.ID)
	if err != nil || old.Status != LeaseRevoked {
		t.Fatalf("first lease after takeover = %#v, %v", old, err)
	}
	if _, err := registry.AuthorizeAction(actionFor(first, "frame-c", "action-c", hash('c'))); !errors.Is(err, ErrLeaseInactive) {
		t.Fatalf("old lease authorization = %v", err)
	}

	if _, err := registry.BumpEpoch("computer-1"); err != nil {
		t.Fatalf("BumpEpoch: %v", err)
	}
	updated, err := registry.GetLease(second.ID)
	if err != nil || updated.Status != LeaseRevoked {
		t.Fatalf("lease after epoch bump = %#v, %v", updated, err)
	}
}

func TestExpiryRenewAndExplicitRevocation(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	registry := newTestRegistry(t, clock)
	register(t, registry, "controller-a", RoleController, CapabilityComputerControl)
	register(t, registry, "controller-b", RoleController, CapabilityComputerControl)
	lease := controlLease(t, registry, "controller-a", "computer-1", "frame-a")

	if _, err := registry.RenewLease(lease.ID, "controller-b"); !errors.Is(err, ErrLeaseBindingMismatch) {
		t.Fatalf("foreign renew = %v", err)
	}
	clock.Advance(20 * time.Second)
	renewed, err := registry.RenewLease(lease.ID, "controller-a")
	if err != nil || !renewed.ExpiresAt.After(lease.ExpiresAt) || renewed.Epoch != lease.Epoch {
		t.Fatalf("renewed = %#v, %v", renewed, err)
	}
	clock.Advance(31 * time.Second)
	if _, err := registry.AuthorizeAction(actionFor(renewed, "frame-a", "action-a", hash('a'))); !errors.Is(err, ErrLeaseInactive) {
		t.Fatalf("expired authorization = %v", err)
	}
	expired, err := registry.GetLease(lease.ID)
	if err != nil || expired.Status != LeaseExpired {
		t.Fatalf("expired lease = %#v, %v", expired, err)
	}

	second := controlLease(t, registry, "controller-b", "computer-1", "frame-b")
	if _, err := registry.UpdateCapabilities("controller-b", nil); err != nil {
		t.Fatalf("UpdateCapabilities: %v", err)
	}
	revoked, err := registry.GetLease(second.ID)
	if err != nil || revoked.Status != LeaseRevoked {
		t.Fatalf("lease after control toggle off = %#v, %v", revoked, err)
	}

	if _, err := registry.UpdateCapabilities("controller-b", []Capability{CapabilityComputerControl}); err != nil {
		t.Fatalf("restore toggle: %v", err)
	}
	third := controlLease(t, registry, "controller-b", "computer-1", "frame-c")
	if _, err := registry.SetRole("controller-b", RoleObserver); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	demoted, err := registry.GetLease(third.ID)
	if err != nil || demoted.Status != LeaseRevoked {
		t.Fatalf("lease after role downgrade = %#v, %v", demoted, err)
	}

	if _, err := registry.SetRole("controller-b", RoleController); err != nil {
		t.Fatalf("restore role: %v", err)
	}
	fourth := controlLease(t, registry, "controller-b", "computer-1", "frame-d")
	directlyRevoked, err := registry.RevokeLease(fourth.ID)
	if err != nil || directlyRevoked.Status != LeaseRevoked {
		t.Fatalf("RevokeLease = %#v, %v", directlyRevoked, err)
	}
	if _, err := registry.AuthorizeAction(actionFor(fourth, "frame-d", "action-d", hash('d'))); !errors.Is(err, ErrLeaseInactive) {
		t.Fatalf("directly revoked authorization = %v", err)
	}
}

func TestRevokeNodeAndCleanupExpired(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	registry := newTestRegistry(t, clock)
	register(t, registry, "controller-a", RoleController, CapabilityComputerControl)
	first := controlLease(t, registry, "controller-a", "computer-1", "frame-a")
	clock.Advance(31 * time.Second)
	count, err := registry.CleanupExpired()
	if err != nil || count != 1 {
		t.Fatalf("CleanupExpired = %d, %v", count, err)
	}

	if _, err := registry.UpdateCapabilities("controller-a", []Capability{CapabilityComputerControl}); err != nil {
		t.Fatalf("UpdateCapabilities: %v", err)
	}
	second := controlLease(t, registry, "controller-a", "computer-1", "frame-b")
	node, err := registry.RevokeNode("controller-a")
	if err != nil || node.Status != NodeRevoked {
		t.Fatalf("RevokeNode = %#v, %v", node, err)
	}
	if _, err := registry.RenewLease(second.ID, "controller-a"); !errors.Is(err, ErrLeaseInactive) {
		t.Fatalf("renew revoked lease = %v", err)
	}
	if _, err := registry.UpdateCapabilities("controller-a", []Capability{CapabilityComputerControl}); !errors.Is(err, ErrNodeRevoked) {
		t.Fatalf("update revoked node = %v", err)
	}
	if _, err := registry.GetLease(first.ID); err != nil {
		t.Fatalf("lease history unavailable: %v", err)
	}
}

func TestConcurrentSameActionHasOneDispatchAndSafeDuplicates(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	registry := newTestRegistry(t, clock)
	register(t, registry, "controller-a", RoleController, CapabilityComputerControl)
	lease := controlLease(t, registry, "controller-a", "computer-1", "frame-a")
	request := actionFor(lease, "frame-a", "same-action", hash('a'))

	var firstCount atomic.Int32
	var duplicateCount atomic.Int32
	var failures atomic.Int32
	start := make(chan struct{})
	var group sync.WaitGroup
	for range 64 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := registry.AuthorizeAction(request)
			if err != nil || !result.Authorized {
				failures.Add(1)
				return
			}
			if result.Duplicate {
				duplicateCount.Add(1)
			} else {
				firstCount.Add(1)
			}
		}()
	}
	close(start)
	group.Wait()
	if failures.Load() != 0 || firstCount.Load() != 1 || duplicateCount.Load() != 63 {
		t.Fatalf("results first=%d duplicate=%d failures=%d", firstCount.Load(), duplicateCount.Load(), failures.Load())
	}
}

func TestValidationAndPersistenceBoundary(t *testing.T) {
	for _, ttl := range []time.Duration{time.Nanosecond, MaxLeaseTTL + time.Second} {
		if _, err := New(Config{LeaseTTL: ttl}); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("New(%s) error = %v", ttl, err)
		}
	}
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	registry := newTestRegistry(t, clock)
	if _, err := registry.Register(RegisterRequest{NodeID: "bad value", Platform: PlatformIOS, Role: RoleObserver}); !errors.Is(err, ErrInvalidNodeID) {
		t.Fatalf("bad node id = %v", err)
	}
	if _, err := registry.Register(RegisterRequest{NodeID: "bad-platform", Platform: Platform("linux"), Role: RoleObserver}); !errors.Is(err, ErrInvalidPlatform) {
		t.Fatalf("bad platform = %v", err)
	}
	if _, err := registry.Register(RegisterRequest{NodeID: "bad-capability", Platform: PlatformAndroid, Role: RoleObserver, Capabilities: []Capability{"shell.root"}}); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("bad capability = %v", err)
	}
	if _, err := registry.Register(RegisterRequest{NodeID: "duplicate-capability", Platform: PlatformMac, Role: RoleOwner, Capabilities: []Capability{CapabilityStateRead, CapabilityStateRead}}); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("duplicate capability = %v", err)
	}
	if _, err := registry.PublishFrame("computer-1", "frame with spaces"); !errors.Is(err, ErrInvalidFrameID) {
		t.Fatalf("bad frame id = %v", err)
	}

	if payload, err := json.Marshal(registry); !errors.Is(err, ErrPersistenceForbidden) || payload != nil {
		t.Fatalf("json.Marshal registry = %q, %v", payload, err)
	}
	var gobBuffer bytes.Buffer
	if err := gob.NewEncoder(&gobBuffer).Encode(registry); !errors.Is(err, ErrPersistenceForbidden) {
		t.Fatalf("gob encode registry = %v", err)
	}
	if got := fmt.Sprintf("%v", registry); got != "NodeRegistry{state=[redacted]}" {
		t.Fatalf("String = %q", got)
	}

	if err := registry.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := registry.ListNodes(); !errors.Is(err, ErrClosed) {
		t.Fatalf("ListNodes after Close = %v", err)
	}
}
