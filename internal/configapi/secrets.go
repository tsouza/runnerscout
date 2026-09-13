package configapi

import (
	"context"
	"errors"
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
		result.Revisions = append(result.Revisions, SecretRevision{Name: name, UID: string(current.UID), ResourceVersion: current.ResourceVersion})
	}
	slices.SortFunc(result.Revisions, func(a, b SecretRevision) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}
