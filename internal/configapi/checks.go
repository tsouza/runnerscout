package configapi

import (
	"context"
	"errors"

	"github.com/tsouza/runnerscout/internal/operator"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CheckConfiguration validates a live configuration/Secret snapshot without
// starting sessions, accessing providers or changing Kubernetes resources.
func (r *Runtime) CheckConfiguration(ctx context.Context) error {
	_, err := Load(ctx, KubernetesReader{Client: r.Dynamic}, r.Client.CoreV1().Secrets(r.Namespace), r.Namespace, r.Name)
	return err
}

// CheckUninstall verifies that this controller no longer has an owned root or
// unresolved durable allocations. It is read-only and requires no credentials.
// Delete the RunnerScaleSet and wait for its finalizer while the controller is
// running; deleting the controller first would strand cloud cleanup.
func (r *Runtime) CheckUninstall(ctx context.Context) error {
	if err := r.rootAbsent(ctx); err != nil {
		return err
	}
	snapshot, err := r.checkpoints().Read(ctx)
	if apierrors.IsNotFound(err) {
		owned, listErr := r.Client.CoreV1().ConfigMaps(r.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "runnerscout/owner=" + r.Name})
		if listErr != nil {
			return listErr
		}
		if len(owned.Items) != 0 {
			return errors.New("configuration checkpoint missing while owned state remains; restore cleanup ownership before uninstall")
		}
	} else if err != nil {
		return err
	} else {
		resolved, err := Compile(snapshot)
		if err != nil {
			return err
		}
		// New constructs only local descriptors. Drained reads durable state;
		// it never creates provider clients or calls GitHub/cloud APIs.
		done, err := operator.New(resolved.Config, r.Client, nil).Drained(ctx)
		if err != nil {
			return err
		}
		if !done {
			return errors.New("cloud cleanup is unresolved; keep the controller running")
		}
	}
	return r.rootAbsent(ctx)
}

func (r *Runtime) rootAbsent(ctx context.Context) error {
	_, err := r.roots().Get(ctx, r.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("delete the RunnerScaleSet and wait for its cleanup finalizer before uninstalling the controller")
}
