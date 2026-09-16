package configapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	api "github.com/tsouza/runnerscout/api/v1alpha1"
	"github.com/tsouza/runnerscout/internal/operator"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typed "k8s.io/client-go/kubernetes/typed/core/v1"
)

const checkpointKind = "configuration"
const checkpointUID = "runnerscout.io/scale-set-uid"
const checkpointLimit = 900 << 10

var ErrCheckpointOwnership = errors.New("configuration checkpoint ownership mismatch")
var ErrBindingChange = errors.New("restore the accepted provider and class binding for cleanup")

// Checkpoints retain only configuration and Secret references. They must be
// written under the scale-set Lease and observed before installing its finalizer
// and allowing cloud effects. This keeps interrupted deletion recoverable.
type Checkpoints struct {
	Maps      typed.ConfigMapInterface
	Namespace string
	Name      string
}

func (c Checkpoints) decode(cm *corev1.ConfigMap) (Snapshot, error) {
	var snapshot Snapshot
	if cm.Namespace != c.Namespace || cm.Name != c.Name+"-configuration" || cm.Labels["runnerscout/owner"] != c.Name || cm.Labels["runnerscout/kind"] != checkpointKind {
		return snapshot, ErrCheckpointOwnership
	}
	data := []byte(cm.Data["snapshot"])
	if len(data) == 0 || len(data) > checkpointLimit {
		return snapshot, errors.New("invalid configuration checkpoint size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, errors.New("invalid configuration checkpoint")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Snapshot{}, errors.New("invalid configuration checkpoint trailer")
	}
	root := snapshot.ScaleSet
	if root.Namespace != c.Namespace || root.Name != c.Name || root.UID == "" || string(root.UID) != cm.Annotations[checkpointUID] {
		return Snapshot{}, ErrCheckpointOwnership
	}
	if _, err := Compile(snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func (c Checkpoints) Read(ctx context.Context) (Snapshot, error) {
	cm, err := c.Maps.Get(ctx, c.Name+"-configuration", metav1.GetOptions{})
	if err != nil {
		return Snapshot{}, err
	}
	return c.decode(cm)
}

func checkpointSnapshot(s Snapshot) (Snapshot, error) {
	data, err := json.Marshal(s)
	if err != nil || len(data) > 4<<20 {
		return Snapshot{}, errors.New("configuration exceeds checkpoint limits")
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, err
	}
	metadata := func(meta metav1.ObjectMeta) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: meta.Name, Namespace: meta.Namespace, UID: meta.UID, Generation: meta.Generation}
	}
	snapshot.ScaleSet.ObjectMeta, snapshot.ScaleSet.Status = metadata(snapshot.ScaleSet.ObjectMeta), api.ConfigurationStatus{}
	snapshot.Class.ObjectMeta, snapshot.Class.Status = metadata(snapshot.Class.ObjectMeta), api.ConfigurationStatus{}
	snapshot.Catalog.ObjectMeta, snapshot.Catalog.Status = metadata(snapshot.Catalog.ObjectMeta), api.ConfigurationStatus{}
	for name, provider := range snapshot.Providers {
		provider.ObjectMeta, provider.Status = metadata(provider.ObjectMeta), api.ConfigurationStatus{}
		snapshot.Providers[name] = provider
	}
	if snapshot.Network != nil {
		snapshot.Network.ObjectMeta, snapshot.Network.Status = metadata(snapshot.Network.ObjectMeta), api.ConfigurationStatus{}
	}
	if snapshot.Budget != nil {
		snapshot.Budget.ObjectMeta, snapshot.Budget.Status = metadata(snapshot.Budget.ObjectMeta), api.ConfigurationStatus{}
	}
	return snapshot, nil
}

func (c Checkpoints) Save(ctx context.Context, source Snapshot) error {
	if _, err := Compile(source); err != nil {
		return err
	}
	snapshot, err := checkpointSnapshot(source)
	if err != nil {
		return err
	}
	if snapshot.ScaleSet.UID == "" || snapshot.ScaleSet.Namespace != c.Namespace || snapshot.ScaleSet.Name != c.Name {
		return ErrCheckpointOwnership
	}
	resolved, err := Compile(snapshot)
	if err != nil {
		return err
	}
	data, err := json.Marshal(snapshot)
	if err != nil || len(data) > checkpointLimit {
		return errors.New("configuration exceeds checkpoint limits")
	}
	cm, err := c.Maps.Get(ctx, c.Name+"-configuration", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = c.Maps.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: c.Name + "-configuration", Namespace: c.Namespace,
			Labels: map[string]string{"runnerscout/owner": c.Name, "runnerscout/kind": checkpointKind}, Annotations: map[string]string{checkpointUID: string(snapshot.ScaleSet.UID)}},
			Data: map[string]string{"snapshot": string(data)}}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	old, err := c.decode(cm)
	if err != nil {
		return err
	}
	if old.ScaleSet.UID != snapshot.ScaleSet.UID {
		return ErrCheckpointOwnership
	}
	previous, err := Compile(old)
	if err != nil {
		return err
	}
	if !operator.SameBinding(previous.Config, resolved.Config) {
		return ErrBindingChange
	}
	if cm.Data["snapshot"] == string(data) {
		return nil
	}
	cm.Data["snapshot"] = string(data)
	_, err = c.Maps.Update(ctx, cm, metav1.UpdateOptions{})
	return err
}

// Remove requires already-confirmed resource cleanup. UID and resourceVersion
// preconditions prevent a concurrent replacement from being deleted.
func (c Checkpoints) Remove(ctx context.Context, scaleSetUID string) error {
	cm, err := c.Maps.Get(ctx, c.Name+"-configuration", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	snapshot, err := c.decode(cm)
	if err != nil {
		return err
	}
	if string(snapshot.ScaleSet.UID) != scaleSetUID || cm.UID == "" || cm.ResourceVersion == "" {
		return ErrCheckpointOwnership
	}
	return c.Maps.Delete(ctx, cm.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &cm.UID, ResourceVersion: &cm.ResourceVersion}})
}
