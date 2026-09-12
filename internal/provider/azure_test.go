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
func deploymentPath(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.URL.Path), "/microsoft.resources/deployments/")
}
func ownedResource(kind, name, owner string) map[string]any {
	return map[string]any{"id": "/subscriptions/sub/resourceGroups/rg/providers/" + kind + "/" + name, "name": name, "type": kind, "tags": map[string]string{"runnerscout-owner": owner, "runnerscout-operation": "rs-test"}}
}

func TestAzureCreateUsesSecureBootstrapAndSpotDelete(t *testing.T) {
	var requests []string
	p, token := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
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
			writeJSON(w, map[string]any{"properties": map[string]string{"provisioningState": "Succeeded"}})
		} else {
			if r.Method != "PATCH" || !strings.HasSuffix(r.URL.Path, "/disks/rs-test-os") {
				t.Error("unexpected disk update", r.Method, r.URL.Path)
			}
			if body["tags"].(map[string]any)["runnerscout-owner"] != "test" {
				t.Error("disk owner missing")
			}
			writeJSON(w, body)
		}
	})
	a := allocation()
	a.Offering.Image = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/images/test"
	id, err := p.Create(context.Background(), a)
	if err != nil || !strings.HasSuffix(id, "/virtualMachines/rs-test") || len(requests) != 2 || token.calls.Load() == 0 {
		t.Fatalf("create: id=%q err=%v requests=%v", id, err, requests)
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
	p, _ := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if deploymentPath(r) {
			w.WriteHeader(404)
			writeJSON(w, map[string]any{"error": map[string]string{"code": "DeploymentNotFound"}})
			return
		}
		writeJSON(w, map[string]any{"value": []any{ownedResource("Microsoft.Network/networkInterfaces", "rs-test-nic", "test")}})
	})
	ob, err := p.Observe(context.Background(), allocation())
	if err != nil || !ob.Known || !ob.Exists {
		t.Fatal(ob, err)
	}
}
func TestAzureSDKPaginatedInventoryAndObservedCleanup(t *testing.T) {
	deleted := false
	pages := 0
	p, _ := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if deploymentPath(r) {
			w.WriteHeader(404)
			writeJSON(w, map[string]any{"error": map[string]string{"code": "DeploymentNotFound"}})
			return
		}
		if r.Method == "DELETE" {
			deleted = true
			w.WriteHeader(204)
			return
		}
		pages++
		if r.URL.Query().Get("page") == "2" {
			writeJSON(w, map[string]any{"value": []any{ownedResource("Microsoft.Network/networkInterfaces", "rs-test-nic", "test")}})
			return
		}
		if deleted {
			writeJSON(w, map[string]any{"value": []any{}})
			return
		}
		writeJSON(w, map[string]any{"value": []any{}, "nextLink": "https://" + r.Host + r.URL.Path + "?api-version=2021-04-01&page=2"})
	})
	if err := p.Delete(context.Background(), allocation()); err != nil {
		t.Fatal(err)
	}
	if !deleted || pages != 4 {
		t.Fatal("did not observe all pages before cleanup", deleted, pages)
	}
	ob, err := p.Observe(context.Background(), allocation())
	if err != nil || !ob.Known || ob.Exists {
		t.Fatal(ob, err)
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
	p, _ := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
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
