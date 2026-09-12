package provider

import (
	"context"
	"encoding/json"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"os"
	"strings"
	"testing"
)

func azureConfig() Config {
	return Config{Kind: "azure", Owner: "test", Subscription: "sub", ResourceGroup: "rg", Subnet: "/subnet", SecurityGroup: "/nsg", SSHPublicKey: "ssh-ed25519 test"}
}
func TestAzureCreateUsesSecureBootstrapAndSpotDelete(t *testing.T) {
	f := &fakeExec{responses: [][]byte{[]byte(`{}`), []byte(`{}`)}}
	checked := false
	f.check = func(name string, args []string) {
		if name != "az" {
			t.Fatal(name)
		}
		for i, arg := range args {
			if strings.Contains(arg, "secret-jit") {
				t.Fatal("credential in CLI arguments")
			}
			if arg == "--template-file" {
				b, e := os.ReadFile(args[i+1])
				if e != nil {
					t.Fatal(e)
				}
				var tpl map[string]any
				if e = json.Unmarshal(b, &tpl); e != nil {
					t.Fatal(e)
				}
				params := tpl["parameters"].(map[string]any)
				if params["bootstrap"].(map[string]any)["type"] != "securestring" {
					t.Fatal(params)
				}
				vm := tpl["resources"].([]any)[1].(map[string]any)
				props := vm["properties"].(map[string]any)
				if props["priority"] != "Spot" || props["evictionPolicy"] != "Delete" {
					t.Fatal(props)
				}
				checked = true
			}
		}
	}
	p := Command{Config: azureConfig(), Exec: f, Bootstrap: func(context.Context, string) (string, error) { return "secret-jit", nil }}
	a := allocation()
	a.Offering.Image = "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/images/ubuntu-pinned"
	a.Requirements.MaxPriceMicros = 120000
	id, e := p.Create(context.Background(), a)
	if e != nil || !checked || !strings.Contains(id, "virtualMachines/rs-test") {
		t.Fatal(id, e)
	}
	if len(f.calls) != 2 {
		t.Fatal(f.calls)
	}
}
func TestAzureActiveDeploymentCannotProveAbsence(t *testing.T) {
	f := &fakeExec{responses: [][]byte{[]byte(`[{"properties":{"provisioningState":"Running"}}]`)}}
	p := Command{Config: azureConfig(), Exec: f}
	ob, e := p.Observe(context.Background(), allocation())
	if e == nil || ob.Known {
		t.Fatal(ob, e)
	}
}
func TestAzureForeignDiskCannotBeDeleted(t *testing.T) {
	f := &fakeExec{responses: [][]byte{[]byte(`[]`), []byte(`[{"id":"/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/disks/rs-test-os","name":"rs-test-os","type":"Microsoft.Compute/disks","tags":{"runnerscout-owner":"foreign"}}]`)}}
	p := Command{Config: azureConfig(), Exec: f}
	if e := p.Delete(context.Background(), allocation()); e == nil {
		t.Fatal("foreign disk deletion allowed")
	}
	if len(f.calls) != 2 {
		t.Fatal(f.calls)
	}
}
func TestAzureResidualOwnedNICRetainsCleanup(t *testing.T) {
	f := &fakeExec{responses: [][]byte{[]byte(`[]`), []byte(`[{"id":"/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/networkInterfaces/rs-test-nic","name":"rs-test-nic","type":"Microsoft.Network/networkInterfaces","tags":{"runnerscout-owner":"test","runnerscout-operation":"rs-test"}}]`)}}
	p := Command{Config: azureConfig(), Exec: f}
	ob, e := p.Observe(context.Background(), lifecycle.Allocation{ID: "rs-test"})
	if e != nil || !ob.Known || !ob.Exists {
		t.Fatal(ob, e)
	}
}
