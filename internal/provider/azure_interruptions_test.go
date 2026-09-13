package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// azureAbsentFixture builds a Command whose deployment is reported gone
// (DeploymentNotFound - terminal per azureTerminal) and whose resource group
// lists no resources at all, so observeAzure/Command.Observe report Known
// with Exists false for any allocation ID - the "VM absent" precondition
// Interrupted is only ever meaningful under (Observation.Interrupted "carries
// no meaning when Exists is true", lifecycle.go's Observation doc comment).
func azureAbsentFixture(t *testing.T) *Command {
	t.Helper()
	p, _ := sdkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if deploymentPath(r) {
			w.WriteHeader(404)
			writeJSON(w, map[string]any{"error": map[string]string{"code": "DeploymentNotFound"}})
			return
		}
		if strings.HasSuffix(strings.ToLower(r.URL.Path), "/resources") {
			writeJSON(w, map[string]any{"value": []any{}})
			return
		}
		w.WriteHeader(404)
		writeJSON(w, map[string]any{"error": map[string]string{"code": "ResourceNotFound"}})
	})
	return p
}

func TestObserveAzureReportsConfirmedInterruptionOnExactResourceIDMatch(t *testing.T) {
	p := azureAbsentFixture(t)
	a := allocation() // Offering.Spot: true, ID "rs-test"
	p.AzureInterrupted = map[string]bool{p.azureID("Microsoft.Compute/virtualMachines", a.ID): true}
	ob, err := p.Observe(context.Background(), a)
	if err != nil || !ob.Known || ob.Exists || !ob.Interrupted {
		t.Fatalf("expected confirmed interruption on exact resource ID match: %+v %v", ob, err)
	}
}

func TestObserveAzureIgnoresNonMatchingResourceID(t *testing.T) {
	p := azureAbsentFixture(t)
	a := allocation()
	// A confirmed preemption for a different VM's resource ID must never
	// leak onto this allocation - correlation is exact resource ID equality,
	// not "any preemption seen this Tick".
	p.AzureInterrupted = map[string]bool{p.azureID("Microsoft.Compute/virtualMachines", "rs-other"): true}
	ob, err := p.Observe(context.Background(), a)
	if err != nil || !ob.Known || ob.Exists || ob.Interrupted {
		t.Fatalf("non-matching resource ID must never confirm interruption: %+v %v", ob, err)
	}
}

func TestObserveAzureNilAzureInterruptedIsInert(t *testing.T) {
	p := azureAbsentFixture(t)
	a := allocation()
	// AzureInterrupted left at its zero value (nil) - the default for every
	// Command until an operator explicitly opts into Azure interruption
	// delivery - must never report a confirmed interruption.
	ob, err := p.Observe(context.Background(), a)
	if err != nil || !ob.Known || ob.Exists || ob.Interrupted {
		t.Fatalf("nil AzureInterrupted must be a complete no-op: %+v %v", ob, err)
	}
}

func TestObserveAzureNeverReportsInterruptedForNonSpotOffering(t *testing.T) {
	p := azureAbsentFixture(t)
	a := allocation()
	a.Offering.Spot = false
	p.AzureInterrupted = map[string]bool{p.azureID("Microsoft.Compute/virtualMachines", a.ID): true}
	ob, err := p.Observe(context.Background(), a)
	if err != nil || !ob.Known || ob.Exists || ob.Interrupted {
		t.Fatalf("only spot offerings can ever be confirmed interrupted: %+v %v", ob, err)
	}
}

func TestObserveAzureNeverReportsInterruptedWhenResourceStillExists(t *testing.T) {
	// A message classified Preempted:true is only meaningful once the VM's
	// own absence is independently confirmed - Observation.Interrupted
	// "carries no meaning when Exists is true", and this codebase never
	// infers interruption from a bare disappearance let alone from presence.
	p, _ := azureInventoryFixture(t)
	a := allocation()
	p.AzureInterrupted = map[string]bool{p.azureID("Microsoft.Compute/virtualMachines", a.ID): true}
	ob, err := p.Observe(context.Background(), a)
	if err != nil || !ob.Known || !ob.Exists || ob.Interrupted {
		t.Fatalf("an existing VM must never be reported interrupted: %+v %v", ob, err)
	}
}

// TestAzureSDKInterruptionQueueReusesResolvedCredential proves
// AzureSDK.InterruptionQueue builds a real *azurequeue.Client using the
// SDK's own already-resolved credential chain, never a distinct one - see
// InterruptionQueue's doc comment for why.
func TestAzureSDKInterruptionQueueReusesResolvedCredential(t *testing.T) {
	token := &testAzureToken{t: t}
	sdk := &AzureSDK{Credential: token}
	client, err := sdk.InterruptionQueue("https://fixture.queue.core.windows.net/interruptions")
	if err != nil || client == nil || client.Queue == nil {
		t.Fatalf("expected a constructed azurequeue.Client: %v %v", client, err)
	}
	cred, credErr := sdk.credentials()
	if credErr != nil || cred != token {
		t.Fatalf("InterruptionQueue must reuse AzureSDK's own resolved credential: %v %v", cred, credErr)
	}
}
