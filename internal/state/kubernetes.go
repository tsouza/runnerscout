// Package state persists controller state using Kubernetes resourceVersion CAS.
package state

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typed "k8s.io/client-go/kubernetes/typed/core/v1"
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
	var a lifecycle.Allocation
	if err = json.Unmarshal([]byte(cm.Data["allocation"]), &a); err != nil {
		return a, err
	}
	if a.ID != id {
		return a, errors.New("allocation identity mismatch")
	}
	a.Revision = cm.ResourceVersion
	return a, nil
}
func (s *Kubernetes) Save(ctx context.Context, a lifecycle.Allocation, revision string) (lifecycle.Allocation, error) {
	a.Revision = ""
	b, err := json.Marshal(a)
	if err != nil {
		return a, err
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: a.ID, ResourceVersion: revision, Labels: map[string]string{"runnerscout/owner": s.Owner, "runnerscout/kind": "allocation"}}, Data: map[string]string{"allocation": string(b)}}
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
		var a lifecycle.Allocation
		if err = json.Unmarshal([]byte(cm.Data["allocation"]), &a); err != nil {
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
