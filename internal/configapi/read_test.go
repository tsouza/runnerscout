package configapi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	api "github.com/tsouza/runnerscout/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

type memoryReader struct {
	objects map[string]*unstructured.Unstructured
	calls   []string
	change  func(int, *unstructured.Unstructured)
}

func (r *memoryReader) Get(_ context.Context, resource, namespace, name string) (*unstructured.Unstructured, error) {
	r.calls = append(r.calls, namespace+"/"+resource+"/"+name)
	u, ok := r.objects[resource+"/"+name]
	if !ok {
		return nil, errors.New("missing")
	}
	u = u.DeepCopy()
	if r.change != nil {
		r.change(len(r.calls), u)
	}
	return u, nil
}
func readerFixture(t *testing.T) *memoryReader {
	t.Helper()
	s := fixture()
	r := &memoryReader{objects: make(map[string]*unstructured.Unstructured)}
	add := func(resource, kind, name string, object any) {
		raw, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		var content map[string]any
		if err = json.Unmarshal(raw, &content); err != nil {
			t.Fatal(err)
		}
		u := &unstructured.Unstructured{Object: content}
		u.SetAPIVersion(api.Group + "/" + api.Version)
		u.SetKind(kind)
		u.SetUID(types.UID(name + "-uid"))
		u.SetResourceVersion("1")
		u.SetGeneration(1)
		r.objects[resource+"/"+name] = u
	}
	add("runnerscalesets", "RunnerScaleSet", "build", s.ScaleSet)
	add("runnerclasses", "RunnerClass", "linux", s.Class)
	add("capacitycatalogs", "CapacityCatalog", "prices", s.Catalog)
	for name, p := range s.Providers {
		add("providerconfigs", "ProviderConfig", name, p)
	}
	return r
}
func TestReadUsesOnlyLocalNamedDependenciesAndRechecksVersions(t *testing.T) {
	r := readerFixture(t)
	s, revisions, err := Read(context.Background(), r, "test", "build")
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 6 || len(r.calls) != 12 || len(s.Providers) != 3 {
		t.Fatal("incomplete dependency collection")
	}
	for _, call := range r.calls {
		if len(call) < 5 || call[:5] != "test/" {
			t.Fatal("cross-namespace read")
		}
	}
}
func TestReadResolvesReferencedBudget(t *testing.T) {
	r := readerFixture(t)
	s := fixture()
	s.ScaleSet.Spec.BudgetRef = &api.LocalReference{Name: "daily"}
	raw, err := json.Marshal(s.ScaleSet)
	if err != nil {
		t.Fatal(err)
	}
	var content map[string]any
	if err = json.Unmarshal(raw, &content); err != nil {
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
	if err = json.Unmarshal(braw, &bcontent); err != nil {
		t.Fatal(err)
	}
	bu := &unstructured.Unstructured{Object: bcontent}
	bu.SetAPIVersion(api.Group + "/" + api.Version)
	bu.SetKind("CapacityBudget")
	bu.SetUID(types.UID("daily-uid"))
	bu.SetResourceVersion("1")
	bu.SetGeneration(1)
	r.objects["capacitybudgets/daily"] = bu

	got, revisions, err := Read(context.Background(), r, "test", "build")
	if err != nil {
		t.Fatal(err)
	}
	if got.Budget == nil || got.Budget.Spec.DailyBudgetMicros != 1000 {
		t.Fatal("budget not resolved", got.Budget)
	}
	if len(revisions) != 7 {
		t.Fatal("budget revision not tracked for Recheck", revisions)
	}
}

func TestReadRejectsConcurrentMutationAndObjectRecreation(t *testing.T) {
	for _, recreate := range []bool{false, true} {
		r := readerFixture(t)
		r.change = func(call int, u *unstructured.Unstructured) {
			if call == 7 {
				if recreate {
					u.SetUID("new-uid")
				} else {
					u.SetResourceVersion("2")
				}
			}
		}
		if _, _, err := Read(context.Background(), r, "test", "build"); !errors.Is(err, ErrChanged) {
			t.Fatalf("inconsistent snapshot accepted: %v", err)
		}
	}
}
func TestReadRejectsUnknownConfigurationAndNamespaceSpoofing(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		r := readerFixture(t)
		u := r.objects["runnerclasses/linux"]
		if unknown {
			u.Object["spec"].(map[string]any)["unimplementedField"] = true
		} else {
			u.SetNamespace("other")
		}
		if _, _, err := Read(context.Background(), r, "test", "build"); err == nil {
			t.Fatal("invalid object accepted")
		}
	}
}
