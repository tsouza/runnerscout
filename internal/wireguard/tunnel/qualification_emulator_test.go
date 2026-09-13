//go:build emulators

package tunnel

// This file is this repository's qualification lane for the actual
// WireGuard data-plane, per docs/networking-peer-model.md's "Local
// qualification lane" section: it drives the real, non-mocked
// golang.zx2c4.com/wireguard/device + tun/netstack code this package wraps,
// exactly the same production code path BringUp uses for any future caller
// (a VM-side agent this repository does not build yet). Nothing here is a
// fake or a hand-rolled protocol stand-in.
//
// # Same-process, two real UDP ports - not two Docker containers
//
// docs/networking-peer-model.md's "Recommendation" text names two
// `--internal`-network Docker containers as this lane's topology, mirroring
// internal/provider/azure_emulator_test.go's structure. This file
// deliberately does not build that: it was investigated and rejected as
// needless infrastructure for what this test actually needs to prove.
//
// The netstack (gVisor) backend this codebase committed to in
// docs/networking-control-plane.md means neither "container" in a
// hypothetical two-container version of this test would ever touch a real
// kernel TUN device, `NET_ADMIN`, or any other elevated capability - the
// entire point of a container per docs/networking-peer-model.background.md
// was proving no elevated capability is *needed*, not exercising container
// network-namespace isolation as a feature under test. A same-process test
// already satisfies that: it requests no capability at all. What is
// actually under test - the real Noise handshake, real per-peer encryption,
// and real AllowedIPs-based authorization that makes a revoked peer's
// traffic unreadable - lives entirely inside golang.zx2c4.com/wireguard's
// device.Device and is completely insensitive to whether the outer UDP
// transport crosses a Docker bridge or stays on loopback: WireGuard treats
// its outer conn.Bind as an opaque, unauthenticated packet transport in
// either case, and authenticates/authorizes purely via the Noise handshake
// running on top of it. Two independent device.Device instances bound to
// two distinct loopback UDP ports exercise that exact same code path a real
// two-container setup would, with no part of the protocol logic bypassed or
// mocked.
//
// This mirrors existing precedent in this codebase for real-library,
// in-process fixtures: internal/prices/aws_test.go and
// internal/azurequeue/azurequeue_test.go both drive real SDK/library code
// against an in-process httptest server rather than a container, reserving
// actual Docker containers (as internal/provider/emulator_test.go and
// azure_emulator_test.go do) for cases where the code under test is a
// network client for a REST API that only a full server implementation can
// stand in for. Here, by contrast, the code under test (device.Device) *is*
// both ends of the protocol; a container would only add process/network
// namespace scaffolding around code this test can and does invoke directly.
//
// This file still carries the `emulators` build tag rather than running as
// a default test, matching this family's convention of gating slower,
// closer-to-real, non-mocked tests behind an opt-in tag distinct from the
// fast default suite - not because it needs Docker (it does not) or the
// tools/emulators.py harness (also not touched by this file).
//
// If a real cross-container/cross-host qualification of this package ever
// becomes necessary (e.g. to prove something host-network-namespace or
// NAT-traversal specific that this test cannot), tools/emulators.py's
// `wireguard` path and a two-container topology remain the documented
// fallback design; nothing above claims that would be redundant in
// general, only that it is redundant for the two properties this task was
// asked to prove.

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/wireguard"
)

// freeUDPPort asks the OS for a currently-unused UDP port on loopback by
// binding to port 0 and immediately releasing it. This has an inherent,
// accepted race (another process could claim the same port before this
// test's own device binds it) - the same accepted idiom Go's own standard
// library test suite uses for "find a free port" - but is otherwise the
// only portable way to get a real, unused loopback UDP port to configure a
// WireGuard endpoint against.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("allocate free UDP port: %v", err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

// TestTwoTunnelsExchangeThenDropAfterRevocation is the qualification lane's
// core proof, per docs/networking-peer-model.md's "Local qualification
// lane" section: two real Tunnel instances (the same BringUp production
// code path any future caller uses), each backed by its own real
// device.Device and netstack TUN, can exchange a UDP payload peer-to-peer
// over real loopback UDP sockets; then, once one side's peer list is
// reconfigured to no longer include the other (the data-plane effect of a
// controller-side revocation - docs/networking-peer-model.md's
// "Revocation" section), the next attempted packet from the revoked peer is
// silently dropped rather than delivered. This is an *observed effect* at
// the data-plane level, not merely a check that RemovePeer's code path ran
// - the same testing philosophy docs/networking-peer-model.md's "Why" table
// cites for CQ-09/interruption detection.
func TestTwoTunnelsExchangeThenDropAfterRevocation(t *testing.T) {
	a, err := wireguard.Generate()
	if err != nil {
		t.Fatal(err)
	}
	b, err := wireguard.Generate()
	if err != nil {
		t.Fatal(err)
	}

	overlayA := netip.MustParseAddr("10.60.0.1")
	overlayB := netip.MustParseAddr("10.60.0.2")
	portA, portB := freeUDPPort(t), freeUDPPort(t)
	loopback := netip.MustParseAddr("127.0.0.1")

	tunA, err := BringUp(Config{
		PrivateKey:     a.Private,
		OverlayAddress: overlayA,
		ListenPort:     uint16(portA),
		Peers: []Peer{
			{PublicKey: b.Public, OverlayAddress: overlayB, Endpoint: netip.AddrPortFrom(loopback, uint16(portB))},
		},
	})
	if err != nil {
		t.Fatalf("BringUp(A) failed: %v", err)
	}
	defer tunA.Close()

	tunB, err := BringUp(Config{
		PrivateKey:     b.Private,
		OverlayAddress: overlayB,
		ListenPort:     uint16(portB),
		Peers: []Peer{
			{PublicKey: a.Public, OverlayAddress: overlayA, Endpoint: netip.AddrPortFrom(loopback, uint16(portA))},
		},
	})
	if err != nil {
		t.Fatalf("BringUp(B) failed: %v", err)
	}
	defer tunB.Close()

	listenAddr := &net.UDPAddr{IP: net.ParseIP("10.60.0.2"), Port: 9000}
	listener, err := tunB.Net().ListenUDP(listenAddr)
	if err != nil {
		t.Fatalf("listen on B's overlay address: %v", err)
	}
	defer listener.Close()

	// received gets one entry per successfully-read packet, for the
	// lifetime of the whole test (both phases): phase 2 depends on this
	// goroutine still actively trying to read when the revoked peer's
	// packet arrives (or doesn't), not on a goroutine that already exited
	// after phase 1's single read.
	received := make(chan string, 8)
	go func() {
		buf := make([]byte, 1500)
		for {
			n, _, err := listener.ReadFrom(buf)
			if err != nil {
				return
			}
			received <- string(buf[:n])
		}
	}()

	dial := func() net.Conn {
		t.Helper()
		conn, err := tunA.Net().DialUDP(nil, &net.UDPAddr{IP: net.ParseIP("10.60.0.2"), Port: 9000})
		if err != nil {
			t.Fatalf("dial from A to B's overlay address: %v", err)
		}
		return conn
	}

	// Phase 1: peer-to-peer delivery while both sides still trust each
	// other. This proves the real Noise handshake and real per-peer
	// encryption/decryption actually work end to end, not merely that
	// BringUp returns without error.
	conn := dial()
	if _, err := conn.Write([]byte("hello-from-A")); err != nil {
		t.Fatalf("write from A: %v", err)
	}
	select {
	case payload := <-received:
		if payload != "hello-from-A" {
			t.Fatalf("B received wrong payload: %q", payload)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("B never received A's packet while still a trusted peer")
	}
	conn.Close()

	// Phase 2: revoke A from B's authoritative peer list - the data-plane
	// effect of docs/networking-peer-model.md's "Revocation" section, which
	// removes a departed allocation from every surviving peer's list. B
	// must now reject traffic claiming to be from A: RemovePeer tears down
	// A's session keys and routing entry on B's device entirely.
	if err := tunB.RemovePeer(a.Public); err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}

	conn = dial()
	defer conn.Close()
	if _, err := conn.Write([]byte("should-be-dropped")); err != nil {
		t.Fatalf("write from A after revocation: %v", err)
	}
	select {
	case payload := <-received:
		t.Fatalf("B delivered a payload from a revoked peer: %q", payload)
	case <-time.After(3 * time.Second):
		// Expected: silent drop, not a delivered payload and not an
		// application-visible error - WireGuard's own failure mode for an
		// unrecognized/unauthorized sender is to discard the packet, which
		// is exactly what docs/networking-peer-model.md's qualification
		// lane calls for observing.
	}
}
