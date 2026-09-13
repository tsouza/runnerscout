package agent

import (
	"encoding/base64"
	"errors"
	"sync"
	"testing"

	"github.com/tsouza/runnerscout/internal/wireguard"
	"github.com/tsouza/runnerscout/internal/wireguard/tunnel"
)

// fakeTunnel is a minimal, in-memory stand-in for the Tunnel interface
// Reconcile depends on - never a real device.Device/netstack instance, per
// this task's TDD requirement to exercise reconciliation logic against a
// fake Tunnel-shaped interface rather than the real (slow, netstack-backed)
// tunnel.Tunnel. It is safe for concurrent use (a mutex guards added/
// removed) so TestRun* below can poll it from the test goroutine while
// Run's own goroutine calls it concurrently.
type fakeTunnel struct {
	mu             sync.Mutex
	added, removed []wireguard.PublicKey
	failAdd        map[wireguard.PublicKey]bool
	failRemove     map[wireguard.PublicKey]bool
}

func (f *fakeTunnel) AddPeer(p tunnel.Peer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAdd[p.PublicKey] {
		return errors.New("fixture: add failed")
	}
	f.added = append(f.added, p.PublicKey)
	return nil
}

func (f *fakeTunnel) RemovePeer(pub wireguard.PublicKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRemove[pub] {
		return errors.New("fixture: remove failed")
	}
	f.removed = append(f.removed, pub)
	return nil
}

// snapshot returns copies of added/removed for a concurrent reader (a test
// goroutine polling for Run's loop to make progress) without racing the
// slices themselves.
func (f *fakeTunnel) snapshot() (added, removed []wireguard.PublicKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]wireguard.PublicKey(nil), f.added...), append([]wireguard.PublicKey(nil), f.removed...)
}

// fakeCloser adds a no-op Close to *fakeTunnel, satisfying the Closer
// interface Run's BringUp hook returns - a real *tunnel.Tunnel's Close
// releases its device/UDP socket; this fixture has nothing to release.
type fakeCloser struct{ *fakeTunnel }

func (fakeCloser) Close() error { return nil }

func peerFixture(id string, pub wireguard.PublicKey, overlay, endpoint string) wireguard.Peer {
	return wireguard.Peer{
		AllocationID:   id,
		PublicKey:      base64.StdEncoding.EncodeToString(pub[:]),
		OverlayAddress: overlay,
		Endpoint:       endpoint,
	}
}

func keyFixture(b byte) wireguard.PublicKey {
	var k wireguard.PublicKey
	k[0] = b
	return k
}

func TestReconcileAddsNewPeersFromEmptyCurrent(t *testing.T) {
	pubA, pubB := keyFixture(1), keyFixture(2)
	next := []wireguard.Peer{
		peerFixture("rs-a", pubA, "10.60.0.1", "10.0.0.1:51820"),
		peerFixture("rs-b", pubB, "10.60.0.2", ""),
	}
	ft := &fakeTunnel{}
	updated, err := Reconcile(ft, nil, next)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(updated) != 2 || updated[pubA].AllocationID != "rs-a" || updated[pubB].AllocationID != "rs-b" {
		t.Fatalf("unexpected updated set: %+v", updated)
	}
	if len(ft.added) != 2 {
		t.Fatalf("expected 2 AddPeer calls, got %d", len(ft.added))
	}
	if len(ft.removed) != 0 {
		t.Fatalf("expected no RemovePeer calls, got %d", len(ft.removed))
	}
}

func TestReconcileRemovesPeersMissingFromNext(t *testing.T) {
	pubA, pubB := keyFixture(1), keyFixture(2)
	current := map[wireguard.PublicKey]wireguard.Peer{
		pubA: peerFixture("rs-a", pubA, "10.60.0.1", ""),
	}
	next := []wireguard.Peer{peerFixture("rs-b", pubB, "10.60.0.2", "")}
	ft := &fakeTunnel{}
	updated, err := Reconcile(ft, current, next)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, stillPresent := updated[pubA]; stillPresent {
		t.Fatal("revoked peer A still present in updated set")
	}
	if _, present := updated[pubB]; !present {
		t.Fatal("newly appeared peer B missing from updated set")
	}
	if len(ft.removed) != 1 || ft.removed[0] != pubA {
		t.Fatalf("expected RemovePeer(A), got %+v", ft.removed)
	}
	if len(ft.added) != 1 || ft.added[0] != pubB {
		t.Fatalf("expected AddPeer(B), got %+v", ft.added)
	}
}

// TestReconcileIsANoOpWhenMembershipUnchanged proves Reconcile never calls
// AddPeer/RemovePeer for a peer that is already configured and still
// present - add/remove only, no in-place update, matching this task's
// "plain, minimal poll loop" scope.
func TestReconcileIsANoOpWhenMembershipUnchanged(t *testing.T) {
	pubA := keyFixture(1)
	peer := peerFixture("rs-a", pubA, "10.60.0.1", "10.0.0.1:51820")
	current := map[wireguard.PublicKey]wireguard.Peer{pubA: peer}
	ft := &fakeTunnel{}
	updated, err := Reconcile(ft, current, []wireguard.Peer{peer})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ft.added) != 0 || len(ft.removed) != 0 {
		t.Fatalf("expected no tunnel calls for unchanged membership, got added=%v removed=%v", ft.added, ft.removed)
	}
	if len(updated) != 1 || updated[pubA] != peer {
		t.Fatalf("unexpected updated set: %+v", updated)
	}
}

// TestReconcileSkipsMalformedPeerButProcessesOthers proves one bad entry
// from the controller (never expected in practice, but never trusted
// blindly either) does not abort reconciliation of every other peer in the
// same response.
func TestReconcileSkipsMalformedPeerButProcessesOthers(t *testing.T) {
	pubB := keyFixture(2)
	malformed := wireguard.Peer{AllocationID: "rs-bad", PublicKey: "not-valid-base64!!", OverlayAddress: "10.60.0.9"}
	next := []wireguard.Peer{malformed, peerFixture("rs-b", pubB, "10.60.0.2", "")}
	ft := &fakeTunnel{}
	updated, err := Reconcile(ft, nil, next)
	if err == nil {
		t.Fatal("expected an error for the malformed peer")
	}
	if len(updated) != 1 || updated[pubB].AllocationID != "rs-b" {
		t.Fatalf("well-formed peer was not still processed: %+v", updated)
	}
}

// TestReconcileRetainsFailedAddForNextAttempt proves a peer whose AddPeer
// call fails is not recorded as configured - a self-healing property, since
// the next poll's Reconcile call will attempt to add it again as long as
// the controller still reports it.
func TestReconcileRetainsFailedAddForNextAttempt(t *testing.T) {
	pubA := keyFixture(1)
	ft := &fakeTunnel{failAdd: map[wireguard.PublicKey]bool{pubA: true}}
	updated, err := Reconcile(ft, nil, []wireguard.Peer{peerFixture("rs-a", pubA, "10.60.0.1", "")})
	if err == nil {
		t.Fatal("expected an error from the failing AddPeer call")
	}
	if _, present := updated[pubA]; present {
		t.Fatal("a peer whose AddPeer call failed must not be recorded as configured")
	}
}

// TestReconcileRetainsFailedRemoveForNextAttempt mirrors
// TestReconcileRetainsFailedAddForNextAttempt for RemovePeer: a peer whose
// removal fails must still be considered "current" so the next reconcile
// pass retries removing it, rather than the agent silently losing track of
// a peer it believes is gone but is actually still configured on the
// device.
func TestReconcileRetainsFailedRemoveForNextAttempt(t *testing.T) {
	pubA := keyFixture(1)
	peer := peerFixture("rs-a", pubA, "10.60.0.1", "")
	current := map[wireguard.PublicKey]wireguard.Peer{pubA: peer}
	ft := &fakeTunnel{failRemove: map[wireguard.PublicKey]bool{pubA: true}}
	updated, err := Reconcile(ft, current, nil)
	if err == nil {
		t.Fatal("expected an error from the failing RemovePeer call")
	}
	if _, present := updated[pubA]; !present {
		t.Fatal("a peer whose RemovePeer call failed must remain tracked as still-configured")
	}
}
