//go:build emulators

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/google/uuid"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/placement"
)

// This file drives the REAL internal/provider Azure adapter (createAzure,
// observeAzure, deleteAzure, finishAzureDiskOwnership) against a local
// floci-az instance, the same way emulator_test.go drives the real AWS
// adapter against Moto/Ministack. It is the Azure counterpart to the
// tools/emulators.py Python REST smoke test, which only hits floci-az's
// REST API with raw urllib and never calls this package's own adapter code.
//
// floci-az's real, experimentally-confirmed coverage is narrower than the
// disk-only gap this file was originally scoped around. Probing a live
// floci-az container (see work/emulator-research/floci-az.md for how it was
// started) with the exact ARM requests this adapter issues found FOUR gaps,
// not one:
//
//  1. Microsoft.Resources/deployments - the ARM deployment resource type
//     createAzure exclusively uses to provision a VM+NIC (deploy() in
//     azure_sdk.go, via armdeployments.BeginCreateOrUpdate) - is not
//     implemented at all. A PUT to that path 404s with "Resource not
//     found" regardless of api-version or path casing tried. This is
//     confirmed by curl against a running floci-az container, not inferred
//     from its README.
//  2. Microsoft.Compute/images - the managed-image resource type
//     validateAzureImage() reads before any deployment is attempted - is
//     also unimplemented ("Unsupported Microsoft.Compute path: images/...").
//     Since validateAzureImage runs before deploy(), createAzure's public
//     entry point (CreateWithResources) fails there first.
//  3. Microsoft.Compute/disks is unimplemented, exactly as the prior
//     research identified: GET on a disk path 404s with "Unsupported
//     Microsoft.Compute path: disks/...". finishAzureDiskOwnership's first
//     step, azureDiskBinding, GETs the disk and fails immediately.
//  4. Microsoft.Network/networkInterfaces IS creatable/gettable/deletable
//     directly (PUT/GET/DELETE all work), but floci-az never populates the
//     NIC's properties.resourceGuid field. azureInventory (azure_inventory.go)
//     requires that field to derive a NIC's durable UID and treats its
//     absence as a hard identity failure ("Azure resource generation
//     invalid"), not a soft "not found". So any code path that lists an
//     *existing* NIC through azureInventory - observeAzure, deleteAzero,
//     reconcileAzureCreation - cannot complete successfully against a real
//     floci-az NIC, independent of the disk gap.
//
// Microsoft.Compute/virtualMachines can be created/read/deleted directly by
// full ARM resource ID, and its VM identity (properties.vmId) is a real
// generated UUID, so the VM half of the identity model works. floci-az's
// generic "list resources in a resource group" endpoint additionally never
// returns virtualMachines entries at all, but that does not by itself break
// azureInventory, which authenticates each resource by a direct GET against
// its known path and only uses the list response as an extra cross-check.
//
// Given (1)-(3), createAzure's only real provisioning mechanism (an ARM
// template deployment) cannot be submitted to floci-az at all, so this file
// cannot drive resource creation through the public CreateWithResources
// path and then observe a fully-formed VM+NIC+disk the way the AWS emulator
// test observes a real EC2 instance. What it qualifies instead, all through
// the unmodified adapter code:
//
//   - TestFlociAzureUnsupportedImageRetainsObligation: the public
//     CreateWithResources entry point, run against a real floci-az resource
//     group, fails safely at image validation (gap 2) - no receipt, no
//     ErrCapacity/ErrNoEffect misclassification - and a subsequent Observe
//     correctly proves nothing was created (Known && !Exists), exactly the
//     "ambiguous create never lies" contract the Ministack AWS test
//     qualifies for an unsupported image.
//   - TestFlociAzureDiskBindingAndNICIdentityUnsupported: a VM+NIC pair is
//     provisioned directly by ARM resource PUT (the same resource shapes,
//     names and tags createAzure's template would have produced, since the
//     template mechanism itself cannot be submitted - see gap 1), then the
//     real finishAzureDiskOwnership, Observe and Delete are called against
//     that live floci-az state. It confirms finishAzureDiskOwnership stops
//     exactly at the disk GET (gap 3) with a deterministic VM receipt and a
//     retained (non-nil) error, and that Observe/Delete both fail safe
//     (return an error, never a false Exists/absence) once floci-az's
//     missing NIC resourceGuid (gap 4) makes inventory identity
//     unconfirmable - never silently drop or fabricate identity.
//
// Both tests were run against a real floci-az container pulled from the
// exact image/digest tools/emulators.py pins; see the accompanying report
// for command transcripts. Nothing here is mocked: every HTTP response the
// adapter sees comes from a live floci-az process.

const flociAzureSubscription = "00000000-0000-0000-0000-000000000001"

// flociAzureToken is a static credential: floci-az's dev auth mode accepts
// any bearer token without validation, so no real Entra token exchange is
// needed or possible against it.
type flociAzureToken struct{}

func (flociAzureToken) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "floci-az-dev-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

// flociAzureAdmin issues raw ARM REST calls to floci-az to build fixtures
// (resource group, VNet, subnet, NSG, and - since floci-az cannot execute an
// ARM deployment at all - the VM and NIC a real deployment would have
// produced) and to tear them down. It never uses this package's own Command
// or AzureSDK: those are reserved for the code actually under test.
type flociAzureAdmin struct {
	t      *testing.T
	client *http.Client
	base   string
}

func (a flociAzureAdmin) request(method, path string, body any) map[string]any {
	a.t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, a.base+path, reader)
	if err != nil {
		a.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer emulator-only")
	response, err := a.client.Do(request)
	if err != nil {
		a.t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		a.t.Fatal(err)
	}
	if response.StatusCode >= 300 {
		a.t.Fatalf("floci-az fixture request failed: %s %s -> %d %s", method, path, response.StatusCode, string(data))
	}
	if len(data) == 0 {
		return nil
	}
	result := map[string]any{}
	if err := json.Unmarshal(data, &result); err != nil {
		a.t.Fatal(err)
	}
	return result
}

// flociAzureFixture validates the isolated-emulator environment (mirroring
// qualifyAWSEmulator's own network/endpoint checks), provisions a resource
// group with a VNet, subnet and NSG through floci-az's real ARM Network
// support, and builds a *Command whose AzureSDK talks to that same live
// floci-az instance - never an httptest double.
func flociAzureFixture(t *testing.T) (p *Command, admin flociAzureAdmin, resourceGroup string) {
	t.Helper()
	network, endpoint := os.Getenv("RUNNERSCOUT_EMULATOR_NETWORK"), os.Getenv("RUNNERSCOUT_AZURE_ENDPOINT")
	target, err := url.Parse(endpoint)
	if err != nil || !strings.HasPrefix(network, "runnerscout-emulators-") || target.Scheme != "http" || target.Port() != "4577" || !net.ParseIP(target.Hostname()).IsPrivate() {
		t.Fatal("explicit isolated emulator network and private Azure endpoint required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	admin = flociAzureAdmin{t: t, client: client, base: endpoint}

	resourceGroup = "rs-" + uuid.NewString()
	admin.request("PUT", "/subscriptions/"+flociAzureSubscription+"/resourceGroups/"+resourceGroup+"?api-version=2021-04-01", map[string]any{"location": "eastus"})
	t.Cleanup(func() {
		req, err := http.NewRequest("DELETE", endpoint+"/subscriptions/"+flociAzureSubscription+"/resourceGroups/"+resourceGroup+"?api-version=2021-04-01", nil)
		if err != nil {
			t.Error(err)
			return
		}
		req.Header.Set("Authorization", "Bearer emulator-only")
		resp, err := client.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		_ = resp.Body.Close()
	})
	vnetID := "/subscriptions/" + flociAzureSubscription + "/resourceGroups/" + resourceGroup + "/providers/Microsoft.Network/virtualNetworks/rs-vnet"
	admin.request("PUT", vnetID+"?api-version=2024-05-01", map[string]any{"location": "eastus", "properties": map[string]any{"addressSpace": map[string]any{"addressPrefixes": []string{"10.10.0.0/16"}}}})
	subnetID := vnetID + "/subnets/rs-subnet"
	admin.request("PUT", subnetID+"?api-version=2024-05-01", map[string]any{"properties": map[string]any{"addressPrefix": "10.10.1.0/24"}})
	nsgID := "/subscriptions/" + flociAzureSubscription + "/resourceGroups/" + resourceGroup + "/providers/Microsoft.Network/networkSecurityGroups/rs-nsg"
	admin.request("PUT", nsgID+"?api-version=2024-05-01", map[string]any{"location": "eastus"})

	options := &arm.ClientOptions{ClientOptions: policy.ClientOptions{
		Transport: client,
		Retry:     policy.RetryOptions{MaxRetries: -1},
		// floci-az serves plain HTTP (no TLS). azcore refuses to attach a
		// bearer token to a non-TLS endpoint unless explicitly overridden;
		// this is safe here because the token is a canned fixture value
		// (flociAzureToken), never a real credential.
		InsecureAllowCredentialWithHTTP: true,
		Cloud: cloud.Configuration{
			ActiveDirectoryAuthorityHost: cloud.AzurePublic.ActiveDirectoryAuthorityHost,
			Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
				cloud.ResourceManager: {Audience: "https://management.azure.com", Endpoint: endpoint},
			},
		},
	}}
	config := Config{Kind: "azure", Owner: "test", Subscription: flociAzureSubscription, ResourceGroup: resourceGroup, Subnet: subnetID, SecurityGroup: nsgID, SSHPublicKey: "ssh-ed25519 test"}
	p = &Command{Config: config, Azure: &AzureSDK{Credential: flociAzureToken{}, Options: options}, Bootstrap: func(context.Context, string) (string, error) { return "emulator-fixture-not-a-github-credential", nil }}
	return p, admin, resourceGroup
}

func flociAzureAllocation(id string) lifecycle.Allocation {
	return lifecycle.Allocation{ID: id, Offering: placement.Offering{Region: "eastus", Zone: "1", Architecture: "amd64", Machine: "Standard_D2s_v5", Image: "", Spot: true}}
}

// TestFlociAzureUnsupportedImageRetainsObligation drives the real public
// CreateWithResources entry point. floci-az does not implement
// Microsoft.Compute/images (gap 2 above), so validateAzureImage - the first
// Azure-specific step createAzure runs - cannot confirm the image and
// createAzure must refuse to proceed. This mirrors
// TestMinistackAWSUnsupportedImageRetainsObligation: an emulator that cannot
// fulfill the request must still leave the adapter's contract intact - no
// false receipt, no capacity/no-effect misclassification - and a subsequent
// Observe must independently prove nothing was created.
func TestFlociAzureUnsupportedImageRetainsObligation(t *testing.T) {
	p, _, resourceGroup := flociAzureFixture(t)
	a := flociAzureAllocation("rs-" + uuid.NewString()[:8])
	a.Offering.Image = "/subscriptions/" + flociAzureSubscription + "/resourceGroups/" + resourceGroup + "/providers/Microsoft.Compute/images/rs-unsupported-image"

	receipt, err := p.CreateWithResources(context.Background(), a)
	if err == nil || receipt.ResourceID != "" || len(receipt.Resources) != 0 || errors.Is(err, lifecycle.ErrCapacity) || errors.Is(err, lifecycle.ErrNoEffect) {
		t.Fatal("unsupported image falsely confirmed or misclassified", receipt, err)
	}
	// A failed create remains uncertain until subsequent inventory proves
	// absence - nothing was actually submitted to floci-az, so Observe must
	// independently confirm that.
	ob, err := p.Observe(context.Background(), a)
	if err != nil || !ob.Known || ob.Exists {
		t.Fatal("unsupported create did not reconcile to absence", ob, err)
	}
}

// TestFlociAzureDiskBindingAndNICIdentityUnsupported provisions the VM and
// NIC a real ARM deployment would have produced directly by resource PUT
// (floci-az cannot execute the deployment itself - gap 1), then exercises
// the real finishAzureDiskOwnership, Observe and Delete against that live
// state to pin down exactly where floci-az's coverage ends.
func TestFlociAzureDiskBindingAndNICIdentityUnsupported(t *testing.T) {
	p, admin, resourceGroup := flociAzureFixture(t)
	a := flociAzureAllocation("rs-" + uuid.NewString()[:8])
	tags := map[string]any{"runnerscout-owner": p.Config.Owner, "runnerscout-operation": a.ID}

	nicID := p.azureID("Microsoft.Network/networkInterfaces", a.ID+"-nic")
	admin.request("PUT", nicID+"?api-version=2024-05-01", map[string]any{
		"location": "eastus", "tags": tags,
		"properties": map[string]any{
			"networkSecurityGroup": map[string]string{"id": p.Config.SecurityGroup},
			"ipConfigurations":     []any{map[string]any{"name": "private", "properties": map[string]any{"privateIPAllocationMethod": "Dynamic", "subnet": map[string]string{"id": p.Config.Subnet}}}},
		},
	})
	t.Cleanup(func() {
		req, err := http.NewRequest("DELETE", admin.base+nicID+"?api-version=2024-05-01", nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer emulator-only")
			if resp, err := admin.client.Do(req); err == nil {
				_ = resp.Body.Close()
			}
		}
	})
	diskID := p.azureID("Microsoft.Compute/disks", a.ID+"-os")
	vmID := p.azureID("Microsoft.Compute/virtualMachines", a.ID)
	admin.request("PUT", vmID+"?api-version=2024-07-01", map[string]any{
		"location": "eastus", "tags": tags,
		"properties": map[string]any{
			"hardwareProfile": map[string]string{"vmSize": a.Offering.Machine},
			// Real Azure always populates storageProfile.osDisk.managedDisk.id
			// on the VM response once a deployment provisions the disk. Since
			// floci-az cannot run that deployment (gap 1) or the disk resource
			// itself (gap 3), this reconstructs what that response would have
			// looked like so azureVMDeletionBindings' identity check below is
			// exercised faithfully rather than trivially skipped.
			"storageProfile": map[string]any{"imageReference": map[string]any{"id": "/subscriptions/" + flociAzureSubscription + "/resourceGroups/" + resourceGroup + "/providers/Microsoft.Compute/images/rs-image"}, "osDisk": map[string]any{"name": a.ID + "-os", "createOption": "FromImage", "managedDisk": map[string]string{"id": diskID}}},
			"osProfile":      map[string]any{"computerName": a.ID, "adminUsername": "runner"},
			"networkProfile": map[string]any{"networkInterfaces": []any{map[string]any{"id": nicID, "properties": map[string]any{"primary": true, "deleteOption": "Delete"}}}},
		},
	})
	t.Cleanup(func() {
		req, err := http.NewRequest("DELETE", admin.base+vmID+"?api-version=2024-07-01", nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer emulator-only")
			if resp, err := admin.client.Do(req); err == nil {
				_ = resp.Body.Close()
			}
		}
	})

	// Gap 3: the disk-ownership-binding step GETs Microsoft.Compute/disks,
	// which floci-az does not implement at all. finishAzureDiskOwnership
	// must still return the deterministic VM receipt lifecycle depends on to
	// retain the creation obligation, alongside a non-nil error.
	receipt, err := p.finishAzureDiskOwnership(context.Background(), a)
	if err == nil || receipt.ResourceID != vmID || len(receipt.Resources) != 0 {
		t.Fatal("disk binding boundary not honored", receipt, err)
	}

	// Gap 4: even setting the disk aside, floci-az's NIC objects never carry
	// a resourceGuid, so azureInventory cannot derive a durable NIC UID for a
	// NIC that genuinely exists. Observe must fail safe (never claim a false
	// Exists or a false absence) rather than silently drop or fabricate an
	// identity.
	if ob, err := p.Observe(context.Background(), a); err == nil {
		t.Fatal("Observe falsely confirmed identity floci-az cannot supply", ob)
	}

	// Delete must refuse for the identical reason - it shares azureInventory
	// with Observe - rather than deleting real resources on unconfirmed
	// inventory.
	if err := p.Delete(context.Background(), a); err == nil {
		t.Fatal("Delete proceeded on unconfirmed Azure inventory")
	}

	// The VM and NIC nonetheless remain independently verifiable by direct
	// ARM GET, proving they were genuinely created against floci-az and that
	// the failures above are the adapter correctly refusing to over-claim,
	// not floci-az silently failing to create anything at all.
	vm := admin.request("GET", vmID+"?api-version=2024-07-01", nil)
	if vm["name"] != a.ID {
		t.Fatal("fixture VM not actually present in floci-az", vm)
	}
	nic := admin.request("GET", nicID+"?api-version=2024-05-01", nil)
	if nic["name"] != a.ID+"-nic" {
		t.Fatal("fixture NIC not actually present in floci-az", nic)
	}
}
