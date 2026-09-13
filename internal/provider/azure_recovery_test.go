package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAzureCreationCannotRetagForeignDisk(t *testing.T) {
	var updates atomic.Int32
	p, _ := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if deploymentPath(r) && r.Method == "PUT" {
			writeJSON(w, map[string]any{"properties": map[string]any{"provisioningState": "Succeeded"}})
			return
		}
		if strings.HasSuffix(strings.ToLower(r.URL.Path), "/disks/rs-test-os") {
			if r.Method == "PATCH" {
				updates.Add(1)
			}
			writeJSON(w, ownedResource("Microsoft.Compute/disks", "rs-test-os", "foreign-owner"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"error": map[string]string{"code": "ResourceNotFound"}})
	})
	a := allocation()
	a.Offering.Image = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/images/test"
	if _, err := p.Create(context.Background(), a); err == nil || updates.Load() != 0 {
		t.Fatalf("creation retagged a foreign disk: updates=%d error=%v", updates.Load(), err)
	}
}

const azureFixtureVMUID = "11111111-1111-4111-8111-111111111111"
const azureFixtureDiskUID = "22222222-2222-4222-8222-222222222222"
const azureFixtureImage = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/images/test"

func azureCreationVM() map[string]any {
	vm := ownedResource("Microsoft.Compute/virtualMachines", "rs-test", "test")
	vm["properties"] = map[string]any{"vmId": azureFixtureVMUID, "provisioningState": "Succeeded", "storageProfile": map[string]any{"imageReference": map[string]any{"id": azureFixtureImage}, "osDisk": map[string]any{"name": "rs-test-os", "createOption": "FromImage", "managedDisk": map[string]any{"id": "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/disks/rs-test-os"}}}}
	return vm
}
func azureCreationDisk() map[string]any {
	disk := ownedResource("Microsoft.Compute/disks", "rs-test-os", "test")
	disk["tags"] = map[string]string{"cost-center": "preserved"}
	disk["managedBy"] = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/rs-test"
	disk["properties"] = map[string]any{"uniqueId": azureFixtureDiskUID, "provisioningState": "Succeeded", "creationData": map[string]any{"createOption": "FromImage", "imageReference": map[string]any{"id": azureFixtureImage}}}
	return disk
}
func TestAzureDiskTaggingRequiresOwnedCreationGraph(t *testing.T) {
	for _, mode := range []string{"untagged", "already-owned", "owned-case", "foreign-case", "ambiguous-case", "foreign-owner", "foreign-operation", "foreign-vm", "wrong-managed-by", "wrong-vm-disk", "wrong-image", "wrong-disk-image", "missing-properties", "invalid-uid", "attached-existing-disk", "tag-response-lost", "tag-not-applied", "disk-replaced", "vm-replaced"} {
		t.Run(mode, func(t *testing.T) {
			disk, vm := azureCreationDisk(), azureCreationVM()
			tags := disk["tags"].(map[string]string)
			properties := disk["properties"].(map[string]any)
			vmProperties := vm["properties"].(map[string]any)
			storage := vmProperties["storageProfile"].(map[string]any)
			switch mode {
			case "already-owned":
				tags["runnerscout-owner"] = "test"
				tags["runnerscout-operation"] = "rs-test"
			case "owned-case":
				tags["RunnerScout-Owner"] = "test"
				tags["RunnerScout-Operation"] = "rs-test"
			case "foreign-case":
				tags["RunnerScout-Owner"] = "foreign-owner"
			case "ambiguous-case":
				tags["RunnerScout-Owner"] = "foreign-owner"
				tags["runnerscout-owner"] = "test"
			case "foreign-owner":
				tags["runnerscout-owner"] = "foreign-owner"
			case "foreign-operation":
				tags["runnerscout-operation"] = "rs-other"
			case "foreign-vm":
				vm["tags"].(map[string]string)["runnerscout-owner"] = "foreign-owner"
			case "wrong-managed-by":
				disk["managedBy"] = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/other"
			case "wrong-vm-disk":
				storage["osDisk"].(map[string]any)["managedDisk"] = map[string]any{"id": "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/disks/other"}
			case "wrong-image":
				storage["imageReference"] = map[string]any{"id": azureFixtureImage + "-other"}
			case "wrong-disk-image":
				properties["creationData"].(map[string]any)["imageReference"] = map[string]any{"id": azureFixtureImage + "-other"}
			case "missing-properties":
				delete(disk, "properties")
			case "invalid-uid":
				properties["uniqueId"] = ""
			case "attached-existing-disk":
				storage["osDisk"].(map[string]any)["createOption"] = "Attach"
			}
			patches := 0
			p, _ := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if deploymentPath(r) && r.Method == "PUT" {
					writeJSON(w, map[string]any{"properties": map[string]any{"provisioningState": "Succeeded"}})
					return
				}
				if r.Method == "GET" && strings.HasSuffix(strings.ToLower(r.URL.Path), "/virtualmachines/rs-test") {
					writeJSON(w, vm)
					return
				}
				if strings.HasSuffix(strings.ToLower(r.URL.Path), "/disks/rs-test-os") {
					if r.Method == "GET" {
						writeJSON(w, disk)
						return
					}
					if r.Method == "PATCH" {
						patches++
						var update struct {
							Tags map[string]string `json:"tags"`
						}
						if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
							t.Error(err)
						}
						if update.Tags["cost-center"] != "preserved" {
							t.Error("unrelated disk tags discarded")
						}
						if mode != "tag-not-applied" {
							disk["tags"] = update.Tags
						}
						if mode == "disk-replaced" {
							properties["uniqueId"] = "33333333-3333-4333-8333-333333333333"
						}
						if mode == "vm-replaced" {
							vmProperties["vmId"] = "33333333-3333-4333-8333-333333333333"
						}
						if mode == "tag-response-lost" {
							w.WriteHeader(500)
							writeJSON(w, map[string]any{"error": map[string]string{"code": "InternalServerError"}})
							return
						}
						writeJSON(w, disk)
						return
					}
				}
				t.Error("unexpected ARM call", r.Method, r.URL.Path)
				w.WriteHeader(400)
			})
			a := allocation()
			a.Offering.Image = azureFixtureImage
			receipt, err := p.CreateWithResources(context.Background(), a)
			success := mode == "untagged" || mode == "already-owned" || mode == "owned-case"
			if (err == nil) != success {
				t.Fatalf("unexpected create outcome: %v", err)
			}
			if receipt.ResourceID != "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/rs-test" {
				t.Fatal("successful deployment receipt lost", receipt)
			}
			afterUpdate := mode == "tag-response-lost" || mode == "tag-not-applied" || mode == "disk-replaced" || mode == "vm-replaced"
			wantPatches := 0
			if mode == "untagged" || afterUpdate {
				wantPatches = 1
			}
			if patches != wantPatches {
				t.Fatalf("unsafe/repeated tag effect: patches=%d want=%d", patches, wantPatches)
			}
			if err != nil && strings.Contains(fmt.Sprint(err), "jit-secret") {
				t.Fatal("private bootstrap in diagnostic")
			}
		})
	}
}

func TestAzureLostTaggingRecoversWithoutRedeployment(t *testing.T) {
	for _, mode := range []string{"recover", "tag-response-lost", "foreign-nic", "foreign-disk", "parent-missing", "deployment-active", "deployment-failed", "inventory-unavailable", "receipt-mismatch", "read-only-observe", "not-creating"} {
		t.Run(mode, func(t *testing.T) {
			disk, vm := azureCreationDisk(), azureCreationVM()
			nic := ownedResource("Microsoft.Network/networkInterfaces", "rs-test-nic", "test")
			if mode == "foreign-nic" {
				nic["tags"].(map[string]string)["runnerscout-owner"] = "foreign"
			}
			if mode == "foreign-disk" {
				disk["tags"].(map[string]string)["runnerscout-owner"] = "foreign"
			}
			writes, deployments := 0, 0
			p, _ := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if deploymentPath(r) {
					if r.Method != "GET" {
						deployments++
						w.WriteHeader(400)
						return
					}
					state := "Succeeded"
					if mode == "deployment-active" {
						state = "Running"
					} else if mode == "deployment-failed" {
						state = "Failed"
					}
					writeJSON(w, map[string]any{"properties": map[string]any{"provisioningState": state}})
					return
				}
				if strings.HasSuffix(strings.ToLower(r.URL.Path), "/resources") {
					if mode == "inventory-unavailable" {
						w.WriteHeader(403)
						writeJSON(w, map[string]any{"error": map[string]string{"code": "AuthorizationFailed"}})
						return
					}
					if mode == "deployment-active" {
						t.Error("inventory used before deployment became terminal")
					}
					resources := []any{disk, nic}
					if mode != "parent-missing" {
						resources = append(resources, vm)
					}
					writeJSON(w, map[string]any{"value": resources})
					return
				}
				if strings.HasSuffix(strings.ToLower(r.URL.Path), "/virtualmachines/rs-test") {
					if mode == "parent-missing" {
						w.WriteHeader(404)
						writeJSON(w, map[string]any{"error": map[string]string{"code": "ResourceNotFound"}})
						return
					}
					writeJSON(w, vm)
					return
				}
				if strings.HasSuffix(strings.ToLower(r.URL.Path), "/disks/rs-test-os") {
					if r.Method == "PATCH" {
						writes++
						var update struct {
							Tags map[string]string `json:"tags"`
						}
						if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
							t.Error(err)
						}
						disk["tags"] = update.Tags
						if mode == "tag-response-lost" {
							w.WriteHeader(500)
							writeJSON(w, map[string]any{"error": map[string]string{"code": "InternalServerError"}})
							return
						}
					}
					writeJSON(w, disk)
					return
				}
				t.Error("unexpected ARM operation", r.Method, r.URL.Path)
				w.WriteHeader(400)
			})
			a := allocation()
			a.Offering.Image = azureFixtureImage
			a.Phase = lifecycle.Creating
			if mode == "receipt-mismatch" {
				a.ResourceID = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/other"
			}
			if mode == "not-creating" {
				a.Phase = lifecycle.Running
			}
			var observed lifecycle.Observation
			var err error
			if mode == "read-only-observe" {
				observed, err = p.Observe(context.Background(), a)
			} else {
				observed, err = p.ReconcileCreation(context.Background(), a)
			}
			if deployments != 0 {
				t.Fatal("creation recovery redeployed resources")
			}
			if mode != "recover" && mode != "tag-response-lost" && mode != "deployment-failed" {
				if err == nil || observed.Known || writes != 0 {
					t.Fatal("unsafe recovery changed ownership", mode, writes, observed, err)
				}
				return
			}
			if writes != 1 {
				t.Fatal("tagging was not recovered", writes, err)
			}
			if mode == "tag-response-lost" {
				if err == nil || observed.Known {
					t.Fatal("lost update response reported success")
				}
			} else if err != nil || !observed.Known || !observed.Exists {
				t.Fatal("owned graph did not recover", observed, err)
			}
			// A restarted controller can observe the applied tag without repeating PATCH.
			observed, err = p.ReconcileCreation(context.Background(), a)
			if err != nil || !observed.Known || !observed.Exists || writes != 1 || deployments != 0 {
				t.Fatal("recovery repeated a completed effect", observed, writes, err)
			}
		})
	}
}
