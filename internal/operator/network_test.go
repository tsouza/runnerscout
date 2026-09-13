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

func networkConfig(networkProfile string) Config {
	return Config{Name: "test", Namespace: "test", GitHubURL: "https://github.com/tsouza/runnerscout", ScaleSetID: 1, MaxRunners: 10, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600,
		Requirements:   placement.Requirements{CPU: 1, MemoryMiB: 1, Architecture: "amd64", MaxPriceMicros: 100, Providers: []string{"aws"}, Regions: []string{"r"}, Policy: "lowest-price"},
		Catalog:        placement.Catalog{Complete: map[string]bool{"aws": true}},
		Providers:      map[string]provider.Config{"aws": {Kind: "aws", Owner: "test", AccountID: "000000000000", Subnet: "private", SecurityGroup: "private"}},
		NetworkProfile: networkProfile,
	}
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
