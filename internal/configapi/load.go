package configapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	api "github.com/tsouza/runnerscout/api/v1alpha1"
	"github.com/tsouza/runnerscout/internal/operator"
	typed "k8s.io/client-go/kubernetes/typed/core/v1"
)

type Loaded struct {
	Snapshot    Snapshot
	Resolved    Resolved
	Credentials Credentials
	Revisions   []Revision
}

// Load accepts configuration and credentials together, or returns no material.
// It does not establish clients, update status or authorize cloud operations.
func Load(ctx context.Context, reader Reader, secrets typed.SecretInterface, namespace, name string) (Loaded, error) {
	snapshot, revisions, err := Read(ctx, reader, namespace, name)
	if err != nil {
		return Loaded{}, err
	}
	resolved, err := Compile(snapshot)
	if err != nil {
		return Loaded{}, err
	}
	credentials, err := ResolveSecrets(ctx, secrets, resolved)
	if err != nil {
		return Loaded{}, err
	}
	if err := Recheck(ctx, reader, namespace, revisions); err != nil {
		return Loaded{}, err
	}
	return Loaded{Snapshot: snapshot, Resolved: resolved, Credentials: credentials, Revisions: revisions}, nil
}

// Key identifies runtime-relevant changes without hashing Secret values. Status
// writes and unrelated metadata updates cannot trigger listener restart loops.
func (l Loaded) Key() string {
	type identity struct{ Resource, Name, UID string }
	identities := make([]identity, 0, len(l.Revisions))
	for _, revision := range l.Revisions {
		identities = append(identities, identity{revision.Resource, revision.Name, revision.UID})
	}
	var network *api.NetworkProfileSpec
	if l.Snapshot.Network != nil {
		network = &l.Snapshot.Network.Spec
	}
	data, _ := json.Marshal(struct {
		Config     operator.Config
		Auth       api.GitHubAuthentication
		References map[string]map[string]api.SecretKeyReference
		Suspend    bool
		Network    *api.NetworkProfileSpec
		Identities []identity
		Secrets    []SecretRevision
	}{l.Resolved.Config, l.Resolved.Auth, l.Resolved.Credentials, l.Resolved.Suspend, network, identities, l.Credentials.Revisions})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
