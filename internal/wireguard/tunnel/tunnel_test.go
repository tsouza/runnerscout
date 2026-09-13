package tunnel

import (
	"net/netip"
	"testing"

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
