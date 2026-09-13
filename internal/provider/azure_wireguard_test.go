package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/wireguard"
)

// azureWireGuardFixture mirrors TestAzureCreateUsesSecureBootstrapAndSpotDelete's
// handler: an empty resource group that accepts one deployment, then a disk
// that starts untagged and gets tagged by finishAzureDiskOwnership. The NIC
// GET/list responses are azureCreationFixture's own hardcoded azureOwnedNIC(),
// which already carries ipConfigurations[0].properties.privateIPAddress.
func azureWireGuardFixture(t *testing.T) *Command {
	t.Helper()
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
		if deploymentPath(r) {
			writeJSON(w, map[string]any{"properties": map[string]string{"provisioningState": "Succeeded"}})
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		disk["tags"] = body["tags"]
		writeJSON(w, disk)
	})
	return p
}

// TestAzureCreationCapturesPrivateIPAsWireGuardEndpoint proves the outer-
// endpoint gap resolution for Azure (lifecycle.Allocation.WireGuardEndpoint's
// doc comment): the NIC's private IP already present in the exact network
// interface GET response azureInventory already performs for ownership/UID
// verification is captured onto Creation.WireGuardEndpoint as
// "ip:internal/wireguard.DefaultListenPort", with no new API call.
func TestAzureCreationCapturesPrivateIPAsWireGuardEndpoint(t *testing.T) {
	p := azureWireGuardFixture(t)
	p.NetworkPeers = func(context.Context, lifecycle.Allocation) ([]wireguard.Peer, error) { return nil, nil }
	a := allocation()
	a.Offering.Image = azureFixtureImage
	a.NetworkProfile = "profile-a"
	a.WireGuardOverlayAddress = "10.60.0.7"
	receipt, err := p.CreateWithResources(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	want := azureFixtureNICPrivateIP + ":51820"
	if receipt.WireGuardEndpoint != want {
		t.Fatalf("wireguard endpoint: got %q want %q", receipt.WireGuardEndpoint, want)
	}
}

// TestAzureCreationWithoutWireGuardIntentNeverCapturesAnEndpoint is this
// field's "zero effect until wired" regression guard, mirroring the AWS
// sibling in aws_lifecycle_test.go: createAzure always has the NIC's private
// IP available in its own inventory response, but CreateWithResources must
// not surface it onto Creation for an allocation that never asked for
// wireguard mode.
func TestAzureCreationWithoutWireGuardIntentNeverCapturesAnEndpoint(t *testing.T) {
	p := azureWireGuardFixture(t)
	a := allocation()
	a.Offering.Image = azureFixtureImage
	if a.NetworkProfile != "" {
		t.Fatal("test fixture unexpectedly set NetworkProfile")
	}
	receipt, err := p.CreateWithResources(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.WireGuardEndpoint != "" {
		t.Fatalf("wireguard endpoint captured with no NetworkProfile intent: %q", receipt.WireGuardEndpoint)
	}
}
