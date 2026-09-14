package operator

import (
	"context"
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/placement"
	"github.com/tsouza/runnerscout/internal/provider"
	"github.com/tsouza/runnerscout/internal/state"
	"github.com/tsouza/runnerscout/internal/wireguard"
	"k8s.io/client-go/kubernetes/fake"
)

// overlayTestCIDR is the overlay address pool every wireguard-mode test
// fixture in this file shares. It deliberately contains
// runningAllocation's own hardcoded "10.90.0.1" as its first usable
// address, so that a test asserting collision avoidance against an
// already-Running peer exercises the allocator actually skipping it, not
// merely picking a disjoint range by accident.
const overlayTestCIDR = "10.90.0.0/24"

func networkConfig(networkProfile string) Config {
	c := Config{Name: "test", Namespace: "test", GitHubURL: "https://github.com/tsouza/runnerscout", ScaleSetID: 1, MaxRunners: 10, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600,
		Requirements:   placement.Requirements{CPU: 1, MemoryMiB: 1, Architecture: "amd64", MaxPriceMicros: 100, Providers: []string{"aws"}, Regions: []string{"r"}, Policy: "lowest-price"},
		Catalog:        placement.Catalog{Complete: map[string]bool{"aws": true}},
		Providers:      map[string]provider.Config{"aws": {Kind: "aws", Owner: "test", AccountID: "000000000000", Subnet: "private", SecurityGroup: "private"}},
		NetworkProfile: networkProfile,
	}
	if networkProfile != "" {
		c.NetworkOverlayCIDRs = []string{overlayTestCIDR}
	}
	return c
}

// TestHandleDesiredRunnerCountSetsNetworkProfileOnNewAllocations is the
// end-to-end proof that a compiled NetworkProfile identity actually reaches
// a real lifecycle.Allocation: before this task nothing in this codebase's
// configuration path could ever produce a non-empty
// lifecycle.Allocation.NetworkProfile.
func TestHandleDesiredRunnerCountSetsNetworkProfileOnNewAllocations(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := networkConfig("mesh")
	cfg.Catalog.Offerings = []placement.Offering{{ID: "pool", Provider: "aws", Region: "r", Zone: "z", Machine: "m", Image: "i", CPU: 1, MemoryMiB: 1, Architecture: "amd64", PriceMicros: 1, Currency: "USD", Spot: true, ObservedAt: time.Now()}}
	o := New(cfg, k, nil)
	n, err := o.HandleDesiredRunnerCount(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, f, err := o.loadFleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Pending) != 1 {
		t.Fatalf("expected exactly one pending allocation, got %d (n=%d, condition=%s)", len(f.Pending), n, f.Condition)
	}
	for _, a := range f.Pending {
		if a.NetworkProfile != "mesh" {
			t.Fatalf("expected NetworkProfile %q on new allocation, got %q", "mesh", a.NetworkProfile)
		}
	}
	// Checkpointing a Pending allocation (the same Save Tick's own first loop
	// performs, before ever invoking Controller.Step/real provider effects)
	// must not lose the identity HandleDesiredRunnerCount just set.
	for _, a := range f.Pending {
		if _, err := o.Store.Save(ctx, a, ""); err != nil {
			t.Fatal(err)
		}
	}
	allocs, err := o.Store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(allocs) != 1 || allocs[0].NetworkProfile != "mesh" {
		t.Fatalf("checkpointed allocation lost its NetworkProfile identity: %+v", allocs)
	}
}

func TestHandleDesiredRunnerCountLeavesNetworkProfileEmptyWhenUnconfigured(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := networkConfig("")
	cfg.Catalog.Offerings = []placement.Offering{{ID: "pool", Provider: "aws", Region: "r", Zone: "z", Machine: "m", Image: "i", CPU: 1, MemoryMiB: 1, Architecture: "amd64", PriceMicros: 1, Currency: "USD", Spot: true, ObservedAt: time.Now()}}
	s := &state.Kubernetes{Maps: k.CoreV1().ConfigMaps("test"), Owner: "test"}
	o := &Operator{Config: cfg, Client: k, Store: s}
	if _, err := o.HandleDesiredRunnerCount(ctx, 1); err != nil {
		t.Fatal(err)
	}
	_, f, err := o.loadFleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range f.Pending {
		if a.NetworkProfile != "" {
			t.Fatalf("allocation gained a NetworkProfile identity with none configured: %+v", a)
		}
	}
}

func TestConfigValidateRejectsInvalidNetworkProfileName(t *testing.T) {
	cfg := networkConfig("Not_A_Valid-Label!")
	if err := cfg.Validate(); err == nil {
		t.Fatal("invalid network profile name accepted")
	}
	cfg = networkConfig("mesh")
	if err := cfg.Validate(); err != nil {
		t.Fatal("valid network profile name rejected", err)
	}
	cfg = networkConfig("")
	if err := cfg.Validate(); err != nil {
		t.Fatal("empty (unconfigured) network profile name rejected", err)
	}
}

func TestConfigValidateRequiresOverlayCIDRsForWireGuardMode(t *testing.T) {
	cfg := networkConfig("mesh")
	cfg.NetworkOverlayCIDRs = nil
	if err := cfg.Validate(); err == nil {
		t.Fatal("wireguard mode NetworkProfile accepted with no overlay CIDRs")
	}
	cfg.NetworkOverlayCIDRs = []string{"not-a-cidr"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("invalid overlay CIDR accepted")
	}
	cfg.NetworkOverlayCIDRs = []string{"10.90.0.1/24"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("non-canonical overlay CIDR (host bits set) accepted")
	}
	cfg.NetworkOverlayCIDRs = []string{overlayTestCIDR}
	if err := cfg.Validate(); err != nil {
		t.Fatal("valid overlay CIDR rejected", err)
	}
}

func TestConfigValidateRejectsOverlayCIDRsWithoutNetworkProfile(t *testing.T) {
	cfg := networkConfig("")
	cfg.NetworkOverlayCIDRs = []string{overlayTestCIDR}
	if err := cfg.Validate(); err == nil {
		t.Fatal("overlay CIDRs accepted with no wireguard NetworkProfile")
	}
}

// runningAllocation is a minimal Running allocation with a checkpointed
// WireGuard identity, the shape wireguard.Snapshot requires to include a
// peer.
func runningAllocation(id, networkProfile string) lifecycle.Allocation {
	keyPair, err := wireguard.Generate()
	if err != nil {
		panic(err)
	}
	return lifecycle.Allocation{ID: id, Phase: lifecycle.Running, NetworkProfile: networkProfile, WireGuardPublicKey: keyPair.Public.Bytes(), WireGuardOverlayAddress: "10.90.0.1", Requirements: placement.Requirements{Providers: []string{"aws"}, Regions: []string{"r"}, Architecture: "amd64", Policy: "lowest-price"}}
}

// TestHandleDesiredRunnerCountAssignsOverlayAddress is the end-to-end proof
// that a wireguard-mode NetworkProfile's CIDR pool actually reaches a real
// lifecycle.Allocation: before this task nothing in this codebase could
// ever produce a non-empty lifecycle.Allocation.WireGuardOverlayAddress.
func TestHandleDesiredRunnerCountAssignsOverlayAddress(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := networkConfig("mesh")
	cfg.Catalog.Offerings = []placement.Offering{{ID: "pool", Provider: "aws", Region: "r", Zone: "z", Machine: "m", Image: "i", CPU: 1, MemoryMiB: 1, Architecture: "amd64", PriceMicros: 1, Currency: "USD", Spot: true, ObservedAt: time.Now()}}
	o := New(cfg, k, nil)
	if _, err := o.HandleDesiredRunnerCount(ctx, 1); err != nil {
		t.Fatal(err)
	}
	_, f, err := o.loadFleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Pending) != 1 {
		t.Fatalf("expected exactly one pending allocation, got %d (condition=%s)", len(f.Pending), f.Condition)
	}
	var addr string
	for _, a := range f.Pending {
		addr = a.WireGuardOverlayAddress
		if addr == "" {
			t.Fatal("expected a non-empty overlay address on a new wireguard-mode allocation")
		}
		if _, err := o.Store.Save(ctx, a, ""); err != nil {
			t.Fatal(err)
		}
	}
	// Checkpointing (the same Save Tick's own first loop performs) must not
	// lose the overlay address HandleDesiredRunnerCount just assigned - the
	// same round-trip proof TestHandleDesiredRunnerCountSetsNetworkProfileOnNewAllocations
	// already requires of NetworkProfile.
	allocs, err := o.Store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(allocs) != 1 || allocs[0].WireGuardOverlayAddress != addr {
		t.Fatalf("checkpointed allocation lost its overlay address: %+v", allocs)
	}
}

// TestHandleDesiredRunnerCountLeavesOverlayAddressEmptyWhenUnconfigured
// mirrors TestHandleDesiredRunnerCountLeavesNetworkProfileEmptyWhenUnconfigured:
// an allocation outside any wireguard-mode NetworkProfile must never gain an
// overlay address it has no use for.
func TestHandleDesiredRunnerCountLeavesOverlayAddressEmptyWhenUnconfigured(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := networkConfig("")
	cfg.Catalog.Offerings = []placement.Offering{{ID: "pool", Provider: "aws", Region: "r", Zone: "z", Machine: "m", Image: "i", CPU: 1, MemoryMiB: 1, Architecture: "amd64", PriceMicros: 1, Currency: "USD", Spot: true, ObservedAt: time.Now()}}
	s := &state.Kubernetes{Maps: k.CoreV1().ConfigMaps("test"), Owner: "test"}
	o := &Operator{Config: cfg, Client: k, Store: s}
	if _, err := o.HandleDesiredRunnerCount(ctx, 1); err != nil {
		t.Fatal(err)
	}
	_, f, err := o.loadFleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range f.Pending {
		if a.WireGuardOverlayAddress != "" {
			t.Fatalf("allocation gained an overlay address with no NetworkProfile configured: %+v", a)
		}
	}
}

// TestHandleDesiredRunnerCountAssignsOverlayAddressAvoidingCollision proves
// collision avoidance against an already-active peer sharing the same
// NetworkProfile: overlayTestCIDR's first usable address ("10.90.0.1") is
// exactly what runningAllocation already occupies, so a freshly admitted
// allocation must be assigned a different one.
func TestHandleDesiredRunnerCountAssignsOverlayAddressAvoidingCollision(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := networkConfig("mesh")
	cfg.Catalog.Offerings = []placement.Offering{{ID: "pool", Provider: "aws", Region: "r", Zone: "z", Machine: "m", Image: "i", CPU: 1, MemoryMiB: 1, Architecture: "amd64", PriceMicros: 1, Currency: "USD", Spot: true, ObservedAt: time.Now()}}
	o := New(cfg, k, nil)
	existing := runningAllocation("peer", "mesh")
	if existing.WireGuardOverlayAddress != "10.90.0.1" {
		t.Fatalf("test fixture assumption broken: expected runningAllocation to occupy 10.90.0.1, got %q", existing.WireGuardOverlayAddress)
	}
	if _, err := o.Store.Save(ctx, existing, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := o.HandleDesiredRunnerCount(ctx, 1); err != nil {
		t.Fatal(err)
	}
	_, f, err := o.loadFleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Pending) != 1 {
		t.Fatalf("expected exactly one pending allocation, got %d (condition=%s)", len(f.Pending), f.Condition)
	}
	for _, a := range f.Pending {
		if a.WireGuardOverlayAddress != "10.90.0.2" {
			t.Fatalf("expected the allocator to skip the already-taken 10.90.0.1 and assign 10.90.0.2, got %q", a.WireGuardOverlayAddress)
		}
	}
}

// TestHandleDesiredRunnerCountRefusesAdmissionWhenOverlayPoolExhausted
// proves exhaustion is a clear, real admission failure - never a silent
// skip, an infinite retry loop, or a panic - mirroring exactly how
// CatalogUnavailable/CatalogNotAdmissible already refuse an over-admitted
// batch above it in HandleDesiredRunnerCount.
func TestHandleDesiredRunnerCountRefusesAdmissionWhenOverlayPoolExhausted(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := networkConfig("mesh")
	// A /30 pool has exactly two usable host addresses; occupying both with
	// already-Running peers leaves nothing for a new admission.
	cfg.NetworkOverlayCIDRs = []string{"10.90.0.0/30"}
	cfg.Catalog.Offerings = []placement.Offering{{ID: "pool", Provider: "aws", Region: "r", Zone: "z", Machine: "m", Image: "i", CPU: 1, MemoryMiB: 1, Architecture: "amd64", PriceMicros: 1, Currency: "USD", Spot: true, ObservedAt: time.Now()}}
	o := New(cfg, k, nil)
	first := runningAllocation("peer-1", "mesh")
	first.WireGuardOverlayAddress = "10.90.0.1"
	second := runningAllocation("peer-2", "mesh")
	second.WireGuardOverlayAddress = "10.90.0.2"
	for _, a := range []lifecycle.Allocation{first, second} {
		if _, err := o.Store.Save(ctx, a, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.HandleDesiredRunnerCount(ctx, 1); err != nil {
		t.Fatal(err)
	}
	_, f, err := o.loadFleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f.Condition != "OverlayAddressPoolExhausted" {
		t.Fatalf("expected OverlayAddressPoolExhausted condition, got %q", f.Condition)
	}
	if len(f.Pending) != 0 {
		t.Fatalf("expected no pending allocation admitted while the overlay pool is exhausted, got %+v", f.Pending)
	}
	if f.Admission.Admitted != 0 {
		t.Fatalf("expected the rejected admission to be rolled back, got Admitted=%d", f.Admission.Admitted)
	}
}

// TestHandleDesiredRunnerCountPreservesExistingOverlayAddressOnSubsequentCalls
// is the idempotency/stability proof: an allocation that already has a
// checkpointed overlay address must keep it, unchanged, across later
// HandleDesiredRunnerCount calls that admit further allocations sharing the
// same NetworkProfile - re-running admission logic must never reassign an
// address already handed out.
func TestHandleDesiredRunnerCountPreservesExistingOverlayAddressOnSubsequentCalls(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := networkConfig("mesh")
	cfg.Catalog.Offerings = []placement.Offering{{ID: "pool", Provider: "aws", Region: "r", Zone: "z", Machine: "m", Image: "i", CPU: 1, MemoryMiB: 1, Architecture: "amd64", PriceMicros: 1, Currency: "USD", Spot: true, ObservedAt: time.Now()}}
	o := New(cfg, k, nil)
	if _, err := o.HandleDesiredRunnerCount(ctx, 1); err != nil {
		t.Fatal(err)
	}
	_, f, err := o.loadFleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var firstID, firstAddr string
	for id, a := range f.Pending {
		firstID, firstAddr = id, a.WireGuardOverlayAddress
		// Materialize it into the Store, the same checkpointing Tick's own
		// first loop performs, so the second HandleDesiredRunnerCount call
		// below sees it as an active allocation to avoid colliding with.
		if _, err := o.Store.Save(ctx, a, ""); err != nil {
			t.Fatal(err)
		}
	}
	if firstAddr == "" {
		t.Fatal("expected the first admitted allocation to receive an overlay address")
	}
	if _, err := o.HandleDesiredRunnerCount(ctx, 2); err != nil {
		t.Fatal(err)
	}
	reloaded, err := o.Store.Load(ctx, firstID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.WireGuardOverlayAddress != firstAddr {
		t.Fatalf("a subsequent admission cycle reassigned an already-checkpointed overlay address: was %q, now %q", firstAddr, reloaded.WireGuardOverlayAddress)
	}
	_, f, err = o.loadFleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for id, a := range f.Pending {
		if id == firstID {
			continue
		}
		if a.WireGuardOverlayAddress == "" || a.WireGuardOverlayAddress == firstAddr {
			t.Fatalf("expected the newly admitted allocation to get a distinct, non-empty overlay address, got %q (first got %q)", a.WireGuardOverlayAddress, firstAddr)
		}
	}
}

// TestNewWiresNetworkPeersFromStoreSnapshot is the RED/GREEN proof for the
// first dependency-injection hook this task closes:
// provider.Command.NetworkPeers must, after New, actually compute a live
// peer snapshot from the same Store the Operator reconciles against -
// before this task, nothing in this codebase ever constructed a non-nil
// NetworkPeers.
func TestNewWiresNetworkPeersFromStoreSnapshot(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := networkConfig("mesh")
	o := New(cfg, k, nil)
	command, ok := o.Controller.Providers["aws"].(*provider.Command)
	if !ok || command.NetworkPeers == nil {
		t.Fatal("provider.Command.NetworkPeers was not wired by New")
	}
	self := runningAllocation("self", "mesh")
	peer := runningAllocation("peer", "mesh")
	other := runningAllocation("other-mesh-peer", "different-mesh")
	for _, a := range []lifecycle.Allocation{self, peer, other} {
		if _, err := o.Store.Save(ctx, a, ""); err != nil {
			t.Fatal(err)
		}
	}
	peers, err := command.NetworkPeers(ctx, self)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].AllocationID != "peer" {
		t.Fatalf("expected exactly the one same-NetworkProfile Running peer (excluding self and a different NetworkProfile), got %+v", peers)
	}
}

// TestNewWithCredentialsPreservesNetworkPeers guards the same copy-forward
// bug class that would have silently dropped Bootstrap if NewWithCredentials
// ever forgot to carry it onto the credentialed replacement Command: it must
// carry NetworkPeers across exactly the same swap.
func TestNewWithCredentialsPreservesNetworkPeers(t *testing.T) {
	ctx := context.Background()
	k := fake.NewClientset()
	cfg := networkConfig("mesh")
	credentials := map[string]map[string]string{"aws": {"AWS_ACCESS_KEY_ID": "fixture-id", "AWS_SECRET_ACCESS_KEY": "fixture-secret"}}
	o, cleanup, err := NewWithCredentials(cfg, k, nil, credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	command, ok := o.Controller.Providers["aws"].(*provider.Command)
	if !ok || command.NetworkPeers == nil {
		t.Fatal("NewWithCredentials dropped NetworkPeers when swapping in the credentialed Command")
	}
	if _, err := command.NetworkPeers(ctx, runningAllocation("self", "mesh")); err != nil {
		t.Fatal(err)
	}
}
