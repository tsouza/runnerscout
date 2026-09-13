package configapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	api "github.com/tsouza/runnerscout/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var ErrChanged = errors.New("configuration dependencies changed during snapshot collection")

type Reader interface {
	Get(context.Context, string, string, string) (*unstructured.Unstructured, error)
}

type KubernetesReader struct{ Client dynamic.Interface }

func (r KubernetesReader) Get(ctx context.Context, resource, namespace, name string) (*unstructured.Unstructured, error) {
	return r.Client.Resource(schema.GroupVersionResource{Group: api.Group, Version: api.Version, Resource: resource}).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

type Revision struct {
	Resource        string `json:"resource"`
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion"`
	Generation      int64  `json:"generation"`
}

// Read obtains only the named dependencies in the scale set's own namespace.
// A second collection verifies an overlapping stable interval for all objects.
// Secret contents are intentionally not part of configuration snapshots.
func Read(ctx context.Context, reader Reader, namespace, name string) (Snapshot, []Revision, error) {
	var s Snapshot
	var revisions []Revision
	get := func(resource, kind, name string, target any) error {
		u, err := reader.Get(ctx, resource, namespace, name)
		if err != nil {
			return errors.New("cannot read configuration dependency")
		}
		if u.GetNamespace() != namespace || u.GetName() != name || u.GetAPIVersion() != api.Group+"/"+api.Version || u.GetKind() != kind || u.GetUID() == "" || u.GetResourceVersion() == "" {
			return errors.New("configuration dependency identity is invalid")
		}
		data, err := json.Marshal(u.Object)
		if err != nil || len(data) > 4<<20 {
			return errors.New("configuration dependency exceeds decoding limits")
		}
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if err := d.Decode(target); err != nil {
			return errors.New("configuration dependency has unsupported fields or types")
		}
		var extra any
		if err := d.Decode(&extra); err != io.EOF {
			return errors.New("invalid configuration document")
		}
		revisions = append(revisions, Revision{Resource: resource, Name: name, UID: string(u.GetUID()), ResourceVersion: u.GetResourceVersion(), Generation: u.GetGeneration()})
		return nil
	}
	if err := get("runnerscalesets", "RunnerScaleSet", name, &s.ScaleSet); err != nil {
		return s, nil, err
	}
	if err := get("runnerclasses", "RunnerClass", s.ScaleSet.Spec.RunnerClassRef.Name, &s.Class); err != nil {
		return s, nil, err
	}
	if err := get("capacitycatalogs", "CapacityCatalog", s.Class.Spec.CatalogRef.Name, &s.Catalog); err != nil {
		return s, nil, err
	}
	if len(s.Class.Spec.Providers) < 1 || len(s.Class.Spec.Providers) > 16 {
		return s, nil, errors.New("runner class must reference 1..16 providers")
	}
	s.Providers = make(map[string]api.ProviderConfig)
	for _, ref := range s.Class.Spec.Providers {
		if _, exists := s.Providers[ref.Name]; exists {
			return s, nil, errors.New("duplicate provider reference")
		}
		var p api.ProviderConfig
		if err := get("providerconfigs", "ProviderConfig", ref.Name, &p); err != nil {
			return s, nil, err
		}
		s.Providers[ref.Name] = p
	}
	if s.Class.Spec.NetworkRef != nil {
		s.Network = new(api.NetworkProfile)
		if err := get("networkprofiles", "NetworkProfile", s.Class.Spec.NetworkRef.Name, s.Network); err != nil {
			return s, nil, err
		}
	}
	if err := Recheck(ctx, reader, namespace, revisions); err != nil {
		return s, nil, err
	}
	if _, err := Compile(s); err != nil {
		return s, nil, err
	}
	return s, revisions, nil
}

// Recheck also lets the runtime verify configuration after collecting Secrets.
// This establishes an overlapping stable interval across both dependency sets.
func Recheck(ctx context.Context, reader Reader, namespace string, revisions []Revision) error {
	for _, rev := range revisions {
		u, err := reader.Get(ctx, rev.Resource, namespace, rev.Name)
		if err != nil {
			return errors.New("cannot verify configuration snapshot")
		}
		if string(u.GetUID()) != rev.UID || u.GetResourceVersion() != rev.ResourceVersion || u.GetNamespace() != namespace || u.GetName() != rev.Name {
			return ErrChanged
		}
	}
	return nil
}
