package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/tsouza/runnerscout/internal/lifecycle"
)

const azureFixtureNICUID = "33333333-3333-4333-8333-333333333333"

func azureOwnedDisk() map[string]any {
	disk := azureCreationDisk()
	disk["tags"] = map[string]string{"runnerscout-owner": "test", "runnerscout-operation": "rs-test"}
	return disk
}

type azureInventoryFixtureState struct {
	mu               sync.Mutex
	resources        map[string]map[string]any
	omitted          map[string]bool
	deletes          []string
	created          bool
	lostTag          bool
	creates, patches int
}

func azureInventoryFixture(t *testing.T) (*Command, *azureInventoryFixtureState) {
	t.Helper()
	state := &azureInventoryFixtureState{resources: map[string]map[string]any{}, omitted: map[string]bool{}, created: true}
	for _, resource := range []map[string]any{azureCreationVM(), azureOwnedDisk(), azureOwnedNIC()} {
		state.resources[strings.ToLower(resource["id"].(string))] = resource
	}
	p, _ := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		id := strings.ToLower(r.URL.Path)
		if strings.EqualFold(r.URL.Path, azureFixtureImage) && r.Method == "GET" {
			writeJSON(w, azureSupportedImage())
			return
		}
		if deploymentPath(r) && r.Method == "PUT" {
			state.created = true
			state.creates++
			writeJSON(w, map[string]any{"properties": map[string]any{"provisioningState": "Succeeded"}})
			return
		}
		if !state.created {
			w.WriteHeader(404)
			writeJSON(w, map[string]any{"error": map[string]string{"code": "ResourceNotFound"}})
			return
		}
		if deploymentPath(r) && r.Method == "GET" {
			writeJSON(w, map[string]any{"properties": map[string]any{"provisioningState": "Succeeded"}})
			return
		}
		if strings.HasSuffix(id, "/resources") && r.Method == "GET" {
			items := []any{}
			for key, resource := range state.resources {
				if !state.omitted[key] {
					items = append(items, resource)
				}
			}
			writeJSON(w, map[string]any{"value": items})
			return
		}
		resource, exists := state.resources[id]
		if r.Method == "PATCH" && strings.Contains(id, "/disks/") && exists {
			state.patches++
			var body struct {
				Tags map[string]string `json:"tags"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			resource["tags"] = body.Tags
			if state.lostTag {
				w.WriteHeader(500)
				writeJSON(w, map[string]any{"error": map[string]string{"code": "InternalServerError"}})
				return
			}
			writeJSON(w, resource)
			return
		}
		if r.Method == "DELETE" {
			state.deletes = append(state.deletes, id)
			if !exists {
				t.Error("attempted deletion of absent resource", id)
			}
			delete(state.resources, id)
			if strings.Contains(id, "/virtualmachines/") {
				for _, child := range state.resources {
					delete(child, "managedBy")
					delete(child["properties"].(map[string]any), "virtualMachine")
				}
			}
			w.WriteHeader(204)
			return
		}
		if r.Method != "GET" {
			t.Error("unexpected cloud mutation", r.Method, id)
			w.WriteHeader(400)
			return
		}
		if exists {
			writeJSON(w, resource)
			return
		}
		w.WriteHeader(404)
		writeJSON(w, map[string]any{"error": map[string]string{"code": "ResourceNotFound"}})
	})
	return p, state
}

func TestAzureRecordedGenerationsRejectReplacementAndRetainDiscoveryLoss(t *testing.T) {
	for _, mode := range []string{"vm-replaced", "disk-replaced", "nic-replaced", "discovery-omitted", "tags-removed", "foreign-attachment", "path-case"} {
		t.Run(mode, func(t *testing.T) {
			p, state := azureInventoryFixture(t)
			a := allocation()
			observed, err := p.Observe(context.Background(), a)
			if err != nil || len(observed.Resources) != 3 {
				t.Fatal(observed, err)
			}
			a.ResourceID, a.Resources = observed.ResourceID, observed.Resources
			state.mu.Lock()
			for key, resource := range state.resources {
				props := resource["properties"].(map[string]any)
				switch {
				case mode == "vm-replaced" && strings.Contains(key, "/virtualmachines/"):
					props["vmId"] = "44444444-4444-4444-8444-444444444444"
				case mode == "disk-replaced" && strings.Contains(key, "/disks/"):
					props["uniqueId"] = "44444444-4444-4444-8444-444444444444"
				case mode == "nic-replaced" && strings.Contains(key, "/networkinterfaces/"):
					props["resourceGuid"] = "44444444-4444-4444-8444-444444444444"
				case mode == "discovery-omitted":
					state.omitted[key] = true
				case mode == "tags-removed" && strings.Contains(key, "/disks/"):
					state.omitted[key] = true
					resource["tags"] = map[string]string{}
				case mode == "foreign-attachment" && strings.Contains(key, "/disks/"):
					resource["managedBy"] = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/other"
				}
			}
			state.mu.Unlock()
			if mode == "path-case" {
				for i := range a.Resources {
					a.Resources[i].ID = strings.ToUpper(a.Resources[i].ID)
				}
			}
			observed, err = p.Observe(context.Background(), a)
			valid := mode == "discovery-omitted" || mode == "path-case"
			if valid {
				if err != nil || !observed.Known || !observed.Exists || len(observed.Resources) != 3 {
					t.Fatal("direct identity read lost owned resource", observed, err)
				}
				if _, changed, err := lifecycle.MergeResources(a.Resources, observed.Resources); err != nil || changed {
					t.Fatal("resource spelling changed durable identities", err)
				}
			} else {
				if err == nil || observed.Known {
					t.Fatal("replacement/unowned dependency accepted", mode, observed, err)
				}
				if err := p.Delete(context.Background(), a); err == nil {
					t.Fatal("unsafe cleanup accepted", mode)
				}
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if len(state.deletes) != 0 {
				t.Fatal("uncertain inventory caused deletion", state.deletes)
			}
		})
	}
}

func TestAzureCleanupRetainsGenerationsUntilAllResourcesAreAbsent(t *testing.T) {
	p, state := azureInventoryFixture(t)
	a := allocation()
	observed, err := p.Observe(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	a.ResourceID, a.Resources = observed.ResourceID, observed.Resources
	for step := 0; step < 3; step++ {
		// Serialize and reload the allocation between deletion steps.
		encoded, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		var restarted lifecycle.Allocation
		if err := json.Unmarshal(encoded, &restarted); err != nil {
			t.Fatal(err)
		}
		if err := p.Delete(context.Background(), restarted); err != nil {
			t.Fatal("ordered cleanup failed", step, err)
		}
		observed, err = p.Observe(context.Background(), restarted)
		if err != nil || !observed.Known || observed.Exists != (step < 2) || len(observed.Resources) != 3 {
			t.Fatal("cleanup discarded a generation or claimed early absence", step, observed, err)
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.deletes) != 3 || !strings.Contains(state.deletes[0], "/virtualmachines/") || !strings.Contains(state.deletes[1], "/networkinterfaces/") || !strings.Contains(state.deletes[2], "/disks/") {
		t.Fatal("cleanup order differs", state.deletes)
	}
}

func TestAzureCreationReceiptKeepsGenerationsAfterLostTagResponse(t *testing.T) {
	for _, mode := range []string{"recover", "vm-replaced", "disk-replaced", "nic-replaced"} {
		t.Run(mode, func(t *testing.T) {
			p, state := azureInventoryFixture(t)
			state.mu.Lock()
			state.created = false
			state.lostTag = true
			for key, resource := range state.resources {
				if strings.Contains(key, "/disks/") {
					resource["tags"] = map[string]string{}
				}
			}
			state.mu.Unlock()
			a := allocation()
			a.Offering.Image = azureFixtureImage
			receipt, err := p.CreateWithResources(context.Background(), a)
			if err == nil || receipt.ResourceID == "" || len(receipt.Resources) != 3 {
				t.Fatal("uncertain creation lost original identities", receipt, err)
			}
			a.Phase = lifecycle.Creating
			a.ResourceID = receipt.ResourceID
			a.Resources = receipt.Resources
			state.mu.Lock()
			for key, resource := range state.resources {
				props := resource["properties"].(map[string]any)
				switch {
				case mode == "vm-replaced" && strings.Contains(key, "/virtualmachines/"):
					props["vmId"] = "44444444-4444-4444-8444-444444444444"
				case mode == "disk-replaced" && strings.Contains(key, "/disks/"):
					props["uniqueId"] = "44444444-4444-4444-8444-444444444444"
				case mode == "nic-replaced" && strings.Contains(key, "/networkinterfaces/"):
					props["resourceGuid"] = "44444444-4444-4444-8444-444444444444"
				}
			}
			state.mu.Unlock()
			observed, err := p.ReconcileCreation(context.Background(), a)
			if mode == "recover" {
				if err != nil || !observed.Known || !observed.Exists || len(observed.Resources) != 3 {
					t.Fatal("lost-response creation did not recover", observed, err)
				}
			} else if err == nil || observed.Known {
				t.Fatal("creation recovery adopted a replacement", mode, observed, err)
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if state.creates != 1 || state.patches != 1 || len(state.deletes) != 0 {
				t.Fatal("recovery repeated effects", state.creates, state.patches, state.deletes)
			}
		})
	}
}

func TestAzureVMDeletionRefusesUntrackedAttachments(t *testing.T) {
	for _, mode := range []string{"extra-disk", "other-os-disk", "other-nic", "missing-profile"} {
		t.Run(mode, func(t *testing.T) {
			p, state := azureInventoryFixture(t)
			a := allocation()
			observed, err := p.Observe(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			a.ResourceID, a.Resources = observed.ResourceID, observed.Resources
			state.mu.Lock()
			for key, resource := range state.resources {
				if !strings.Contains(key, "/virtualmachines/") {
					continue
				}
				props := resource["properties"].(map[string]any)
				storage := props["storageProfile"].(map[string]any)
				switch mode {
				case "extra-disk":
					storage["dataDisks"] = []any{map[string]any{"managedDisk": map[string]string{"id": "/foreign-disk"}}}
				case "other-os-disk":
					storage["osDisk"].(map[string]any)["managedDisk"] = map[string]string{"id": "/foreign-os-disk"}
				case "other-nic":
					props["networkProfile"] = map[string]any{"networkInterfaces": []any{map[string]string{"id": "/foreign-nic"}}}
				case "missing-profile":
					delete(props, "networkProfile")
				}
			}
			state.mu.Unlock()
			if err := p.Delete(context.Background(), a); err == nil {
				t.Fatal("untracked attachment allowed VM deletion", mode)
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			if len(state.deletes) != 0 {
				t.Fatal("unsafe cascade delete", state.deletes)
			}
		})
	}
}

func TestAzureAbsenceRequiresRecognizedNotFoundResponses(t *testing.T) {
	for _, mode := range []string{"known-empty", "deployment-empty", "deployment-wrong-code", "resource-empty", "resource-wrong-code"} {
		t.Run(mode, func(t *testing.T) {
			p, _ := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("absence probe mutated cloud state", r.Method)
				}
				if deploymentPath(r) {
					w.WriteHeader(404)
					if mode == "deployment-empty" {
						return
					}
					code := "DeploymentNotFound"
					if mode == "deployment-wrong-code" {
						code = "AuthorizationFailed"
					}
					writeJSON(w, map[string]any{"error": map[string]string{"code": code}})
					return
				}
				if strings.HasSuffix(strings.ToLower(r.URL.Path), "/resources") {
					writeJSON(w, map[string]any{"value": []any{}})
					return
				}
				w.WriteHeader(404)
				if mode == "resource-empty" {
					return
				}
				code := "ResourceNotFound"
				if mode == "resource-wrong-code" {
					code = "AuthorizationFailed"
				}
				writeJSON(w, map[string]any{"error": map[string]string{"code": code}})
			})
			observed, err := p.Observe(context.Background(), allocation())
			if mode == "known-empty" {
				if err != nil || !observed.Known || observed.Exists {
					t.Fatal("valid absence refused", observed, err)
				}
			} else if err == nil || observed.Known {
				t.Fatal("unknown response became absence", mode, observed, err)
			}
		})
	}
}

func azureOwnedNIC() map[string]any {
	nic := ownedResource("Microsoft.Network/networkInterfaces", "rs-test-nic", "test")
	nic["properties"] = map[string]any{"resourceGuid": azureFixtureNICUID, "virtualMachine": map[string]string{"id": "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/rs-test"}}
	return nic
}

func TestAzureObservationRetainsImmutableResourceIdentities(t *testing.T) {
	resources := []map[string]any{azureCreationVM(), azureOwnedDisk(), azureOwnedNIC()}
	p, _ := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("observation mutated cloud resources", r.Method)
		}
		if deploymentPath(r) {
			writeJSON(w, map[string]any{"properties": map[string]any{"provisioningState": "Succeeded"}})
			return
		}
		if strings.HasSuffix(strings.ToLower(r.URL.Path), "/resources") {
			writeJSON(w, map[string]any{"value": resources})
			return
		}
		for _, resource := range resources {
			if strings.EqualFold(r.URL.Path, resource["id"].(string)) {
				writeJSON(w, resource)
				return
			}
		}
		w.WriteHeader(404)
		writeJSON(w, map[string]any{"error": map[string]string{"code": "ResourceNotFound"}})
	})
	observed, err := p.Observe(context.Background(), allocation())
	if err != nil || !observed.Known || !observed.Exists {
		t.Fatal("owned inventory unavailable", observed, err)
	}
	data, err := json.Marshal(observed.Resources)
	if err != nil {
		t.Fatal(err)
	}
	var references []map[string]string
	if err := json.Unmarshal(data, &references); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"azure-vm": azureFixtureVMUID, "azure-disk": azureFixtureDiskUID, "azure-network-interface": azureFixtureNICUID}
	if len(references) != len(want) {
		t.Fatalf("inventory discarded durable identities: %s", data)
	}
	for _, reference := range references {
		if want[reference["kind"]] != reference["uid"] || reference["uid"] == "" || reference["id"] == "" {
			t.Fatal("service identity missing from receipt", reference)
		}
		delete(want, reference["kind"])
	}
	if len(want) != 0 {
		t.Fatal("missing resource identity", want)
	}
}
