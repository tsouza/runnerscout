package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/tsouza/runnerscout/internal/lifecycle"
)

func azureConfig() Config {
	return Config{Kind: "azure", Owner: "test", Subscription: "sub", ResourceGroup: "rg", Subnet: "/subnet", SecurityGroup: "/nsg", SSHPublicKey: "ssh-ed25519 test"}
}

type testAzureToken struct {
	t     *testing.T
	calls atomic.Int32
	fail  bool
}

func (c *testAzureToken) GetToken(_ context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error) {
	c.calls.Add(1)
	if c.fail {
		return azcore.AccessToken{}, errors.New("secret credential diagnostic")
	}
	if len(options.Scopes) != 1 || options.Scopes[0] != "https://management.azure.com/.default" {
		c.t.Errorf("unexpected token scopes: %v", options.Scopes)
		return azcore.AccessToken{}, errors.New("unexpected token scope")
	}
	return azcore.AccessToken{Token: "local-test-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}
func sdkFixture(t *testing.T, handler http.HandlerFunc) (*Command, *testAzureToken) {
	t.Helper()
	token := &testAzureToken{t: t}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer local-test-token" {
			t.Error("SDK did not authenticate ARM request")
		}
		if r.URL.Query().Get("api-version") == "" {
			t.Error("missing API version")
		}
		if !strings.HasPrefix(r.URL.Path, "/subscriptions/sub/resourcegroups/rg/") && !strings.HasPrefix(r.URL.Path, "/subscriptions/sub/resourceGroups/rg/") {
			t.Errorf("wrong account/resource-group scope: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	options := &arm.ClientOptions{ClientOptions: policy.ClientOptions{Transport: server.Client(), Retry: policy.RetryOptions{MaxRetries: -1}, Cloud: cloud.Configuration{ActiveDirectoryAuthorityHost: cloud.AzurePublic.ActiveDirectoryAuthorityHost, Services: map[cloud.ServiceName]cloud.ServiceConfiguration{cloud.ResourceManager: {Audience: "https://management.azure.com", Endpoint: server.URL}}}}}
	return &Command{Config: azureConfig(), Azure: &AzureSDK{Credential: token, Options: options}, Bootstrap: func(context.Context, string) (string, error) { return "jit-secret", nil }}, token
}
func writeJSON(w http.ResponseWriter, value any) { _ = json.NewEncoder(w).Encode(value) }

// Model an empty resource group until this test submits its deployment. Recovery
// tests use sdkFixture directly because their resources are already committed.
func azureCreationFixture(t *testing.T, handler http.HandlerFunc) (*Command, *testAzureToken) {
	t.Helper()
	deployed := false
	return sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.URL.Path, azureFixtureImage) && r.Method == "GET" {
			writeJSON(w, azureSupportedImage())
			return
		}
		if !deployed && r.Method == "GET" {
			w.WriteHeader(404)
			writeJSON(w, map[string]any{"error": map[string]string{"code": "ResourceNotFound"}})
			return
		}
		if deploymentPath(r) && r.Method == "PUT" {
			deployed = true
		}
		if deployed && r.Method == "GET" && strings.HasSuffix(strings.ToLower(r.URL.Path), "/resources") {
			// The list view can lag the disk tag update. Direct reads below use
			// the test's current resource state and decide its ownership.
			writeJSON(w, map[string]any{"value": []any{azureCreationVM(), azureCreationDisk(), azureOwnedNIC()}})
			return
		}
		if deployed && r.Method == "GET" && strings.HasSuffix(strings.ToLower(r.URL.Path), "/networkinterfaces/rs-test-nic") {
			writeJSON(w, azureOwnedNIC())
			return
		}
		handler(w, r)
	})
}
func deploymentPath(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.URL.Path), "/microsoft.resources/deployments/")
}
func ownedResource(kind, name, owner string) map[string]any {
	return map[string]any{"id": "/subscriptions/sub/resourceGroups/rg/providers/" + kind + "/" + name, "name": name, "type": kind, "tags": map[string]string{"runnerscout-owner": owner, "runnerscout-operation": "rs-test"}}
}

func TestAzureCreateUsesSecureBootstrapAndSpotDelete(t *testing.T) {
	var requests []string
	disk := azureCreationDisk()
	p, token := azureCreationFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.Method == "GET" {
			if strings.HasSuffix(strings.ToLower(r.URL.Path), "/disks/rs-test-os") {
				writeJSON(w, disk)
			} else {
				writeJSON(w, azureCreationVM())
			}
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if deploymentPath(r) {
			if r.Method != "PUT" {
				t.Error(r.Method)
			}
			props := body["properties"].(map[string]any)
			template := props["template"].(map[string]any)
			if template["parameters"].(map[string]any)["bootstrap"].(map[string]any)["type"] != "securestring" {
				t.Error("bootstrap not securestring")
			}
			resources := template["resources"].([]any)
			vm := resources[1].(map[string]any)["properties"].(map[string]any)
			if vm["priority"] != "Spot" || vm["evictionPolicy"] != "Delete" {
				t.Error("spot contract changed")
			}
			if vm["osProfile"].(map[string]any)["customData"] != "[parameters('bootstrap')]" {
				t.Error("secret embedded in template")
			}
			if vm["storageProfile"].(map[string]any)["osDisk"].(map[string]any)["deleteOption"] != "Delete" {
				t.Error("disk cleanup missing")
			}
			nics := vm["networkProfile"].(map[string]any)["networkInterfaces"].([]any)
			if len(nics) != 1 || nics[0].(map[string]any)["properties"].(map[string]any)["deleteOption"] != "Delete" {
				t.Error("interface cleanup missing")
			}
			writeJSON(w, map[string]any{"properties": map[string]string{"provisioningState": "Succeeded"}})
		} else {
			if r.Method != "PATCH" || !strings.HasSuffix(r.URL.Path, "/disks/rs-test-os") {
				t.Error("unexpected disk update", r.Method, r.URL.Path)
			}
			if body["tags"].(map[string]any)["runnerscout-owner"] != "test" {
				t.Error("disk owner missing")
			}
			disk["tags"] = body["tags"]
			writeJSON(w, disk)
		}
	})
	a := allocation()
	a.Offering.Image = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/images/test"
	id, err := p.Create(context.Background(), a)
	deployments, updates := 0, 0
	for _, request := range requests {
		if strings.HasPrefix(request, "PUT ") {
			deployments++
		}
		if strings.HasPrefix(request, "PATCH ") {
			updates++
		}
	}
	if err != nil || !strings.HasSuffix(id, "/virtualMachines/rs-test") || deployments != 1 || updates != 1 || token.calls.Load() == 0 {
		t.Fatalf("create: id=%q err=%v requests=%v", id, err, requests)
	}
}

// TestAzureCreateDiskControllerTypeOptIn confirms Config.AzureDiskControllerType
// is purely additive: empty (its zero value, and every real caller's value
// today) omits storageProfile.diskControllerType from the ARM template
// entirely - not present with an empty-string value - preserving Azure's
// own default inference exactly as before this field existed. A non-empty
// value is passed through verbatim.
func TestAzureCreateDiskControllerTypeOptIn(t *testing.T) {
	for _, controllerType := range []string{"", "NVMe", "SCSI"} {
		t.Run("controllerType="+controllerType, func(t *testing.T) {
			var storageProfile map[string]any
			disk := azureCreationDisk()
			p, _ := azureCreationFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					if strings.HasSuffix(strings.ToLower(r.URL.Path), "/disks/rs-test-os") {
						writeJSON(w, disk)
					} else {
						writeJSON(w, azureCreationVM())
					}
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if deploymentPath(r) {
					resources := body["properties"].(map[string]any)["template"].(map[string]any)["resources"].([]any)
					storageProfile = resources[1].(map[string]any)["properties"].(map[string]any)["storageProfile"].(map[string]any)
					writeJSON(w, map[string]any{"properties": map[string]string{"provisioningState": "Succeeded"}})
					return
				}
				disk["tags"] = body["tags"]
				writeJSON(w, disk)
			})
			p.Config.AzureDiskControllerType = controllerType
			a := allocation()
			a.Offering.Image = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/images/test"
			if _, err := p.Create(context.Background(), a); err != nil {
				t.Fatalf("create: %v", err)
			}
			got, present := storageProfile["diskControllerType"]
			if controllerType == "" {
				if present {
					t.Fatalf("expected diskControllerType omitted for empty Config value, got %q", got)
				}
				return
			}
			if !present || got != controllerType {
				t.Fatalf("expected diskControllerType=%q, got %q (present=%v)", controllerType, got, present)
			}
		})
	}
}

// azureFailedDeploymentFixture models a deployment that reaches a terminal
// Failed provisioning state before any of its resources exist. residual lets
// a test prove a surviving resource blocks capacity classification.
func azureFailedDeploymentFixture(t *testing.T, deploymentError map[string]any, residual []any) (*Command, *testAzureToken) {
	t.Helper()
	deployed := false
	absent := func(w http.ResponseWriter) {
		w.WriteHeader(404)
		writeJSON(w, map[string]any{"error": map[string]string{"code": "ResourceNotFound"}})
	}
	return sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.URL.Path, azureFixtureImage) && r.Method == "GET" {
			writeJSON(w, azureSupportedImage())
			return
		}
		if deploymentPath(r) {
			if r.Method == "PUT" {
				deployed = true
			}
			if !deployed {
				absent(w)
				return
			}
			writeJSON(w, map[string]any{"properties": map[string]any{"provisioningState": "Failed", "error": deploymentError}})
			return
		}
		if !deployed {
			absent(w)
			return
		}
		if strings.HasSuffix(strings.ToLower(r.URL.Path), "/resources") {
			writeJSON(w, map[string]any{"value": residual})
			return
		}
		absent(w)
	})
}
func TestAzureCreateDefinitiveCapacityRejectionHasNoReceipt(t *testing.T) {
	p, _ := azureFailedDeploymentFixture(t, map[string]any{
		"code":    "DeploymentFailed",
		"message": "top",
		"details": []any{map[string]any{"code": "OverconstrainedAllocationRequest", "message": "no capacity"}},
	}, []any{})
	a := allocation()
	a.Offering.Image = azureFixtureImage
	receipt, err := p.createAzure(context.Background(), a, "boot")
	if !errors.Is(err, lifecycle.ErrCapacity) || receipt.ResourceID != "" || len(receipt.Resources) != 0 {
		t.Fatal("definitive capacity rejection misclassified", receipt, err)
	}
}
func TestAzureCreateAmbiguousDeploymentFailureStaysUnknown(t *testing.T) {
	p, _ := azureFailedDeploymentFixture(t, map[string]any{
		"code":    "DeploymentFailed",
		"message": "top",
		"details": []any{map[string]any{"code": "ResourceQuotaExceeded", "message": "quota"}},
	}, []any{})
	a := allocation()
	a.Offering.Image = azureFixtureImage
	receipt, err := p.createAzure(context.Background(), a, "boot")
	if err == nil || errors.Is(err, lifecycle.ErrCapacity) || receipt.ResourceID != "" {
		t.Fatal("ambiguous deployment failure misclassified as capacity", receipt, err)
	}
}
func TestAzureCreateCapacityCodeWithResidualNICStaysUnknown(t *testing.T) {
	p, _ := azureFailedDeploymentFixture(t, map[string]any{
		"code":    "DeploymentFailed",
		"message": "top",
		"details": []any{map[string]any{"code": "OverconstrainedAllocationRequest", "message": "no capacity"}},
	}, []any{azureOwnedNIC()})
	a := allocation()
	a.Offering.Image = azureFixtureImage
	receipt, err := p.createAzure(context.Background(), a, "boot")
	if err == nil || errors.Is(err, lifecycle.ErrCapacity) || receipt.ResourceID != "" {
		t.Fatal("capacity code misclassified despite a surviving resource", receipt, err)
	}
}
func TestAzureActiveDeploymentCannotProveAbsence(t *testing.T) {
	requests := 0
	p, _ := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if !deploymentPath(r) {
			t.Error("listed inventory before deployment completion")
		}
		writeJSON(w, map[string]any{"properties": map[string]string{"provisioningState": "Running"}})
	})
	ob, err := p.Observe(context.Background(), allocation())
	if err == nil || ob.Known || requests != 1 {
		t.Fatal(ob, err, requests)
	}
}
func TestAzureForeignDiskCannotBeDeleted(t *testing.T) {
	p, _ := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("mutation of foreign resource")
		}
		if deploymentPath(r) {
			w.WriteHeader(404)
			writeJSON(w, map[string]any{"error": map[string]string{"code": "DeploymentNotFound"}})
			return
		}
		writeJSON(w, map[string]any{"value": []any{ownedResource("Microsoft.Compute/disks", "rs-test-os", "foreign")}})
	})
	if err := p.Delete(context.Background(), allocation()); err == nil {
		t.Fatal("foreign disk accepted")
	}
}
func TestAzureResidualOwnedNICRetainsCleanup(t *testing.T) {
	nic := azureOwnedNIC()
	delete(nic["properties"].(map[string]any), "virtualMachine")
	p, _ := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if deploymentPath(r) {
			w.WriteHeader(404)
			writeJSON(w, map[string]any{"error": map[string]string{"code": "DeploymentNotFound"}})
			return
		}
		if strings.HasSuffix(strings.ToLower(r.URL.Path), "/resources") {
			writeJSON(w, map[string]any{"value": []any{nic}})
			return
		}
		if strings.EqualFold(r.URL.Path, nic["id"].(string)) {
			writeJSON(w, nic)
			return
		}
		w.WriteHeader(404)
		writeJSON(w, map[string]any{"error": map[string]string{"code": "ResourceNotFound"}})
	})
	ob, err := p.Observe(context.Background(), allocation())
	if err != nil || !ob.Known || !ob.Exists || len(ob.Resources) != 1 || ob.Resources[0].UID != azureFixtureNICUID {
		t.Fatal(ob, err)
	}
}
func TestAzureSDKPaginatedInventoryAndObservedCleanup(t *testing.T) {
	nic := azureOwnedNIC()
	delete(nic["properties"].(map[string]any), "virtualMachine")
	deleted := false
	deletes, secondPages := 0, 0
	p, _ := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if deploymentPath(r) {
			w.WriteHeader(404)
			writeJSON(w, map[string]any{"error": map[string]string{"code": "DeploymentNotFound"}})
			return
		}
		if r.Method == "DELETE" {
			if !strings.EqualFold(r.URL.Path, nic["id"].(string)) {
				t.Error("unexpected resource deletion", r.URL.Path)
			}
			deletes++
			deleted = true
			w.WriteHeader(204)
			return
		}
		if strings.HasSuffix(strings.ToLower(r.URL.Path), "/resources") {
			if r.URL.Query().Get("page") == "2" {
				secondPages++
				items := []any{}
				if !deleted {
					items = append(items, nic)
				}
				writeJSON(w, map[string]any{"value": items})
				return
			}
			writeJSON(w, map[string]any{"value": []any{}, "nextLink": "https://" + r.Host + r.URL.Path + "?api-version=2021-04-01&page=2"})
			return
		}
		if !deleted && strings.EqualFold(r.URL.Path, nic["id"].(string)) {
			writeJSON(w, nic)
			return
		}
		w.WriteHeader(404)
		writeJSON(w, map[string]any{"error": map[string]string{"code": "ResourceNotFound"}})
	})
	a := allocation()
	if err := p.Delete(context.Background(), a); err == nil || deletes != 0 {
		t.Fatal("uncheckpointed dependency was deleted", deletes, err)
	}
	observed, err := p.Observe(context.Background(), a)
	if err != nil || !observed.Exists || len(observed.Resources) != 1 {
		t.Fatal("residual dependency not observed", observed, err)
	}
	a.ResourceID = observed.ResourceID
	a.Resources = observed.Resources
	if err := p.Delete(context.Background(), a); err != nil || deletes != 1 {
		t.Fatal("cleanup did not delete the checkpointed NIC once", deletes, err)
	}
	observed, err = p.Observe(context.Background(), a)
	if err != nil || !observed.Known || observed.Exists || secondPages < 3 || len(observed.Resources) != 1 || observed.Resources[0] != a.Resources[0] {
		t.Fatal("cleanup lost identity or confirmed absence incorrectly", observed, err)
	}
}
func TestAzureSDKAuthenticationAndTransportFailuresStayUnknown(t *testing.T) {
	for _, mode := range []string{"authentication", "authorization", "invalid-inventory"} {
		t.Run(mode, func(t *testing.T) {
			requests := 0
			p, token := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				if mode == "authorization" {
					w.WriteHeader(403)
					_, _ = io.WriteString(w, `{"error":{"code":"AuthorizationFailed","message":"private-secret"}}`)
					return
				}
				if deploymentPath(r) {
					w.WriteHeader(404)
					writeJSON(w, map[string]any{"error": map[string]string{"code": "DeploymentNotFound"}})
					return
				}
				writeJSON(w, map[string]any{})
			})
			token.fail = mode == "authentication"
			ob, err := p.Observe(context.Background(), allocation())
			if err == nil || ob.Known || strings.Contains(err.Error(), "secret") {
				t.Fatal("failure reported as absence or leaked", ob, err)
			}
			if token.fail && requests != 0 {
				t.Fatal("unauthenticated request sent")
			}
		})
	}
}
func TestAzureSDKCreateTimeoutRetainsUnknownCommitment(t *testing.T) {
	p, _ := azureCreationFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if !deploymentPath(r) {
			t.Error("tagging before deployment completion")
		}
		w.Header().Set("Azure-AsyncOperation", "https://"+r.Host+r.URL.Path+"?api-version=2021-04-01")
		w.WriteHeader(201)
		writeJSON(w, map[string]any{"properties": map[string]string{"provisioningState": "Running"}})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	a := allocation()
	a.Offering.Image = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/images/test"
	id, err := p.Create(ctx, a)
	if err == nil || id != "" || !strings.Contains(err.Error(), "commitment unknown") {
		t.Fatal(id, err)
	}
}
