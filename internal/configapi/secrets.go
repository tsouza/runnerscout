package configapi

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"

	api "github.com/tsouza/runnerscout/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typed "k8s.io/client-go/kubernetes/typed/core/v1"
)

// Credentials contains only referenced Secret keys, never entire Secret objects.
// It is process-local material and must not be persisted in configuration or status.
type Credentials struct {
	GitHub    []byte                       `json:"-"`
	Providers map[string]map[string]string `json:"-"`
	Revisions []SecretRevision             `json:"-"`
}

type SecretRevision struct {
	Name            string
	UID             string
	ResourceVersion string
	// ContentHash is a sha256 over this Secret's own .data (sorted by key),
	// never the raw bytes. Load.Key() hashes this, not ResourceVersion - a
	// live Kubernetes Secret's resourceVersion bumps on any write to the
	// object, including a metadata-only or status-only change with .data
	// byte-for-byte unchanged (a real observed cause: something touching
	// annotations/labels on a watched credential Secret with no rotation
	// involved at all). Key()'s own doc comment says "status writes and
	// unrelated metadata updates cannot trigger listener restart loops" -
	// hashing ResourceVersion directly violated exactly that, restarting
	// the listener session (and losing all in-flight admission progress)
	// on every such touch. Confirmed in production via the listener's own
	// diagnostic logging (PR #183's Logger fix): "Getting next message
	// lastMessageID=0" repeating every 60-90s with a fresh "Handling
	// initial session statistics" each time - a brand new session every
	// reconcile, never surviving long enough to admit anything, exactly
	// the restart loop this comment already disclaimed but this struct's
	// own ResourceVersion field was still causing. Computed
	// once resolveSecrets has already confirmed (via ResourceVersion, kept
	// on this struct for exactly that unrelated purpose - see this
	// function's own concurrent-recheck loop) that no mutation happened
	// mid-read, so it reflects a single consistent snapshot of .data.
	ContentHash string
}

// secretDataHash hashes a Secret's .data deterministically - map iteration
// order is not - so identical content always produces the identical hash
// regardless of how Kubernetes happens to have ordered the map this time.
func secretDataHash(data map[string][]byte) string {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	hash := sha256.New()
	for _, key := range keys {
		hash.Write([]byte(key))
		hash.Write([]byte{0})
		hash.Write(data[key])
		hash.Write([]byte{0})
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func (Credentials) String() string   { return "credentials (redacted)" }
func (Credentials) GoString() string { return "credentials (redacted)" }

// ResolveSecrets reads a compiled configuration's named, same-namespace Secrets.
// Rechecking object identities and versions rejects a mixed concurrent rotation.
// File-valued environment variables reference operator-mounted paths; their
// Secret values are not interpreted as file contents or executable commands.
func ResolveSecrets(ctx context.Context, client typed.SecretInterface, resolved Resolved) (Credentials, error) {
	return resolveSecrets(ctx, client, resolved, true)
}

// ResolveCleanupSecrets reads cloud credentials without requiring GitHub access.
// It is only for a worker already in monotonic drain mode.
func ResolveCleanupSecrets(ctx context.Context, client typed.SecretInterface, resolved Resolved) (Credentials, error) {
	return resolveSecrets(ctx, client, resolved, false)
}

func resolveSecrets(ctx context.Context, client typed.SecretInterface, resolved Resolved, github bool) (Credentials, error) {
	result := Credentials{Providers: map[string]map[string]string{}}
	objects := map[string]*corev1.Secret{}
	read := func(ref api.SecretKeyReference) ([]byte, error) {
		if err := secret(ref); err != nil {
			return nil, err
		}
		object, ok := objects[ref.Name]
		if !ok {
			var err error
			object, err = client.Get(ctx, ref.Name, metav1.GetOptions{})
			if err != nil {
				return nil, errors.New("referenced Secret unavailable")
			}
			if object.Namespace != resolved.Config.Namespace || object.Name != ref.Name || object.UID == "" || object.ResourceVersion == "" || object.DeletionTimestamp != nil {
				return nil, errors.New("referenced Secret identity invalid")
			}
			objects[ref.Name] = object.DeepCopy()
		}
		value, ok := object.Data[ref.Key]
		if !ok || len(value) == 0 {
			return nil, errors.New("referenced Secret key missing or empty")
		}
		return append([]byte(nil), value...), nil
	}
	if github {
		var err error
		result.GitHub, err = read(resolved.Auth.SecretRef)
		if err != nil {
			return Credentials{}, err
		}
		if resolved.Auth.Mode == "pat" {
			result.GitHub = []byte(strings.TrimSpace(string(result.GitHub)))
			if len(result.GitHub) == 0 || strings.ContainsAny(string(result.GitHub), "\x00\r\n") {
				return Credentials{}, errors.New("invalid GitHub token")
			}
		}
	}
	for name, refs := range resolved.Credentials {
		config, ok := resolved.Config.Providers[name]
		if !ok {
			return Credentials{}, errors.New("credential provider is not configured")
		}
		result.Providers[name] = map[string]string{}
		for variable, ref := range refs {
			if !credentialVariable(config.Kind, variable) {
				return Credentials{}, errors.New("unsupported provider credential variable")
			}
			value, err := read(ref)
			if err != nil {
				return Credentials{}, err
			}
			if strings.ContainsRune(string(value), 0) {
				return Credentials{}, errors.New("invalid provider credential value")
			}
			result.Providers[name][variable] = string(value)
		}
	}
	for name, old := range objects {
		current, err := client.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return Credentials{}, errors.New("referenced Secret recheck failed")
		}
		if current.Namespace != old.Namespace || current.Name != old.Name || current.UID != old.UID || current.ResourceVersion != old.ResourceVersion || current.DeletionTimestamp != nil {
			return Credentials{}, ErrChanged
		}
		result.Revisions = append(result.Revisions, SecretRevision{Name: name, UID: string(current.UID), ResourceVersion: current.ResourceVersion, ContentHash: secretDataHash(current.Data)})
	}
	slices.SortFunc(result.Revisions, func(a, b SecretRevision) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}
