package lifecycle_test

import (
	"context"
	"testing"

	l "github.com/tsouza/runnerscout/internal/lifecycle"
)

// wireGuardCloud is a ResourceCreator that returns a WireGuard public key
// alongside its cloud resource identity, exercising the same checkpoint path
// Creation.ResourceID/Resources already use (see receiptCloud in
// creation_test.go). No real provider does this today - internal/configapi's
// network() keeps NetworkProfile unreachable - but the checkpoint plumbing on
// Controller.Step must already be correct for when it is.
type wireGuardCloud struct {
	*cloud
	publicKey []byte
}

func (p *wireGuardCloud) CreateWithResources(context.Context, l.Allocation) (l.Creation, error) {
	p.created++
	return l.Creation{ResourceID: "vm-1", WireGuardPublicKey: p.publicKey}, nil
}

func TestCreationChecksPointsWireGuardPublicKeyLikeResourceID(t *testing.T) {
	controller, durable, base, _ := setup()
	provider := &wireGuardCloud{cloud: base, publicKey: []byte("fixture-public-key")}
	controller.Providers = map[string]l.Provider{"a": provider}
	if err := controller.Step(context.Background(), durable.a.ID); err != nil {
		t.Fatal(err)
	}
	if string(durable.a.WireGuardPublicKey) != "fixture-public-key" {
		t.Fatalf("wireguard public key was not checkpointed: %q", durable.a.WireGuardPublicKey)
	}
}

// TestCreationWithoutWireGuardIntentNeverCheckpointsAKey is half of this
// task's "zero effect until wired" regression guard at the lifecycle layer:
// a provider that never sets Creation.WireGuardPublicKey (every provider in
// this codebase today) must never cause Allocation.WireGuardPublicKey to
// become non-empty.
func TestCreationWithoutWireGuardIntentNeverCheckpointsAKey(t *testing.T) {
	controller, durable, base, _ := setup()
	provider := &receiptCloud{cloud: base}
	controller.Providers = map[string]l.Provider{"a": provider}
	if err := controller.Step(context.Background(), durable.a.ID); err != nil {
		t.Fatal(err)
	}
	if len(durable.a.WireGuardPublicKey) != 0 {
		t.Fatalf("wireguard public key checkpointed with no provider-supplied key: %q", durable.a.WireGuardPublicKey)
	}
}

// wireGuardPollTokenCloud is a ResourceCreator that returns a WireGuard poll
// token hash alongside its cloud resource identity - the same checkpoint
// path wireGuardCloud above exercises for the public key, for the new field
// this task adds.
type wireGuardPollTokenCloud struct {
	*cloud
	pollTokenHash []byte
}

func (p *wireGuardPollTokenCloud) CreateWithResources(context.Context, l.Allocation) (l.Creation, error) {
	p.created++
	return l.Creation{ResourceID: "vm-1", WireGuardPollTokenHash: p.pollTokenHash}, nil
}

func TestCreationChecksPointsWireGuardPollTokenHashLikePublicKey(t *testing.T) {
	controller, durable, base, _ := setup()
	provider := &wireGuardPollTokenCloud{cloud: base, pollTokenHash: []byte("fixture-poll-token-hash")}
	controller.Providers = map[string]l.Provider{"a": provider}
	if err := controller.Step(context.Background(), durable.a.ID); err != nil {
		t.Fatal(err)
	}
	if string(durable.a.WireGuardPollTokenHash) != "fixture-poll-token-hash" {
		t.Fatalf("wireguard poll token hash was not checkpointed: %q", durable.a.WireGuardPollTokenHash)
	}
}

// TestCreationWithoutWireGuardIntentNeverCheckpointsAPollTokenHash mirrors
// TestCreationWithoutWireGuardIntentNeverCheckpointsAKey for the new field:
// a provider that never sets Creation.WireGuardPollTokenHash (every provider
// in this codebase today) must never cause
// Allocation.WireGuardPollTokenHash to become non-empty.
func TestCreationWithoutWireGuardIntentNeverCheckpointsAPollTokenHash(t *testing.T) {
	controller, durable, base, _ := setup()
	provider := &receiptCloud{cloud: base}
	controller.Providers = map[string]l.Provider{"a": provider}
	if err := controller.Step(context.Background(), durable.a.ID); err != nil {
		t.Fatal(err)
	}
	if len(durable.a.WireGuardPollTokenHash) != 0 {
		t.Fatalf("wireguard poll token hash checkpointed with no provider-supplied hash: %q", durable.a.WireGuardPollTokenHash)
	}
}
