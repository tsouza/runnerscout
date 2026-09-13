// Package state persists controller state using Kubernetes resourceVersion CAS.
package state

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"io"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typed "k8s.io/client-go/kubernetes/typed/core/v1"
	"strings"
)

type Kubernetes struct {
	Maps  typed.ConfigMapInterface
	Owner string
}

func (s *Kubernetes) Load(ctx context.Context, id string) (lifecycle.Allocation, error) {
	cm, err := s.Maps.Get(ctx, id, metav1.GetOptions{})
	if err != nil {
		return lifecycle.Allocation{}, err
	}
	if cm.Labels["runnerscout/owner"] != s.Owner {
		return lifecycle.Allocation{}, errors.New("state ownership mismatch")
	}
	a, err := decodeAllocation(cm)
	if err != nil {
		return a, err
	}
	if a.ID != id {
		return a, errors.New("allocation identity mismatch")
	}
	a.Revision = cm.ResourceVersion
	return a, nil
}
func (s *Kubernetes) Save(ctx context.Context, a lifecycle.Allocation, revision string) (lifecycle.Allocation, error) {
	if revision != "" {
		current, e := s.Maps.Get(ctx, a.ID, metav1.GetOptions{})
		if e != nil {
			return a, e
		}
		if current.Labels["runnerscout/owner"] != s.Owner {
			return a, errors.New("state ownership mismatch")
		}
		if current.ResourceVersion != revision {
			return a, lifecycle.ErrConflict
		}
		previous, err := decodeAllocation(current)
		if err != nil {
			return a, err
		}
		incoming, _, err := lifecycle.MergeResources(nil, a.Resources)
		if err != nil {
			return a, err
		}
		retained, _, err := lifecycle.MergeResources(previous.Resources, incoming)
		if err != nil {
			return a, err
		}
		if len(retained) != len(incoming) {
			return a, errors.New("cannot discard recorded cloud dependencies")
		}
		a.Resources = incoming
	}
	resources, _, err := lifecycle.MergeResources(nil, a.Resources)
	if err != nil {
		return a, err
	}
	a.Resources = resources
	a.Revision = ""
	b, err := json.Marshal(a)
	if err != nil {
		return a, err
	}
	key := "allocation"
	if len(a.Resources) > 0 {
		key = "allocation-v2"
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: a.ID, ResourceVersion: revision, Labels: map[string]string{"runnerscout/owner": s.Owner, "runnerscout/kind": "allocation"}}, Data: map[string]string{key: string(b)}}
	var out *corev1.ConfigMap
	if revision == "" {
		out, err = s.Maps.Create(ctx, cm, metav1.CreateOptions{})
	} else {
		out, err = s.Maps.Update(ctx, cm, metav1.UpdateOptions{})
	}
	if apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) {
		return a, lifecycle.ErrConflict
	}
	if err != nil {
		return a, err
	}
	a.Revision = out.ResourceVersion
	return a, nil
}
func (s *Kubernetes) List(ctx context.Context) ([]lifecycle.Allocation, error) {
	cms, err := s.Maps.List(ctx, metav1.ListOptions{LabelSelector: "runnerscout/owner=" + s.Owner + ",runnerscout/kind=allocation"})
	if err != nil {
		return nil, err
	}
	out := make([]lifecycle.Allocation, 0, len(cms.Items))
	for _, cm := range cms.Items {
		a, err := decodeAllocation(&cm)
		if err != nil {
			return nil, err
		}
		if a.ID != cm.Name {
			return nil, errors.New("allocation identity mismatch")
		}
		a.Revision = cm.ResourceVersion
		out = append(out, a)
	}
	return out, nil
}

// Versioned dependency records deliberately remove the legacy data key: an older
// reader fails decoding instead of silently dropping cleanup obligations.
func decodeAllocation(cm *corev1.ConfigMap) (lifecycle.Allocation, error) {
	var allocation lifecycle.Allocation
	legacy, old := cm.Data["allocation"]
	current, versioned := cm.Data["allocation-v2"]
	if old == versioned {
		return allocation, errors.New("missing or ambiguous allocation format")
	}
	data := legacy
	if versioned {
		data = current
	}
	decoder := json.NewDecoder(strings.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&allocation); err != nil {
		return allocation, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return allocation, errors.New("invalid trailing allocation data")
	}
	if allocation.ID != cm.Name {
		return allocation, errors.New("allocation identity mismatch")
	}
	if versioned != (len(allocation.Resources) > 0) {
		return allocation, errors.New("allocation dependency format mismatch")
	}
	if _, _, err := lifecycle.MergeResources(nil, allocation.Resources); err != nil {
		return allocation, err
	}
	return allocation, nil
}
