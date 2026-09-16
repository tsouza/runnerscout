package configapi

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	api "github.com/tsouza/runnerscout/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
)

// budgetReaderFixture returns a reader whose "build" RunnerScaleSet
// references a CapacityBudget - readerFixture's own scale set has no
// budgetRef, matching every other checkpoint test's default shape.
func budgetReaderFixture(t *testing.T) *memoryReader {
	t.Helper()
	r := readerFixture(t)
	s := fixture()
	s.ScaleSet.Spec.BudgetRef = &api.LocalReference{Name: "daily"}
	raw, err := json.Marshal(s.ScaleSet)
	if err != nil {
		t.Fatal(err)
	}
	var content map[string]any
	if err := json.Unmarshal(raw, &content); err != nil {
		t.Fatal(err)
	}
	u := r.objects["runnerscalesets/build"]
	u.Object = content
	u.SetAPIVersion(api.Group + "/" + api.Version)
	u.SetKind("RunnerScaleSet")
	u.SetUID(types.UID("build-uid"))
	u.SetResourceVersion("1")
	u.SetGeneration(1)
	budget := api.CapacityBudget{ObjectMeta: metav1.ObjectMeta{Name: "daily", Namespace: "test"}, Spec: api.CapacityBudgetSpec{DailyBudgetMicros: 1000}}
	braw, err := json.Marshal(budget)
	if err != nil {
		t.Fatal(err)
	}
	var bcontent map[string]any
	if err := json.Unmarshal(braw, &bcontent); err != nil {
		t.Fatal(err)
	}
	bu := &unstructured.Unstructured{Object: bcontent}
	bu.SetAPIVersion(api.Group + "/" + api.Version)
	bu.SetKind("CapacityBudget")
	bu.SetUID(types.UID("daily-uid"))
	bu.SetResourceVersion("1")
	bu.SetGeneration(1)
	r.objects["capacitybudgets/daily"] = bu
	return r
}

func checkpointFixture(t *testing.T) (Checkpoints, Snapshot, *fake.Clientset) {
	t.Helper()
	snapshot, _, err := Read(context.Background(), readerFixture(t), "test", "build")
	if err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientset()
	version := 0
	client.PrependReactor("*", "configmaps", func(action kt.Action) (bool, runtime.Object, error) {
		var object *corev1.ConfigMap
		switch action := action.(type) {
		case kt.CreateAction:
			object = action.GetObject().(*corev1.ConfigMap)
		case kt.UpdateAction:
			object = action.GetObject().(*corev1.ConfigMap)
		default:
			return false, nil, nil
		}
		version++
		object.UID = types.UID("checkpoint-object")
		object.ResourceVersion = strconv.Itoa(version)
		return false, nil, nil
	})
	return Checkpoints{Maps: client.CoreV1().ConfigMaps("test"), Namespace: "test", Name: "build"}, snapshot, client
}

func TestCheckpointPreservesRecoverableBindingWithoutMetadataOrSecrets(t *testing.T) {
	ctx := context.Background()
	store, snapshot, client := checkpointFixture(t)
	snapshot.ScaleSet.Annotations = map[string]string{"unrelated": "must-not-be-checkpointed"}
	if err := store.Save(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	cm, err := client.CoreV1().ConfigMaps("test").Get(ctx, "build-configuration", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cm.Data["snapshot"], "must-not-be-checkpointed") {
		t.Fatal("unrelated metadata retained in cleanup checkpoint")
	}
	recovered, err := store.Read(ctx)
	if err != nil || recovered.ScaleSet.UID != snapshot.ScaleSet.UID || len(recovered.Providers) != 3 {
		t.Fatal("cleanup binding was not recoverable", err)
	}
	if len(recovered.ScaleSet.Annotations) != 0 || recovered.ScaleSet.ResourceVersion != "" {
		t.Fatal("checkpoint retained transient metadata")
	}
	recovered.ScaleSet.Spec.MaxRunners = 3
	recovered.Catalog.Spec.Offerings = nil
	if err := store.Save(ctx, recovered); err != nil {
		t.Fatal("safe admission/catalog update rejected", err)
	}
	changed := recovered
	changed.Class.Spec.Resources.CPU++
	if err := store.Save(ctx, changed); !errors.Is(err, ErrBindingChange) {
		t.Fatal("ownership/class drift replaced the cleanup checkpoint", err)
	}
	after, err := store.Read(ctx)
	if err != nil || after.ScaleSet.Spec.MaxRunners != 3 || after.Class.Spec.Resources.CPU != recovered.Class.Spec.Resources.CPU {
		t.Fatal("rejected binding change corrupted accepted state", err)
	}
	changed = recovered
	changed.ScaleSet.UID = "another-scale-set"
	if err := store.Save(ctx, changed); !errors.Is(err, ErrCheckpointOwnership) {
		t.Fatal("replacement scale set adopted previous cleanup state", err)
	}
	changed = recovered
	now := metav1.Now()
	changed.ScaleSet.DeletionTimestamp = &now
	if err := store.Save(ctx, changed); err == nil {
		t.Fatal("deleting live configuration was silently accepted as active")
	}
}

// checkpointSnapshot strips ScaleSet/Class/Catalog/Providers/Network
// metadata and status before persisting, so unrelated churn on those
// objects (a label, a status write, a re-apply bumping resourceVersion)
// never forces a needless checkpoint write. Budget must get the same
// treatment.
func TestCheckpointStripsUnrelatedBudgetMetadata(t *testing.T) {
	ctx := context.Background()
	snapshot, _, err := Read(ctx, budgetReaderFixture(t), "test", "build")
	if err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientset()
	client.PrependReactor("*", "configmaps", func(action kt.Action) (bool, runtime.Object, error) {
		var object *corev1.ConfigMap
		switch action := action.(type) {
		case kt.CreateAction:
			object = action.GetObject().(*corev1.ConfigMap)
		case kt.UpdateAction:
			object = action.GetObject().(*corev1.ConfigMap)
		default:
			return false, nil, nil
		}
		object.UID = types.UID("checkpoint-object")
		object.ResourceVersion = "1"
		return false, nil, nil
	})
	store := Checkpoints{Maps: client.CoreV1().ConfigMaps("test"), Namespace: "test", Name: "build"}
	if err := store.Save(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	cm, err := client.CoreV1().ConfigMaps("test").Get(ctx, "build-configuration", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	before := cm.Data["snapshot"]

	// Unrelated Budget metadata churn - nothing Compile actually reads
	// (BudgetDailyMicros is unchanged) - must not change the checkpoint.
	snapshot.Budget.Annotations = map[string]string{"unrelated": "must-not-be-checkpointed"}
	snapshot.Budget.ResourceVersion = "999"
	snapshot.Budget.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Test", Message: "unrelated status churn", LastTransitionTime: metav1.Now()}}
	if err := store.Save(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	cm, err = client.CoreV1().ConfigMaps("test").Get(ctx, "build-configuration", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cm.Data["snapshot"], "must-not-be-checkpointed") {
		t.Fatal("unrelated Budget annotation retained in cleanup checkpoint")
	}
	if cm.Data["snapshot"] != before {
		t.Fatal("unrelated Budget metadata/status churn forced a checkpoint write", cm.Data["snapshot"], before)
	}
}

func TestCheckpointRejectsForeignMalformedAndOversizedState(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"foreign-owner", "foreign-uid", "unknown-data", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			store, snapshot, client := checkpointFixture(t)
			if err := store.Save(ctx, snapshot); err != nil {
				t.Fatal(err)
			}
			cm, err := client.CoreV1().ConfigMaps("test").Get(ctx, "build-configuration", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "foreign-owner":
				cm.Labels["runnerscout/owner"] = "another-controller"
			case "foreign-uid":
				cm.Annotations[checkpointUID] = "another-scale-set"
			case "unknown-data":
				cm.Data["snapshot"] = `{"credentialMaterial":"not-configuration"}`
			case "oversized":
				cm.Data["snapshot"] = strings.Repeat("x", checkpointLimit+1)
			}
			if _, err := client.CoreV1().ConfigMaps("test").Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Read(ctx); err == nil {
				t.Fatal("invalid cleanup checkpoint accepted")
			}
			if err := store.Save(ctx, snapshot); err == nil {
				t.Fatal("invalid checkpoint was overwritten instead of requiring recovery")
			}
		})
	}
}

func TestCheckpointConflictAndRemovalKeepOwnershipPreconditions(t *testing.T) {
	ctx := context.Background()
	store, snapshot, client := checkpointFixture(t)
	if err := store.Save(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	client.PrependReactor("update", "configmaps", func(kt.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "build-configuration", errors.New("fixture conflict"))
	})
	snapshot.ScaleSet.Spec.MaxRunners = 3
	if err := store.Save(ctx, snapshot); !apierrors.IsConflict(err) {
		t.Fatal("checkpoint update bypassed optimistic concurrency", err)
	}
	if err := store.Remove(ctx, "another-scale-set"); !errors.Is(err, ErrCheckpointOwnership) {
		t.Fatal("foreign checkpoint removal accepted", err)
	}
	checked := false
	client.PrependReactor("delete", "configmaps", func(action kt.Action) (bool, runtime.Object, error) {
		options := action.(kt.DeleteAction).GetDeleteOptions()
		if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != "checkpoint-object" || options.Preconditions.ResourceVersion == nil || *options.Preconditions.ResourceVersion == "" {
			t.Fatal("checkpoint removal lacks identity/version preconditions")
		}
		checked = true
		return false, nil, nil
	})
	if err := store.Remove(ctx, string(snapshot.ScaleSet.UID)); err != nil || !checked {
		t.Fatal("owned checkpoint removal failed", err)
	}
}
