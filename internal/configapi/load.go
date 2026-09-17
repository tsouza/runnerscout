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
	// secretIdentity deliberately omits SecretRevision.ResourceVersion, the
	// same way identity above already omits it for CRD/ConfigMap
	// revisions - see SecretRevision.ContentHash's own doc comment for why
	// hashing a live Secret's raw resourceVersion here reopens exactly the
	// restart loop this function's own comment disclaims.
	type secretIdentity struct{ Name, UID, ContentHash string }
	secrets := make([]secretIdentity, 0, len(l.Credentials.Revisions))
	for _, revision := range l.Credentials.Revisions {
		secrets = append(secrets, secretIdentity{revision.Name, revision.UID, revision.ContentHash})
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
		Secrets    []secretIdentity
	}{l.Resolved.Config, l.Resolved.Auth, l.Resolved.Credentials, l.Resolved.Suspend, network, identities, secrets})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
