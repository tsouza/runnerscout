package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/crypto/curve25519"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/wireguard"
)

func TestBootstrapWithWireGuardEmptyPayloadIsByteForByteUnchanged(t *testing.T) {
	for _, jit := range []string{"secret-jit", "", "another-token"} {
		script, err := BootstrapWithWireGuard(jit, "")
		if err != nil {
			t.Fatal(err)
		}
		if script != Bootstrap(jit) {
			t.Fatalf("BootstrapWithWireGuard with no payload diverged from Bootstrap for jit=%q", jit)
		}
	}
}

func TestBootstrapWithWireGuardEmbedsAndCleansUpSecondFile(t *testing.T) {
	payload := `{"privateKey":"fixture-private","overlayAddress":"10.60.0.5","peers":[]}`
	script, err := BootstrapWithWireGuard("secret-jit", payload)
	if err != nil {
		t.Fatal(err)
	}
	if script == Bootstrap("secret-jit") {
		t.Fatal("wireguard payload did not change the script")
	}
	b64 := base64.StdEncoding.EncodeToString([]byte(payload))
	if !strings.Contains(script, b64) {
		t.Fatal("script does not embed the base64-encoded wireguard payload")
	}
	if !strings.Contains(script, "/run/runnerscout/wireguard.json") {
		t.Fatal("script does not write the wireguard payload to the documented path")
	}
	if !strings.Contains(script, "trap 'rm -f /run/runnerscout/jit /run/runnerscout/wireguard.json; shutdown -h now' EXIT") {
		t.Fatal("script's cleanup trap does not remove the wireguard payload file")
	}
	// The JIT token portion must still be present and untouched.
	if !strings.Contains(script, base64.StdEncoding.EncodeToString([]byte("secret-jit"))) {
		t.Fatal("JIT token embedding was disturbed by wireguard embedding")
	}
}

// TestCreateWithResourcesZeroEffectWithoutNetworkProfile is this task's
// required regression guard: for every allocation this codebase can produce
// today (NetworkProfile always ""), the generated cloud-init script must be
// byte-for-byte identical to what CreateWithResources produced before this
// change, and no WireGuard key material is generated or checkpointed.
func TestCreateWithResourcesZeroEffectWithoutNetworkProfile(t *testing.T) {
	p, f, a := nativeAWSFixture(t)
	if a.NetworkProfile != "" {
		t.Fatal("test fixture unexpectedly set NetworkProfile")
	}
	receipt, err := p.CreateWithResources(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.WireGuardPublicKey) != 0 {
		t.Fatalf("wireguard key material generated with no NetworkProfile intent: %x", receipt.WireGuardPublicKey)
	}
	var script string
	for _, request := range f.Requests() {
		if request.Get("Action") == "RunInstances" {
			decoded, err := base64.StdEncoding.DecodeString(request.Get("UserData"))
			if err != nil {
				t.Fatal(err)
			}
			script = string(decoded)
		}
	}
	if script == "" {
		t.Fatal("no RunInstances request observed")
	}
	if script != Bootstrap("secret-jit") {
		t.Fatal("cloud-init script changed for an allocation with no wireguard intent")
	}
	if strings.Contains(script, "wireguard") {
		t.Fatal("cloud-init script mentions wireguard despite no NetworkProfile intent")
	}
}

// TestCreateWithResourcesWithoutNetworkPeersHookRefusesWireGuardIntent proves
// the integration point fails safely - never silently proceeding with an
// empty peer list - when an allocation intends wireguard mode but the caller
// never wired NetworkPeers (true of every caller in this codebase today).
func TestCreateWithResourcesWithoutNetworkPeersHookRefusesWireGuardIntent(t *testing.T) {
	p, f, a := nativeAWSFixture(t)
	a.NetworkProfile = "profile-a"
	receipt, err := p.CreateWithResources(context.Background(), a)
	if err != lifecycle.ErrNoEffect {
		t.Fatalf("expected ErrNoEffect, got %v", err)
	}
	if receipt.ResourceID != "" || len(receipt.WireGuardPublicKey) != 0 {
		t.Fatal("wireguard intent without a NetworkPeers hook produced a receipt", receipt)
	}
	if len(f.Requests()) != 0 {
		t.Fatal("wireguard intent without a NetworkPeers hook still reached the cloud provider")
	}
}

// TestCreateWithResourcesEmbedsWireGuardIdentityWhenIntended exercises the
// full wireguard-mode path this task builds: given NetworkProfile set and a
// NetworkPeers hook, CreateWithResources must generate a keypair, embed the
// private key and peer snapshot into cloud-init, and checkpoint the matching
// public key onto the returned Creation.
func TestCreateWithResourcesEmbedsWireGuardIdentityWhenIntended(t *testing.T) {
	p, f, a := nativeAWSFixture(t)
	a.NetworkProfile = "profile-a"
	a.WireGuardOverlayAddress = "10.60.0.7"
	wantPeers := []wireguard.Peer{{AllocationID: "rs-peer", PublicKey: "cGVlci1wdWJsaWMta2V5", OverlayAddress: "10.60.0.1"}}
	var sawAllocationID string
	p.NetworkPeers = func(_ context.Context, allocation lifecycle.Allocation) ([]wireguard.Peer, error) {
		sawAllocationID = allocation.ID
		return wantPeers, nil
	}
	receipt, err := p.CreateWithResources(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if sawAllocationID != a.ID {
		t.Fatalf("NetworkPeers hook received wrong allocation: %q", sawAllocationID)
	}
	if len(receipt.WireGuardPublicKey) != wireguard.KeySize {
		t.Fatalf("checkpointed public key has wrong size: %d", len(receipt.WireGuardPublicKey))
	}
	var script string
	for _, request := range f.Requests() {
		if request.Get("Action") == "RunInstances" {
			decoded, err := base64.StdEncoding.DecodeString(request.Get("UserData"))
			if err != nil {
				t.Fatal(err)
			}
			script = string(decoded)
		}
	}
	if script == "" {
		t.Fatal("no RunInstances request observed")
	}
	if !strings.Contains(script, "/run/runnerscout/wireguard.json") {
		t.Fatal("cloud-init script does not embed the wireguard payload")
	}
	// Extract and decode the embedded wireguard.json payload from the script
	// to prove it round-trips into the same public key checkpointed onto the
	// Creation, and carries the exact peer snapshot the hook returned.
	const marker = "printf '%s' '"
	start := strings.LastIndex(script, marker)
	if start < 0 {
		t.Fatal("could not locate wireguard payload install line")
	}
	start += len(marker)
	end := strings.Index(script[start:], "'")
	if end < 0 {
		t.Fatal("could not locate end of wireguard payload base64 blob")
	}
	rawJSON, err := base64.StdEncoding.DecodeString(script[start : start+end])
	if err != nil {
		t.Fatal(err)
	}
	var decodedPayload wireguard.CloudInitPayload
	if err := json.Unmarshal(rawJSON, &decodedPayload); err != nil {
		t.Fatal(err)
	}
	if decodedPayload.OverlayAddress != a.WireGuardOverlayAddress {
		t.Fatalf("overlay address mismatch: got %q want %q", decodedPayload.OverlayAddress, a.WireGuardOverlayAddress)
	}
	if len(decodedPayload.Peers) != 1 || decodedPayload.Peers[0] != wantPeers[0] {
		t.Fatalf("peer snapshot mismatch: got %+v want %+v", decodedPayload.Peers, wantPeers)
	}
	privateKeyBytes, err := base64.StdEncoding.DecodeString(decodedPayload.PrivateKey)
	if err != nil || len(privateKeyBytes) != wireguard.KeySize {
		t.Fatalf("embedded private key malformed: %v (len %d)", err, len(privateKeyBytes))
	}
	derivedPublic, err := curve25519.X25519(privateKeyBytes, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	if string(derivedPublic) != string(receipt.WireGuardPublicKey) {
		t.Fatal("checkpointed public key does not match the private key embedded in cloud-init")
	}
}
