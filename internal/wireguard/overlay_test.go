package wireguard

import (
	"errors"
	"testing"
)

func TestNextOverlayAddressExcludesNetworkAndBroadcast(t *testing.T) {
	// 10.42.0.0/30 has exactly two usable host addresses: .1 and .2. .0 is
	// the network address and .3 is the broadcast address; neither must ever
	// be returned.
	taken := map[string]bool{}
	first, err := NextOverlayAddress([]string{"10.42.0.0/30"}, taken)
	if err != nil {
		t.Fatal(err)
	}
	if first != "10.42.0.1" {
		t.Fatalf("expected first usable address 10.42.0.1, got %q", first)
	}
	taken[first] = true
	second, err := NextOverlayAddress([]string{"10.42.0.0/30"}, taken)
	if err != nil {
		t.Fatal(err)
	}
	if second != "10.42.0.2" {
		t.Fatalf("expected second usable address 10.42.0.2, got %q", second)
	}
	taken[second] = true
	if _, err := NextOverlayAddress([]string{"10.42.0.0/30"}, taken); !errors.Is(err, ErrOverlayAddressesExhausted) {
		t.Fatalf("expected exhaustion once .1 and .2 are both taken (network .0 and broadcast .3 must never be handed out), got %v", err)
	}
}

func TestNextOverlayAddressAvoidsAlreadyTakenAddresses(t *testing.T) {
	// A collision-avoidance regression: an address already assigned to
	// another currently-active allocation must never be handed out again,
	// even though it is otherwise the numerically-first candidate.
	taken := map[string]bool{"10.42.0.1": true}
	addr, err := NextOverlayAddress([]string{"10.42.0.0/24"}, taken)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "10.42.0.2" {
		t.Fatalf("expected the allocator to skip the already-taken .1 and return .2, got %q", addr)
	}
}

func TestNextOverlayAddressExhaustionAcrossFullCIDR(t *testing.T) {
	// A /30 pool has exactly two usable addresses; once both are recorded as
	// taken, exhaustion must be reported clearly rather than hanging,
	// panicking, or silently returning a reused/invalid address.
	taken := map[string]bool{"10.42.0.1": true, "10.42.0.2": true}
	if _, err := NextOverlayAddress([]string{"10.42.0.0/30"}, taken); !errors.Is(err, ErrOverlayAddressesExhausted) {
		t.Fatalf("expected ErrOverlayAddressesExhausted for a fully-taken CIDR, got %v", err)
	}
}

func TestNextOverlayAddressFallsThroughToLaterCIDRs(t *testing.T) {
	// A NetworkMapping can carry more than one CIDR; once the first is fully
	// taken, the allocator must fall through to the next one in order rather
	// than reporting exhaustion prematurely.
	taken := map[string]bool{"10.42.0.1": true, "10.42.0.2": true}
	addr, err := NextOverlayAddress([]string{"10.42.0.0/30", "10.43.0.0/30"}, taken)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "10.43.0.1" {
		t.Fatalf("expected fallthrough to the second CIDR's first usable address 10.43.0.1, got %q", addr)
	}
}

func TestNextOverlayAddressRejectsInvalidCIDR(t *testing.T) {
	if _, err := NextOverlayAddress([]string{"not-a-cidr"}, map[string]bool{}); err == nil {
		t.Fatal("expected an error for an invalid CIDR, got nil")
	}
}

func TestNextOverlayAddressDeterministicAcrossRepeatedCalls(t *testing.T) {
	// Given the same taken set, two separate calls must return the same
	// address - determinism is what lets the caller reason about
	// idempotency and testability without introducing randomness.
	taken := map[string]bool{}
	a, err := NextOverlayAddress([]string{"10.42.0.0/24"}, taken)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NextOverlayAddress([]string{"10.42.0.0/24"}, taken)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("expected deterministic allocation for an unchanged taken set, got %q then %q", a, b)
	}
}
