package configapi

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/placement"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func exampleReader(t *testing.T) *memoryReader {
	t.Helper()
	r := &memoryReader{objects: map[string]*unstructured.Unstructured{}}
	resources := map[string]string{"ProviderConfig": "providerconfigs", "RunnerClass": "runnerclasses", "RunnerScaleSet": "runnerscalesets", "CapacityCatalog": "capacitycatalogs", "NetworkProfile": "networkprofiles"}
	for _, name := range []string{"providers.yaml", "class.yaml", "catalog.yaml", "network.yaml"} {
		file, err := os.Open(filepath.Join("../../examples/multicloud", name))
		if err != nil {
			t.Fatal(err)
		}
		decoder := yaml.NewYAMLOrJSONDecoder(file, 4096)
		for {
			object := &unstructured.Unstructured{}
			err := decoder.Decode(object)
			if err != nil {
				if err != io.EOF {
					file.Close()
					t.Fatal(err)
				}
				break
			}
			resource, ok := resources[object.GetKind()]
			if !ok {
				file.Close()
				t.Fatal("unsupported example kind", object.GetKind())
			}
			object.SetUID(types.UID(object.GetName() + "-uid"))
			object.SetResourceVersion("1")
			object.SetGeneration(1)
			r.objects[resource+"/"+object.GetName()] = object
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestCompleteMulticloudExampleCompilesWithoutEnablingAdmissions(t *testing.T) {
	r := exampleReader(t)
	if len(r.objects) != 7 {
		t.Fatalf("complete example has %d resources, want 7", len(r.objects))
	}
	snapshot, revisions, err := Read(context.Background(), r, "runnerscout", "build")
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 7 || len(snapshot.Providers) != 3 || snapshot.Network == nil {
		t.Fatal("examples do not cover the full CRD graph")
	}
	resolved, err := Compile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Suspend || resolved.Config.Requirements.AllowOnDemand || snapshot.Class.Spec.Retry.Enabled || snapshot.Network.Spec.Mode != "separate" {
		t.Fatal("example silently enabled paid admission or unsupported behavior")
	}
	if len(resolved.Config.Catalog.Offerings) != 6 {
		t.Fatal("example must cover spot and on-demand pools for all three clouds")
	}
	if offering, err := placement.Choose(time.Now(), resolved.Config.Requirements, resolved.Config.Catalog, nil); err == nil || offering.ID != "" {
		t.Fatal("illustrative expired prices permitted placement")
	}
	for name, complete := range resolved.Config.Catalog.Complete {
		if complete {
			t.Fatal("illustrative catalog claims authoritative enumeration", name)
		}
	}
	if len(resolved.Credentials["aws"]) != 1 || len(resolved.Credentials["azure"]) != 3 || len(resolved.Credentials["gcp"]) != 1 {
		t.Fatal("examples lost external credential boundaries")
	}
}
