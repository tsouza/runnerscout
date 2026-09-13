// Package agent implements the VM-side WireGuard poll-and-reconfigure loop:
// the piece that runs on a wireguard-mode runner VM at boot, reading the
// cloud-init-delivered internal/wireguard.CloudInitPayload, bringing up a
// real tunnel via internal/wireguard/tunnel.BringUp, and periodically
// polling internal/health.WireGuardPeersHandler's endpoint to converge its
// peer set with the controller's authoritative list
// (docs/networking-peer-model.md's "Revocation" section).
//
// This is a separate package (and, via cmd/runnerscout-wireguard-agent, a
// separate binary), never imported by cmd/runnerscout, for the same
// dependency-isolation reason internal/wireguard/tunnel is its own
// subpackage of internal/wireguard: it depends on internal/wireguard/tunnel,
// which pulls in golang.zx2c4.com/wireguard/tun/netstack's gVisor
// dependency tree - see internal/wireguard/tunnel's own package doc comment.
package agent

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"

	"github.com/tsouza/runnerscout/internal/wireguard"
	"github.com/tsouza/runnerscout/internal/wireguard/tunnel"
)

// Tunnel is the minimal surface Reconcile needs from a *tunnel.Tunnel: add a
// newly appeared peer, remove one no longer present. Declared locally
// (never tunnel.Tunnel itself) so this package's tests exercise
// reconciliation logic against a plain in-memory fake, never a real
// device.Device/netstack instance - the same "provably no wider than what
// it actually uses" discipline internal/health.AllocationStore already
// established for a comparable dependency-narrowing interface.
type Tunnel interface {
	AddPeer(tunnel.Peer) error
	RemovePeer(wireguard.PublicKey) error
}

// DecodePeer converts one wireguard.Peer - the poll endpoint's wire shape,
// wireguard.Snapshot's exact output - into the typed tunnel.Peer
// tunnel.Tunnel's AddPeer/BringUp need: PublicKey base64-decoded,
// OverlayAddress parsed, and Endpoint parsed when present (left as the zero
// netip.AddrPort when p.Endpoint == "", which tunnel.Peer's own doc comment
// already documents as "not yet captured for this peer, fall back to
// WireGuard's roaming behavior" - never an error on its own). Any other
// malformed field is a hard error: this agent never guesses at a peer's
// identity or address.
func DecodePeer(p wireguard.Peer) (tunnel.Peer, error) {
	raw, err := base64.StdEncoding.DecodeString(p.PublicKey)
	if err != nil || len(raw) != wireguard.KeySize {
		return tunnel.Peer{}, fmt.Errorf("agent: peer %s has a malformed public key: %v", p.AllocationID, err)
	}
	var pub wireguard.PublicKey
	copy(pub[:], raw)
	overlay, err := netip.ParseAddr(p.OverlayAddress)
	if err != nil {
		return tunnel.Peer{}, fmt.Errorf("agent: peer %s has a malformed overlay address %q: %w", p.AllocationID, p.OverlayAddress, err)
	}
	result := tunnel.Peer{PublicKey: pub, OverlayAddress: overlay}
	if p.Endpoint != "" {
		endpoint, err := netip.ParseAddrPort(p.Endpoint)
		if err != nil {
			return tunnel.Peer{}, fmt.Errorf("agent: peer %s has a malformed endpoint %q: %w", p.AllocationID, p.Endpoint, err)
		}
		result.Endpoint = endpoint
	}
	return result, nil
}

// Reconcile converges t's configured peer set from current (what this
// agent's own last successful reconcile pass left configured - the zero
// value/nil for the very first call, after BringUp's own initial peer list
// has already been folded in by the caller) to next (a freshly polled
// snapshot from internal/health.WireGuardPeersHandler). It calls t.AddPeer
// for every peer in next not already in current, and t.RemovePeer for every
// peer in current no longer present in next - add/remove only, never an
// in-place update of an already-configured peer's fields, matching this
// task's "plain, minimal poll loop" scope and
// docs/networking-peer-model.md's "Revocation" design (a peer's identity is
// fixed for its allocation's lifetime; only membership changes).
//
// The returned map reflects what is actually configured on t after this
// call, not what next asked for: a peer whose AddPeer/RemovePeer call
// failed keeps its prior state (absent if it was never added; still present
// if its removal failed) so the next Reconcile call - given the same or a
// refreshed next - retries exactly the peers still out of sync, without an
// exponential-backoff/retry framework of its own; "try again next poll
// interval" is the only retry policy this needs. A malformed peer in next
// (DecodePeer failure) is skipped, not fatal to the rest of the batch.
// Every error encountered (malformed peers, failed Add/RemovePeer calls) is
// aggregated with errors.Join and returned; the caller decides whether/how
// to log it.
func Reconcile(t Tunnel, current map[wireguard.PublicKey]wireguard.Peer, next []wireguard.Peer) (map[wireguard.PublicKey]wireguard.Peer, error) {
	type wanted struct {
		peer   wireguard.Peer
		tunnel tunnel.Peer
	}
	wantedByKey := make(map[wireguard.PublicKey]wanted, len(next))
	var errs []error
	for _, p := range next {
		decoded, err := DecodePeer(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		wantedByKey[decoded.PublicKey] = wanted{peer: p, tunnel: decoded}
	}

	updated := make(map[wireguard.PublicKey]wireguard.Peer, len(current))
	for pub, peer := range current {
		updated[pub] = peer
	}

	for pub, w := range wantedByKey {
		if _, already := current[pub]; already {
			continue
		}
		if err := t.AddPeer(w.tunnel); err != nil {
			errs = append(errs, fmt.Errorf("agent: add peer %s: %w", w.peer.AllocationID, err))
			continue
		}
		updated[pub] = w.peer
	}
	for pub, peer := range current {
		if _, stillWanted := wantedByKey[pub]; stillWanted {
			continue
		}
		if err := t.RemovePeer(pub); err != nil {
			errs = append(errs, fmt.Errorf("agent: remove peer %s: %w", peer.AllocationID, err))
			continue
		}
		delete(updated, pub)
	}
	return updated, errors.Join(errs...)
}
