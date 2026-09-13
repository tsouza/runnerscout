package wireguard

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/curve25519"

	"github.com/tsouza/runnerscout/internal/lifecycle"
)

// TestGenerateProducesDistinctAgreeingKeys proves Generate returns real,
// usable Curve25519 keys rather than merely well-shaped bytes: two
// independently generated keypairs must agree on the same ECDH shared secret
// computed from either direction (X25519(privA, pubB) == X25519(privB,
// pubA)), which is only true if both are valid points/scalars on the curve.
// Recomputing the public key from the private key with the same formula
// Generate uses internally would be tautological; the cross-agreement check
// is not.
func TestGenerateProducesDistinctAgreeingKeys(t *testing.T) {
	a, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if a.Private == b.Private || a.Public == b.Public {
		t.Fatal("two calls to Generate produced identical keys")
	}
	secretFromA, err := curve25519.X25519(a.Private[:], b.Public[:])
	if err != nil {
		t.Fatal(err)
	}
	secretFromB, err := curve25519.X25519(b.Private[:], a.Public[:])
	if err != nil {
		t.Fatal(err)
	}
	if string(secretFromA) != string(secretFromB) {
		t.Fatal("generated keypairs did not agree on a shared secret")
	}
	var zero [KeySize]byte
	if a.Private == PrivateKey(zero) || a.Public == PublicKey(zero) {
		t.Fatal("generated key was all-zero")
	}
}

// TestPrivateKeyNeverExposedByFormattingOrJSON is the regression guard the
// task calls for: the raw private key bytes must never appear in %v, %+v,
// %#v output or survive an encoding/json.Marshal of anything containing one.
func TestPrivateKeyNeverExposedByFormattingOrJSON(t *testing.T) {
	kp, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	raw := string(kp.Private[:])
	b64 := kp.Private.Base64()
	forms := []string{
		fmt.Sprintf("%v", kp.Private),
		fmt.Sprintf("%+v", kp.Private),
		fmt.Sprintf("%#v", kp.Private),
		fmt.Sprintf("%v", kp),
		fmt.Sprintf("%+v", kp),
		fmt.Sprintf("%#v", kp),
	}
	for _, form := range forms {
		if strings.Contains(form, b64) || strings.Contains(form, raw) {
			t.Fatalf("private key material leaked through formatting: %q", form)
		}
	}
	type wrapper struct{ Key PrivateKey }
	if _, err := json.Marshal(wrapper{Key: kp.Private}); err == nil {
		t.Fatal("marshaling a struct containing a PrivateKey did not fail")
	}
	if _, err := json.Marshal(kp.Private); err == nil {
		t.Fatal("marshaling a bare PrivateKey did not fail")
	}
}

func running(id, profile, pub, addr string) lifecycle.Allocation {
	var key []byte
	if pub != "" {
		key = []byte(pub)
	}
	return lifecycle.Allocation{ID: id, Phase: lifecycle.Running, NetworkProfile: profile, WireGuardPublicKey: key, WireGuardOverlayAddress: addr}
}

func TestSnapshotOrdersFiltersAndExcludesSelf(t *testing.T) {
	allocations := []lifecycle.Allocation{
		running("rs-c", "profile-a", "pub-c", "10.60.0.3"),
		running("rs-a", "profile-a", "pub-a", "10.60.0.1"),
		running("rs-b", "profile-a", "pub-b", "10.60.0.2"),
		// self: must be excluded even though it otherwise qualifies.
		running("rs-self", "profile-a", "pub-self", "10.60.0.9"),
		// different profile: must be excluded.
		{ID: "rs-other-profile", Phase: lifecycle.Running, NetworkProfile: "profile-b", WireGuardPublicKey: []byte("pub-other"), WireGuardOverlayAddress: "10.60.1.1"},
		// not Running yet: must be excluded.
		{ID: "rs-creating", Phase: lifecycle.Creating, NetworkProfile: "profile-a", WireGuardPublicKey: []byte("pub-creating"), WireGuardOverlayAddress: "10.60.0.4"},
		// Running, same profile, but has not yet checkpointed its identity: excluded.
		{ID: "rs-incomplete", Phase: lifecycle.Running, NetworkProfile: "profile-a"},
	}
	peers := Snapshot("rs-self", "profile-a", allocations)
	if len(peers) != 3 {
		t.Fatalf("expected 3 peers, got %d: %+v", len(peers), peers)
	}
	wantOrder := []string{"rs-a", "rs-b", "rs-c"}
	for i, id := range wantOrder {
		if peers[i].AllocationID != id {
			t.Fatalf("peer order at %d: got %s want %s (%+v)", i, peers[i].AllocationID, id, peers)
		}
	}
	for _, p := range peers {
		if p.PublicKey == "" || p.OverlayAddress == "" {
			t.Fatalf("peer missing checkpointed identity: %+v", p)
		}
	}
}

// TestSnapshotCarriesCheckpointedEndpoint proves the outer-endpoint gap
// resolution (lifecycle.Allocation.WireGuardEndpoint) flows through into the
// peer snapshot's Endpoint field exactly like PublicKey/OverlayAddress
// already do.
func TestSnapshotCarriesCheckpointedEndpoint(t *testing.T) {
	a := running("rs-a", "profile-a", "pub-a", "10.60.0.1")
	a.WireGuardEndpoint = "10.0.5.9:51820"
	peers := Snapshot("rs-self", "profile-a", []lifecycle.Allocation{a})
	if len(peers) != 1 || peers[0].Endpoint != "10.0.5.9:51820" {
		t.Fatalf("checkpointed endpoint not carried into peer snapshot: %+v", peers)
	}
}

// TestSnapshotToleratesMissingEndpoint proves a peer that has checkpointed
// its public key/overlay address but not yet its endpoint (see
// Creation.WireGuardEndpoint's doc comment: not every provider sets it yet)
// is still included, with an empty Endpoint - never excluded, and never
// synthesized.
func TestSnapshotToleratesMissingEndpoint(t *testing.T) {
	peers := Snapshot("rs-self", "profile-a", []lifecycle.Allocation{running("rs-a", "profile-a", "pub-a", "10.60.0.1")})
	if len(peers) != 1 || peers[0].Endpoint != "" {
		t.Fatalf("expected one peer with an empty endpoint, got: %+v", peers)
	}
}

func TestSnapshotEmptyNetworkProfileYieldsNoPeers(t *testing.T) {
	allocations := []lifecycle.Allocation{running("rs-a", "", "pub-a", "10.60.0.1")}
	if peers := Snapshot("rs-b", "", allocations); len(peers) != 0 {
		t.Fatalf("empty NetworkProfile produced peers: %+v", peers)
	}
}

// TestGeneratePollTokenProducesDistinctHexTokensWithMatchingHash proves
// GeneratePollToken's two return values are actually linked (the hash really
// is of the token it returns, not some independent value) and that repeated
// calls never collide - the two properties the poll endpoint's auth check
// depends on.
func TestGeneratePollTokenProducesDistinctHexTokensWithMatchingHash(t *testing.T) {
	tokenA, hashA, err := GeneratePollToken()
	if err != nil {
		t.Fatal(err)
	}
	tokenB, hashB, err := GeneratePollToken()
	if err != nil {
		t.Fatal(err)
	}
	if tokenA == tokenB || hashA == hashB {
		t.Fatal("two calls to GeneratePollToken produced identical output")
	}
	raw, err := hex.DecodeString(tokenA)
	if err != nil {
		t.Fatalf("token is not valid hex: %v", err)
	}
	if len(raw) != PollTokenSize {
		t.Fatalf("decoded token has wrong length: got %d want %d", len(raw), PollTokenSize)
	}
	recomputed, err := HashPollToken(tokenA)
	if err != nil {
		t.Fatal(err)
	}
	if recomputed != hashA {
		t.Fatal("HashPollToken(tokenA) does not match the hash GeneratePollToken returned for tokenA")
	}
	if recomputed == hashB {
		t.Fatal("hash of tokenA matched tokenB's hash")
	}
}

// TestHashPollTokenRejectsMalformedInput proves the endpoint's auth path can
// treat "not valid hex" the same as "wrong token" rather than panicking or
// silently hashing garbage bytes.
func TestHashPollTokenRejectsMalformedInput(t *testing.T) {
	if _, err := HashPollToken("not-hex!!"); err == nil {
		t.Fatal("expected an error decoding a non-hex token")
	}
}

func TestSnapshotDeterministic(t *testing.T) {
	allocations := []lifecycle.Allocation{
		running("rs-z", "profile-a", "pub-z", "10.60.0.26"),
		running("rs-a", "profile-a", "pub-a", "10.60.0.1"),
	}
	first := Snapshot("rs-self", "profile-a", allocations)
	second := Snapshot("rs-self", "profile-a", allocations)
	if len(first) != len(second) {
		t.Fatal("nondeterministic peer count")
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatal("nondeterministic peer order or content")
		}
	}
}
