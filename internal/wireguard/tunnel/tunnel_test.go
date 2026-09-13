package tunnel

import (
	"net/netip"
	"testing"

	wgdevice "golang.zx2c4.com/wireguard/device"

	"github.com/tsouza/runnerscout/internal/wireguard"
)

// TestBringUpRejectsInvalidOverlayAddress proves BringUp validates its
// config before touching any real device/netstack machinery: a zero-value
// (invalid) OverlayAddress can never be assigned to the TUN interface, so
// BringUp must refuse it up front with a clear error rather than failing
// deeper inside netstack.CreateNetTUN with a confusing message.
func TestBringUpRejectsInvalidOverlayAddress(t *testing.T) {
	kp, err := wireguard.Generate()
	if err != nil {
		t.Fatal(err)
	}
	_, err = BringUp(Config{PrivateKey: kp.Private})
	if err == nil {
		t.Fatal("expected an error for a zero-value OverlayAddress, got nil")
	}
}

// TestBringUpAndCloseWithNoPeers proves the minimal happy path: a real
// userspace WireGuard device, backed by the real netstack (gVisor) TUN
// implementation, can be brought up with zero peers and cleanly closed.
// This does not prove two tunnels can exchange traffic (see the
// //go:build emulators qualification test for that) - it only proves
// BringUp/Close do not error against the real library for the simplest
// possible configuration.
func TestBringUpAndCloseWithNoPeers(t *testing.T) {
	kp, err := wireguard.Generate()
	if err != nil {
		t.Fatal(err)
	}
	tun, err := BringUp(Config{
		PrivateKey:     kp.Private,
		OverlayAddress: netip.MustParseAddr("10.60.0.1"),
	})
	if err != nil {
		t.Fatalf("BringUp with no peers failed: %v", err)
	}
	if tun == nil {
		t.Fatal("BringUp returned a nil Tunnel with a nil error")
	}
	if err := tun.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	// Close must be idempotent - callers (including a deferred Close after
	// an early explicit one in the qualification test) must never panic or
	// error on a second call.
	if err := tun.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
}

// TestAddPeerRejectsInvalidOverlayAddress mirrors BringUp's own validation
// (TestBringUpRejectsInvalidOverlayAddress) for the incremental single-peer
// path: a peer with no overlay address can never produce a usable
// AllowedIPs entry, so AddPeer must refuse it up front rather than handing
// device.Device.IpcSet a malformed config line.
func TestAddPeerRejectsInvalidOverlayAddress(t *testing.T) {
	kp, err := wireguard.Generate()
	if err != nil {
		t.Fatal(err)
	}
	tun, err := BringUp(Config{PrivateKey: kp.Private, OverlayAddress: netip.MustParseAddr("10.60.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	peer, err := wireguard.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := tun.AddPeer(Peer{PublicKey: peer.Public}); err == nil {
		t.Fatal("expected an error for a peer with no OverlayAddress, got nil")
	}
}

// TestAddPeerConfiguresANewPeerWithoutDisturbingExisting proves AddPeer
// (device.Device.IpcSet with no "replace_peers=true" line - see AddPeer's
// doc comment) adds a peer incrementally: an already-configured peer from
// BringUp's own initial Peers list must still be present and still
// recognized by the device (LookupPeer) after AddPeer configures a second,
// different peer. This does not itself prove two tunnels can exchange
// traffic through an AddPeer-added peer (see the //go:build emulators
// qualification test for that observed-effect proof) - it only proves
// AddPeer does not clobber the device's existing peer set.
func TestAddPeerConfiguresANewPeerWithoutDisturbingExisting(t *testing.T) {
	kp, err := wireguard.Generate()
	if err != nil {
		t.Fatal(err)
	}
	existing, err := wireguard.Generate()
	if err != nil {
		t.Fatal(err)
	}
	tun, err := BringUp(Config{
		PrivateKey:     kp.Private,
		OverlayAddress: netip.MustParseAddr("10.60.0.1"),
		Peers:          []Peer{{PublicKey: existing.Public, OverlayAddress: netip.MustParseAddr("10.60.0.2")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	added, err := wireguard.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := tun.AddPeer(Peer{PublicKey: added.Public, OverlayAddress: netip.MustParseAddr("10.60.0.3")}); err != nil {
		t.Fatalf("AddPeer failed: %v", err)
	}
	if tun.dev.LookupPeer(wgdevice.NoisePublicKey(existing.Public)) == nil {
		t.Fatal("AddPeer removed or lost the peer BringUp already configured")
	}
	if tun.dev.LookupPeer(wgdevice.NoisePublicKey(added.Public)) == nil {
		t.Fatal("AddPeer did not configure the new peer")
	}
}
