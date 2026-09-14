package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	"golang.org/x/oauth2"
	compute "google.golang.org/api/compute/v1"
	"google.golang.org/api/option"
)

func gcpAllocation() lifecycle.Allocation {
	a := allocation()
	a.Offering.Region = "us-central1"
	a.Offering.Zone = "us-central1-a"
	a.Offering.Machine = "n2-standard-2"
	a.Offering.Image = "projects/images/global/images/runner-v1"
	return a
}
func gcpFixture(t *testing.T, handler http.HandlerFunc) *Command {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gcp-fixture" {
			t.Error("SDK omitted scoped authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, server.Client())
	service, err := compute.NewService(ctx, option.WithEndpoint(server.URL+"/compute/v1/"), option.WithHTTPClient(oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "gcp-fixture"}))))
	if err != nil {
		t.Fatal(err)
	}
	return &Command{Config: credentialConfig("gcp"), GCP: &GCPSDK{Service: service}, Bootstrap: func(context.Context, string) (string, error) { return "private-jit-fixture", nil }}
}
func gcpMissing(w http.ResponseWriter) {
	w.WriteHeader(404)
	writeJSON(w, map[string]any{"error": map[string]any{"code": 404, "message": "not found"}})
}
func gcpLink(kind, name string) string {
	return "https://www.googleapis.com/compute/v1/projects/test-project/zones/us-central1-a/" + kind + "/" + name
}
func gcpOperation(p *Command, action, kind, name, status string) map[string]any {
	operationType := "delete"
	if action == "create" {
		operationType = "insert"
	}
	return map[string]any{"kind": "compute#operation", "name": "operation-" + action, "clientOperationId": p.gcpRequestID(gcpAllocation(), action), "operationType": operationType, "targetLink": gcpLink(kind, name), "status": status}
}
func gcpOwned(kind string) map[string]any {
	k := "compute#instance"
	if kind == "disks" {
		k = "compute#disk"
	}
	result := map[string]any{"kind": k, "id": "42", "name": "rs-test", "selfLink": gcpLink(kind, "rs-test"), "labels": map[string]string{"runnerscout-owner": "test", "runnerscout-operation": "rs-test"}}
	if kind == "instances" {
		result["disks"] = []any{map[string]any{"source": gcpLink("disks", "rs-test"), "boot": true, "autoDelete": true}}
	}
	return result
}
func TestGCPSDKCreatePrivateSpotVMAndTaggedBootDisk(t *testing.T) {
	var p *Command
	creates := 0
	p = gcpFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			gcpMissing(w)
			return
		}
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/instances") {
			t.Error("unexpected mutation", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		creates++
		if r.URL.Query().Get("requestId") != p.gcpRequestID(gcpAllocation(), "create") {
			t.Error("missing durable request identity")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["name"] != "rs-test" || body["machineType"] != "projects/test-project/zones/us-central1-a/machineTypes/n2-standard-2" {
			t.Error("wrong placement", body["machineType"])
		}
		accounts, ok := body["serviceAccounts"].([]any)
		if !ok || len(accounts) != 0 {
			t.Error("VM can inherit a service account", body["serviceAccounts"])
		}
		nic := body["networkInterfaces"].([]any)[0].(map[string]any)
		access, ok := nic["accessConfigs"].([]any)
		if !ok || len(access) != 0 || nic["subnetwork"] != "projects/test-project/regions/us-central1/subnetworks/private-subnet" {
			t.Error("VM networking is not explicitly private", nic)
		}
		disk := body["disks"].([]any)[0].(map[string]any)
		init := disk["initializeParams"].(map[string]any)
		if disk["autoDelete"] != true || disk["boot"] != true || init["diskName"] != "rs-test" || init["sourceImage"] != gcpAllocation().Offering.Image || init["labels"].(map[string]any)["runnerscout-operation"] != "rs-test" {
			t.Error("boot disk ownership/cleanup missing")
		}
		schedule := body["scheduling"].(map[string]any)
		if schedule["provisioningModel"] != "SPOT" || schedule["instanceTerminationAction"] != "DELETE" || schedule["automaticRestart"] != false {
			t.Error("spot policy changed")
		}
		metadata := body["metadata"].(map[string]any)["items"].([]any)[0].(map[string]any)
		if metadata["key"] != "startup-script" || metadata["value"] != Bootstrap("private-jit-fixture") {
			t.Error("wrong bootstrap delivery")
		}
		writeJSON(w, gcpOperation(p, "create", "instances", "rs-test", "DONE"))
	})
	id, err := p.Create(context.Background(), gcpAllocation())
	if err != nil || id != "rs-test" || creates != 1 {
		t.Fatal(id, err, creates)
	}
}
func TestGCPSDKLostResponseAndResidualDiskCleanup(t *testing.T) {
	vm, disk := false, false
	deletes := []string{}
	var p *Command
	p = gcpFixture(t, func(w http.ResponseWriter, r *http.Request) {
		kind := "instances"
		if strings.Contains(r.URL.Path, "/disks") {
			kind = "disks"
		}
		if strings.HasSuffix(r.URL.Path, "/operations") {
			if strings.Contains(r.URL.Query().Get("filter"), "preempted") {
				writeJSON(w, map[string]any{"kind": "compute#operationList", "items": []any{}})
				return
			}
			if !strings.Contains(r.URL.Query().Get("filter"), p.gcpRequestID(gcpAllocation(), "create")) {
				t.Error("wrong operation filter")
			}
			if r.URL.Query().Get("pageToken") == "tail" {
				writeJSON(w, map[string]any{"kind": "compute#operationList"})
				return
			}
			writeJSON(w, map[string]any{"kind": "compute#operationList", "items": []any{gcpOperation(p, "create", "instances", "rs-test", "DONE")}, "nextPageToken": "tail"})
			return
		}
		if r.Method == "POST" {
			vm, disk = true, true
			w.WriteHeader(503)
			writeJSON(w, map[string]any{"error": map[string]any{"code": 503, "message": "lost response diagnostic detail"}})
			return
		}
		if r.Method == "DELETE" {
			action := "delete-vm"
			if kind == "instances" {
				vm = false
			} else {
				disk = false
				action = "delete-disk"
			}
			deletes = append(deletes, kind)
			if r.URL.Query().Get("requestId") != p.gcpRequestID(gcpAllocation(), action) {
				t.Error("delete identity missing")
			}
			writeJSON(w, gcpOperation(p, action, kind, "rs-test", "DONE"))
			return
		}
		if (kind == "instances" && !vm) || (kind == "disks" && !disk) {
			gcpMissing(w)
			return
		}
		writeJSON(w, gcpOwned(kind))
	})
	a := gcpAllocation()
	if _, err := p.Create(context.Background(), a); err == nil || !strings.Contains(err.Error(), "lost response diagnostic detail") {
		t.Fatal("ambiguous response accepted, or the real GCP diagnostic was discarded", err)
	}
	restarted := &Command{Config: p.Config, GCP: p.GCP}
	ob, err := restarted.Observe(context.Background(), a)
	if err != nil || !ob.Known || !ob.Exists {
		t.Fatal("lost create was not recovered", ob, err)
	}
	a.ResourceID = ob.ResourceID
	if err := restarted.Delete(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	ob, err = restarted.Observe(context.Background(), a)
	if err != nil || !ob.Exists || !disk || vm {
		t.Fatal("residual disk mistaken for completed cleanup", ob, err)
	}
	if err := restarted.Delete(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	ob, err = restarted.Observe(context.Background(), a)
	if err != nil || !ob.Known || ob.Exists || strings.Join(deletes, ",") != "instances,disks" {
		t.Fatal("cleanup not confirmed in dependency order", ob, err, deletes)
	}
}
func TestGCPOwnershipBlocksDeletion(t *testing.T) {
	mutations := 0
	var p *Command
	p = gcpFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mutations++
			w.WriteHeader(400)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/operations") {
			writeJSON(w, map[string]any{"kind": "compute#operationList", "items": []any{gcpOperation(p, "create", "instances", "rs-test", "DONE")}})
			return
		}
		vm := gcpOwned("instances")
		vm["labels"] = map[string]string{"runnerscout-owner": "foreign"}
		writeJSON(w, vm)
	})
	if err := p.Delete(context.Background(), gcpAllocation()); err == nil || mutations != 0 {
		t.Fatal("foreign VM could be deleted", err, mutations)
	}
}
func TestGCPSDKUnknownOperationsAndInventoryRetainOwnership(t *testing.T) {
	for _, mode := range []string{"durable-identity", "active", "missing", "malformed", "foreign-operation", "forbidden", "invalid-vm", "foreign-attachment", "foreign-disk", "attached-disk"} {
		t.Run(mode, func(t *testing.T) {
			mutations := 0
			var p *Command
			p = gcpFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					mutations++
					w.WriteHeader(400)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/operations") {
					if mode == "malformed" {
						writeJSON(w, map[string]any{})
						return
					}
					if mode == "missing" {
						writeJSON(w, map[string]any{"kind": "compute#operationList"})
						return
					}
					op := gcpOperation(p, "create", "instances", "rs-test", "DONE")
					if mode == "active" {
						op["status"] = "RUNNING"
					}
					if mode == "foreign-operation" {
						op["targetLink"] = gcpLink("instances", "foreign")
					}
					writeJSON(w, map[string]any{"kind": "compute#operationList", "items": []any{op}})
					return
				}
				if mode == "forbidden" {
					w.WriteHeader(403)
					writeJSON(w, map[string]any{"error": map[string]any{"code": 403, "message": "forbidden diagnostic detail"}})
					return
				}
				if strings.Contains(r.URL.Path, "/instances/") {
					vm := gcpOwned("instances")
					if mode == "invalid-vm" {
						delete(vm, "id")
					}
					if mode == "foreign-attachment" {
						vm["disks"].([]any)[0].(map[string]any)["source"] = gcpLink("disks", "foreign")
					}
					writeJSON(w, vm)
					return
				}
				disk := gcpOwned("disks")
				if mode == "foreign-disk" {
					disk["labels"] = map[string]string{"runnerscout-owner": "foreign"}
				}
				if mode == "attached-disk" {
					disk["users"] = []string{gcpLink("instances", "another")}
				}
				writeJSON(w, disk)
			})
			a := gcpAllocation()
			if mode == "durable-identity" {
				a.ResourceID = "another-vm"
			}
			ob, err := p.Observe(context.Background(), a)
			if err == nil || ob.Known {
				t.Fatal("unknown inventory became authoritative", ob, err)
			}
			if mode == "forbidden" && !strings.Contains(err.Error(), "forbidden diagnostic detail") {
				t.Fatal("real GCP diagnostic was discarded instead of surfaced", err)
			}
			if err := p.Delete(context.Background(), a); err == nil || mutations != 0 {
				t.Fatal("unsafe cleanup", err, mutations)
			}
		})
	}
}
func TestGCPSDKCreateRefusesOccupiedDiskIdentity(t *testing.T) {
	mutations := 0
	p := gcpFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mutations++
			w.WriteHeader(400)
			return
		}
		if strings.Contains(r.URL.Path, "/instances/") {
			gcpMissing(w)
			return
		}
		disk := gcpOwned("disks")
		disk["labels"] = map[string]string{}
		writeJSON(w, disk)
	})
	if _, err := p.Create(context.Background(), gcpAllocation()); err == nil || mutations != 0 {
		t.Fatal("pre-existing disk adopted by name", err, mutations)
	}
}
func TestGCPSDKDefinitiveCapacityRejectionHasNoReceipt(t *testing.T) {
	var p *Command
	p = gcpFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			gcpMissing(w)
			return
		}
		op := gcpOperation(p, "create", "instances", "rs-test", "DONE")
		op["error"] = map[string]any{"errors": []any{map[string]any{"code": "ZONE_RESOURCE_POOL_EXHAUSTED", "message": "no capacity"}}}
		writeJSON(w, op)
	})
	id, err := p.Create(context.Background(), gcpAllocation())
	if id != "" || !errors.Is(err, lifecycle.ErrCapacity) {
		t.Fatal("definitive capacity rejection misclassified", id, err)
	}
}
func TestGCPSDKAmbiguousOperationFailureStaysUnknown(t *testing.T) {
	var p *Command
	p = gcpFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			gcpMissing(w)
			return
		}
		op := gcpOperation(p, "create", "instances", "rs-test", "DONE")
		op["error"] = map[string]any{"errors": []any{map[string]any{"code": "RESOURCE_OPERATION_RATE_EXCEEDED", "message": "throttled"}}}
		writeJSON(w, op)
	})
	id, err := p.Create(context.Background(), gcpAllocation())
	if id != "" || err == nil || errors.Is(err, lifecycle.ErrCapacity) {
		t.Fatal("ambiguous operation failure misclassified as capacity", id, err)
	}
}
func gcpPreemptionFixture(t *testing.T, includeVM bool) *Command {
	t.Helper()
	return gcpFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/operations") {
			if strings.Contains(r.URL.Query().Get("filter"), "preempted") {
				op := map[string]any{"kind": "compute#operation", "name": "preempt-op", "operationType": "compute.instances.preempted", "targetLink": gcpLink("instances", "rs-test"), "status": "DONE"}
				writeJSON(w, map[string]any{"kind": "compute#operationList", "items": []any{op}})
				return
			}
			writeJSON(w, map[string]any{"kind": "compute#operationList", "items": []any{}})
			return
		}
		if includeVM && strings.Contains(r.URL.Path, "/instances/") {
			writeJSON(w, gcpOwned("instances"))
			return
		}
		gcpMissing(w)
	})
}
func TestGCPObservationConfirmsSpotPreemption(t *testing.T) {
	p := gcpPreemptionFixture(t, false)
	a := gcpAllocation()
	a.Offering.Spot = true
	a.ResourceID = a.ID
	observed, err := p.Observe(context.Background(), a)
	if err != nil || !observed.Known || observed.Exists || !observed.Interrupted {
		t.Fatal("confirmed GCP preemption not observed", observed, err)
	}
}
func TestGCPObservationRequiresSpotOfferingForPreemption(t *testing.T) {
	p := gcpPreemptionFixture(t, false)
	a := gcpAllocation()
	a.Offering.Spot = false
	a.ResourceID = a.ID
	observed, err := p.Observe(context.Background(), a)
	if err != nil || !observed.Known || observed.Exists || observed.Interrupted {
		t.Fatal("on-demand offering misclassified as spot preemption", observed, err)
	}
}
func TestGCPObservationPreemptionWithSurvivingVMStaysUnknown(t *testing.T) {
	p := gcpPreemptionFixture(t, true)
	a := gcpAllocation()
	a.Offering.Spot = true
	a.ResourceID = a.ID
	observed, err := p.Observe(context.Background(), a)
	if err != nil || !observed.Known || !observed.Exists || observed.Interrupted {
		t.Fatal("preemption op misclassified despite a surviving VM", observed, err)
	}
}
func TestGCPSDKCapacityCodeWithSurvivingVMStaysUnknown(t *testing.T) {
	var p *Command
	p = gcpFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			if strings.Contains(r.URL.Path, "/instances/") {
				writeJSON(w, gcpOwned("instances"))
				return
			}
			gcpMissing(w)
			return
		}
		op := gcpOperation(p, "create", "instances", "rs-test", "DONE")
		op["error"] = map[string]any{"errors": []any{map[string]any{"code": "ZONE_RESOURCE_POOL_EXHAUSTED", "message": "no capacity"}}}
		writeJSON(w, op)
	})
	id, err := p.Create(context.Background(), gcpAllocation())
	if id != "" || err == nil || errors.Is(err, lifecycle.ErrCapacity) {
		t.Fatal("capacity code misclassified despite a surviving VM", id, err)
	}
}
func TestGCPSDKPendingCreateHonorsCancellation(t *testing.T) {
	var created atomic.Bool
	var p *Command
	p = gcpFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			gcpMissing(w)
			return
		}
		created.Store(true)
		writeJSON(w, gcpOperation(p, "create", "instances", "rs-test", "RUNNING"))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	id, err := p.Create(ctx, gcpAllocation())
	if !created.Load() || err == nil || id != "" || errors.Is(err, lifecycle.ErrCapacity) || time.Since(started) > time.Second {
		t.Fatal("pending creation escaped deadline or became capacity rejection", id, err)
	}
}
