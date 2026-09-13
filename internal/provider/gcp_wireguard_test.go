package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/wireguard"
)

// gcpWireGuardFixture mirrors gcpFixture's create-then-inventory shape: the
// pre-create existence check (gcpInventory, inside createGCP) always sees a
// missing instance/disk, exactly like every other GCP create fixture; once
// the create POST lands, a later Instances.Get for the same name returns
// instanceAfterCreate (or, if nil, a plain gcpMissing 404, to prove no such
// Get was ever attempted). Disks.Get always reports missing, since none of
// these tests exercise disk ownership.
func gcpWireGuardFixture(t *testing.T, instanceGets *int, instanceAfterCreate func() map[string]any) *Command {
	t.Helper()
	created := false
	var p *Command
	p = gcpFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			if strings.Contains(r.URL.Path, "/disks/") {
				gcpMissing(w)
				return
			}
			if !strings.Contains(r.URL.Path, "/instances/") {
				t.Fatalf("unexpected GET %s", r.URL.Path)
			}
			*instanceGets++
			if !created || instanceAfterCreate == nil {
				gcpMissing(w)
				return
			}
			writeJSON(w, instanceAfterCreate())
			return
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/instances") {
			created = true
			writeJSON(w, gcpOperation(p, "create", "instances", "rs-test", "DONE"))
			return
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	})
	return p
}

// TestGCPCreationCapturesPrivateIPAsWireGuardEndpoint proves the outer-
// endpoint gap resolution for GCP (lifecycle.Allocation.WireGuardEndpoint's
// doc comment): unlike AWS/Azure, GCP's create path never receives a
// compute.Instance response (only compute.Operation, which carries no
// NetworkInterfaces), so this requires one genuine extra post-create
// Instances.Get - made only because a.NetworkProfile != "" - whose
// NetworkInterfaces[0].NetworkIP is captured the same way AWS/Azure capture
// their own already-available private IP.
func TestGCPCreationCapturesPrivateIPAsWireGuardEndpoint(t *testing.T) {
	instanceGets := 0
	p := gcpWireGuardFixture(t, &instanceGets, func() map[string]any {
		vm := gcpOwned("instances")
		vm["networkInterfaces"] = []any{map[string]any{"networkIP": "10.20.0.5"}}
		return vm
	})
	p.NetworkPeers = func(context.Context, lifecycle.Allocation) ([]wireguard.Peer, error) { return nil, nil }
	a := gcpAllocation()
	a.NetworkProfile = "profile-a"
	a.WireGuardOverlayAddress = "10.60.0.7"
	receipt, err := p.CreateWithResources(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	want := "10.20.0.5:51820"
	if receipt.WireGuardEndpoint != want {
		t.Fatalf("wireguard endpoint: got %q want %q", receipt.WireGuardEndpoint, want)
	}
	// One instance GET for the pre-create occupancy check, one more for the
	// post-create capture - never more, never fewer.
	if instanceGets != 2 {
		t.Fatalf("expected exactly one pre-create and one post-create instance GET, got %d", instanceGets)
	}
}

// TestGCPCreationWithoutWireGuardIntentNeverCapturesAnEndpoint is this
// field's "zero effect until wired" regression guard, mirroring the AWS/
// Azure siblings: an allocation that never asked for wireguard mode must
// gain neither a captured endpoint nor even the extra Instances.Get call
// that would capture it - the whole point of gating the new call on
// a.NetworkProfile != "" is that it costs literally nothing for every
// allocation this codebase's configuration path can produce today.
func TestGCPCreationWithoutWireGuardIntentNeverCapturesAnEndpoint(t *testing.T) {
	instanceGets := 0
	p := gcpWireGuardFixture(t, &instanceGets, func() map[string]any {
		vm := gcpOwned("instances")
		vm["networkInterfaces"] = []any{map[string]any{"networkIP": "10.20.0.5"}}
		return vm
	})
	a := gcpAllocation()
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
	if instanceGets != 1 {
		t.Fatalf("post-create Instances.Get issued without NetworkProfile intent: %d instance GETs", instanceGets)
	}
}

// TestGCPCreationEndpointCaptureFailureDoesNotFailCreation proves the
// best-effort posture this task requires: a failed or incomplete post-create
// Get must never fail the overall creation - it leaves WireGuardEndpoint
// simply empty, matching how AWS/Azure already tolerate their own equivalent
// edge cases (an unexpected NetworkInterfaces count) without discarding the
// creation receipt.
func TestGCPCreationEndpointCaptureFailureDoesNotFailCreation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response func() map[string]any
	}{
		{"get-fails", nil},
		{"no-network-interfaces", func() map[string]any {
			vm := gcpOwned("instances")
			vm["networkInterfaces"] = []any{}
			return vm
		}},
		{"two-network-interfaces", func() map[string]any {
			vm := gcpOwned("instances")
			vm["networkInterfaces"] = []any{map[string]any{"networkIP": "10.20.0.5"}, map[string]any{"networkIP": "10.20.0.6"}}
			return vm
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instanceGets := 0
			created := false
			var p *Command
			p = gcpFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					if strings.Contains(r.URL.Path, "/disks/") {
						gcpMissing(w)
						return
					}
					instanceGets++
					if !created {
						gcpMissing(w)
						return
					}
					if tc.response == nil {
						w.WriteHeader(500)
						writeJSON(w, map[string]any{"error": map[string]any{"code": 500, "message": "secret diagnostic"}})
						return
					}
					writeJSON(w, tc.response())
					return
				}
				if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/instances") {
					created = true
					writeJSON(w, gcpOperation(p, "create", "instances", "rs-test", "DONE"))
					return
				}
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
			})
			p.NetworkPeers = func(context.Context, lifecycle.Allocation) ([]wireguard.Peer, error) { return nil, nil }
			a := gcpAllocation()
			a.NetworkProfile = "profile-a"
			a.WireGuardOverlayAddress = "10.60.0.7"
			receipt, err := p.CreateWithResources(context.Background(), a)
			if err != nil {
				t.Fatal("capture failure must not fail creation", err)
			}
			if receipt.ResourceID != a.ID {
				t.Fatalf("creation resource id lost on capture failure: %q", receipt.ResourceID)
			}
			if receipt.WireGuardEndpoint != "" {
				t.Fatalf("wireguard endpoint captured from a failed/incomplete Get: %q", receipt.WireGuardEndpoint)
			}
		})
	}
}
