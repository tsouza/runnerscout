//go:build realcloud

// Real-GCP provider qualification (issue #3's remaining "Final real-cloud
// qualification" scope, GCP piece): "Verify ordinary runs-on, actual VM job
// execution, interruption/restart and independent cleanup inventory for each
// provider. Local emulator results do not discharge the real-VM
// obligations."
//
// This file drives the production internal/provider GCP adapter (gcp_sdk.go,
// gcp_credentials.go - neither of which this file modifies) against real GCP
// infrastructure: it creates ONE real, billed Compute Engine Spot instance
// (plus its boot disk), confirms it independently reaches Compute Engine's
// own "RUNNING" status, deletes it, and independently re-queries the GCP API
// - through a second, separately-constructed compute.Service (see
// newIndependentGCPComputeService), never just trusting the adapter's own
// Observe()/Delete() - to confirm zero leftover billable resources (instance,
// boot disk, any reserved address). This mirrors aws_realcloud_test.go's
// shape and safety reasoning exactly; see that file and
// docs/qualification-real-cloud.md/.background.md for the shared design this
// one deliberately follows rather than reinvents.
//
// Only .github/workflows/qualify-gcp.yml is meant to ever run this: it is
// gated behind the "realcloud" build tag (the same one aws_realcloud_test.go
// uses - both files live in this one package, distinguished only by their
// own package-scope identifiers, all deliberately named with a "GCP"
// distinguisher below so this file can never collide with its AWS sibling),
// workflow_dispatch-only, and this test itself additionally refuses to run
// without an explicit confirmation phrase in its own process environment
// (requireRealCloudConfirmation, shared with aws_realcloud_test.go/
// azure_realcloud_test.go) - a second, independent guard against accidental
// invocation, in case this binary is ever built and run outside that one
// intended workflow.
//
// What this test does NOT and honestly CANNOT qualify:
//
//   - Real GitHub Actions job execution (issue #3's "ordinary runs-on,
//     actual VM job execution"). p.Bootstrap below returns a synthetic,
//     non-functional placeholder in place of a real GitHub JIT registration
//     token - mirroring emulator_test.go's and aws_realcloud_test.go's own
//     established "fixture-not-a-github-credential" precedent - so the
//     image's own startup-script runner-registration step is expected to
//     fail harmlessly inside the guest. This test only confirms the VM boots
//     and reaches a real RUNNING state at the Compute Engine API level,
//     independent of whether the startup script succeeds inside the guest.
//     Proving a real runner actually registers and executes a real workflow
//     job is a separate, larger, not-yet-built qualification piece - see
//     docs/qualification-real-cloud.md's "Known gaps" section.
//   - Real GCP Spot price observation. internal/prices has no GCP client at
//     all today (see docs/prices-gcp.md/prices-gcp.background.md, written for
//     issue #2): Compute Engine's public pricing surface splits Core/RAM
//     SKUs with no structured machine-type field, no zone-level pricing and
//     no request-side filter, so a GCP spot price client was assessed as not
//     honestly buildable without guessing. This test therefore does not
//     attempt any price observation at all - unlike aws_realcloud_test.go,
//     which observes a real EC2 Spot price because internal/prices.AWSSpotClient
//     genuinely exists. This is a named, tracked gap, not a silently skipped
//     step.
//   - A real Spot preemption. GCP provides no supported, on-demand API to
//     force one: the closest tool, `gcloud compute instances
//     simulate-maintenance-event`, exercises host-maintenance/live-migration
//     handling, not confirmed to produce the specific
//     "compute.instances.preempted" zone operation gcpConfirmedPreemption
//     (gcp_sdk.go) looks for - using it here would risk qualifying the wrong
//     signal instead of the real one. This test can only confirm the real
//     adapter correctly reports "not interrupted" against a real, healthy
//     running instance. The gcpConfirmedPreemption detection logic itself is
//     already covered against synthesized operation payloads by the existing
//     unit test suite (see TestGCPObservationConfirmsSpotPreemption and its
//     siblings in gcp_sdk_test.go) - this file does not re-prove that logic,
//     only that it stays wired to a real instance's real state today.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/placement"
	compute "google.golang.org/api/compute/v1"
)

// realCloudConfirmPhrase and dedicatedTeardownBudget (defined once, in
// aws_realcloud_test.go, this package) are reused here unmodified - one
// shared confirmation phrase and one shared teardown budget for every
// real-cloud provider piece, exactly like azure_realcloud_test.go already
// does, rather than a byte-identical GCP-prefixed copy that could silently
// drift from the shared value if it were ever rotated.

// gcpPollInterval bounds how often this file's own manual wait loops
// (waitForGCPInstanceStatus/waitForGCPInstanceAbsent) re-query the
// independent verification client. google.golang.org/api/compute/v1 - the
// plain REST client this codebase already uses for the adapter itself
// (gcp_sdk.go) - ships no built-in operation waiter equivalent to
// aws-sdk-go-v2/service/ec2's NewInstanceRunningWaiter/
// NewInstanceTerminatedWaiter, so this file provides its own, deliberately
// simple, poll loop rather than pulling in a second, heavier GCP client
// library just for a waiter.
const gcpPollInterval = 3 * time.Second

type qualifyGCPEnv struct {
	project, region, zone, image, subnetwork, machineType string
	allocationID, evidenceDir                             string
	maxRuntime                                            time.Duration
}

func loadQualifyGCPEnv(t *testing.T) qualifyGCPEnv {
	t.Helper()
	get := func(name string) string {
		value := os.Getenv(name)
		if value == "" {
			t.Fatalf("%s must be set for real-cloud GCP qualification", name)
		}
		return value
	}
	minutes, err := strconv.Atoi(get("RUNNERSCOUT_QUALIFY_MAX_RUNTIME_MINUTES"))
	if err != nil || minutes < 1 || minutes > 20 {
		// 20 mirrors qualify-gcp.yml's own hard-coded ceiling (the same
		// ceiling aws_realcloud_test.go/qualify-aws.yml chose) - checked
		// again here so this test never trusts the workflow's own
		// validation step alone.
		t.Fatalf("RUNNERSCOUT_QUALIFY_MAX_RUNTIME_MINUTES must be an integer between 1 and 20, got %q", os.Getenv("RUNNERSCOUT_QUALIFY_MAX_RUNTIME_MINUTES"))
	}
	env := qualifyGCPEnv{
		project:      get("RUNNERSCOUT_QUALIFY_PROJECT"),
		region:       get("RUNNERSCOUT_QUALIFY_REGION"),
		zone:         get("RUNNERSCOUT_QUALIFY_ZONE"),
		image:        get("RUNNERSCOUT_QUALIFY_IMAGE"),
		subnetwork:   get("RUNNERSCOUT_QUALIFY_SUBNETWORK"),
		machineType:  get("RUNNERSCOUT_QUALIFY_MACHINE_TYPE"),
		allocationID: get("RUNNERSCOUT_QUALIFY_ALLOCATION_ID"),
		evidenceDir:  get("RUNNERSCOUT_QUALIFY_EVIDENCE_DIR"),
		maxRuntime:   time.Duration(minutes) * time.Minute,
	}
	return env
}

// newIndependentGCPComputeService builds a Compute Engine client through
// google.golang.org/api/compute/v1's own default behavior when given no
// explicit HTTP client or credential option: it resolves Application
// Default Credentials itself (the exact GOOGLE_APPLICATION_CREDENTIALS/
// CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE file google-github-actions/auth
// exported in the qualify-gcp.yml job) - never the adapter's own internal/
// provider newGCPSDK/gcpFileCredential/gcpAuthTransport construction. It
// exists solely so this test's own verification calls (waiting for real
// state, re-querying after delete) never depend on the exact code path
// under qualification also being the code path that certifies its own
// success - the same "independence of code path, not identity" reasoning
// aws_realcloud_test.go's newIndependentEC2Client documents (see
// docs/qualification-real-cloud.background.md).
func newIndependentGCPComputeService(ctx context.Context) (*compute.Service, error) {
	service, err := compute.NewService(ctx)
	if err != nil {
		return nil, fmt.Errorf("independent verification client: %w", err)
	}
	return service, nil
}

// waitForGCPInstanceStatus polls the independent client until the named
// instance reports the given Compute Engine status (e.g. "RUNNING"), a
// definitive absence, or the timeout elapses.
func waitForGCPInstanceStatus(ctx context.Context, service *compute.Service, project, zone, name, status string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	var lastStatus string
	for {
		vm, err := service.Instances.Get(project, zone, name).Context(ctx).Do()
		if err == nil {
			lastStatus = vm.Status
			if vm.Status == status {
				return nil
			}
		} else if !missingGCP(err) {
			return fmt.Errorf("querying instance status: %w", err)
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("timed out waiting for instance status %q: last error: %w", status, lastErr)
			}
			return fmt.Errorf("timed out waiting for instance status %q: last observed %q", status, lastStatus)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(gcpPollInterval):
		}
	}
}

// waitForGCPInstanceAbsent polls the independent client until the named
// instance definitively no longer exists (a 404), or the timeout elapses.
func waitForGCPInstanceAbsent(ctx context.Context, service *compute.Service, project, zone, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		_, err := service.Instances.Get(project, zone, name).Context(ctx).Do()
		if err != nil && missingGCP(err) {
			return nil
		}
		if err != nil && !missingGCP(err) {
			return fmt.Errorf("querying instance for absence: %w", err)
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for instance to be deleted")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(gcpPollInterval):
		}
	}
}

type qualifyGCPRecord struct {
	Step  string      `json:"step"`
	At    time.Time   `json:"at"`
	Data  interface{} `json:"data,omitempty"`
	Error string      `json:"error,omitempty"`
}

type qualifyGCPEvidence struct {
	dir     string
	mu      sync.Mutex
	records []qualifyGCPRecord
}

func newQualifyGCPEvidence(dir string) *qualifyGCPEvidence { return &qualifyGCPEvidence{dir: dir} }

func (e *qualifyGCPEvidence) record(step string, data interface{}, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rec := qualifyGCPRecord{Step: step, At: time.Now().UTC(), Data: data}
	if err != nil {
		rec.Error = err.Error()
	}
	e.records = append(e.records, rec)
}

func (e *qualifyGCPEvidence) flush(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(e.dir, 0o755); err != nil {
		t.Errorf("evidence directory: %v", err)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	payload, err := json.MarshalIndent(struct {
		Scope   string             `json:"scope"`
		Records []qualifyGCPRecord `json:"records"`
	}{
		Scope:   "real GCP Compute Engine Spot lifecycle qualification (issue #3): one real billed instance created, observed and deleted through the production adapter, with cleanup independently re-verified via a separate compute.Service client; real price observation is out of scope (issue #2 - see this file's header)",
		Records: e.records,
	}, "", "  ")
	if err != nil {
		t.Errorf("evidence marshal: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(e.dir, "manifest.json"), append(payload, '\n'), 0o644); err != nil {
		t.Errorf("evidence write: %v", err)
	}
}

func TestQualifyRealGCPSpotLifecycle(t *testing.T) {
	requireRealCloudConfirmation(t)
	env := loadQualifyGCPEnv(t)

	evidence := newQualifyGCPEvidence(env.evidenceDir)
	// Registered first, so - per testing.T.Cleanup's documented last-added,
	// first-called order - it runs LAST, after the delete/independent-
	// verify cleanup registered below once a real instance exists. This is
	// what lets the final manifest.json include the cleanup outcome instead
	// of being written before cleanup even ran.
	t.Cleanup(func() { evidence.flush(t) })

	verify, err := newIndependentGCPComputeService(context.Background())
	if err != nil {
		evidence.record("independent-client-setup", nil, err)
		t.Fatalf("independent verification client: %v", err)
	}

	p, cleanupCreds, err := NewCommand(Config{
		Kind:    "gcp",
		Project: env.project,
		Owner:   "runnerscout-qualify-gcp",
		Subnet:  env.subnetwork,
	}, nil) // nil environment: reads GOOGLE_APPLICATION_CREDENTIALS/CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE directly from this process's own env, exactly as google-github-actions/auth populated them.
	if err != nil {
		evidence.record("adapter-setup", nil, err)
		t.Fatalf("NewCommand: %v", err)
	}
	t.Cleanup(func() { _ = cleanupCreds() })
	// See this file's header: a synthetic, non-functional placeholder,
	// never a real GitHub credential - this test qualifies the GCP provider
	// adapter's real-VM lifecycle, not real runner registration.
	p.Bootstrap = func(context.Context, string) (string, error) {
		return "realcloud-fixture-not-a-github-credential", nil
	}

	a := lifecycle.Allocation{
		ID: env.allocationID,
		Offering: placement.Offering{
			Region:  env.region,
			Zone:    env.zone,
			Machine: env.machineType,
			Image:   env.image,
			Spot:    true,
		},
	}

	createCtx, cancelCreate := context.WithTimeout(context.Background(), env.maxRuntime)
	defer cancelCreate()

	initial, err := p.Observe(createCtx, a)
	evidence.record("pre-create-inventory", initial, err)
	if err != nil || !initial.Known || initial.Exists {
		t.Fatalf("pre-create inventory not clean: %+v %v", initial, err)
	}

	receipt, err := p.CreateWithResources(createCtx, a)
	evidence.record("create", receipt, err)
	if err != nil {
		if errors.Is(err, lifecycle.ErrCapacity) {
			t.Skipf("GCP reported no Spot capacity for this pinned pool right now - not a defect, just unavailable capacity: %v", err)
		}
		t.Fatalf("real GCP create failed: %+v %v", receipt, err)
	}
	if receipt.ResourceID != env.allocationID {
		t.Fatalf("real GCP create returned an unexpected resource id: %+v", receipt)
	}
	a.ResourceID, a.Resources = receipt.ResourceID, receipt.Resources
	t.Logf("real GCP Spot instance created: %s", receipt.ResourceID)

	// Registered after create succeeded, so it is the most-recently-added
	// Cleanup func and therefore runs FIRST - before evidence.flush above.
	// Uses its own fresh, fixed-budget context (never createCtx, which may
	// already be exhausted by the time cleanup runs) so a create/observe
	// phase that used its whole budget still gets a full, unhurried
	// teardown attempt. This is this test's own in-process cleanup
	// guarantee; qualify-gcp.yml's own `if: always()` final step is the
	// authoritative backstop for the case where this process is killed
	// (job timeout/cancellation) before this Cleanup func can even run.
	t.Cleanup(func() {
		teardownCtx, cancel := context.WithTimeout(context.Background(), dedicatedTeardownBudget)
		defer cancel()

		deleteErr := p.Delete(teardownCtx, a)
		evidence.record("delete", nil, deleteErr)
		if deleteErr != nil {
			t.Errorf("real GCP delete failed: %v", deleteErr)
		}

		absentErr := waitForGCPInstanceAbsent(teardownCtx, verify, env.project, env.zone, env.allocationID, dedicatedTeardownBudget)
		evidence.record("independent-instance-absent-wait", nil, absentErr)
		if absentErr != nil {
			t.Errorf("independent wait for instance deletion failed: %v", absentErr)
		}

		_, diskErr := verify.Disks.Get(env.project, env.zone, env.allocationID).Context(teardownCtx).Do()
		leftoverDisk := diskErr == nil
		evidence.record("independent-post-delete-disk", map[string]any{"leftover": leftoverDisk}, nil)
		if diskErr != nil && !missingGCP(diskErr) {
			t.Errorf("independent post-delete disk query failed: %v", diskErr)
			return
		}

		// This adapter never allocates a static/reserved address (createGCP
		// forces an empty AccessConfigs list, so no external IP is ever
		// assigned, and no compute.Address is ever created) - this check
		// exists purely as a defensive backstop confirming that stays true,
		// exactly like aws_realcloud_test.go independently re-checks for
		// leftover ENIs it also never expects to find.
		var leftoverAddresses []*compute.Address
		listErr := verify.Addresses.List(env.project, env.region).
			Filter(fmt.Sprintf(`labels.runnerscout-owner="runnerscout-qualify-gcp" AND labels.runnerscout-operation="%s"`, env.allocationID)).
			Pages(teardownCtx, func(page *compute.AddressList) error {
				leftoverAddresses = append(leftoverAddresses, page.Items...)
				return nil
			})
		evidence.record("independent-post-delete-addresses", leftoverAddresses, listErr)
		if listErr != nil {
			t.Errorf("independent post-delete address query failed: %v", listErr)
			return
		}
		if leftoverDisk || len(leftoverAddresses) != 0 {
			t.Errorf("independent inventory found leftover billable resources after delete: disk=%v address(es)=%d", leftoverDisk, len(leftoverAddresses))
		}
	})

	// Independent confirmation of a real RUNNING state - not merely
	// "exists" (lifecycle.Observation.Exists also covers other transient
	// statuses), and not derived from the adapter's own session, but from
	// the separately-constructed verification client.
	if err := waitForGCPInstanceStatus(createCtx, verify, env.project, env.zone, env.allocationID, "RUNNING", env.maxRuntime); err != nil {
		evidence.record("independent-running-wait", nil, err)
		t.Fatalf("independent wait for RUNNING state failed: %v", err)
	}
	evidence.record("independent-running-wait", "confirmed", nil)

	observed, err := p.Observe(createCtx, a)
	evidence.record("post-running-observe", observed, err)
	if err != nil || !observed.Known || !observed.Exists {
		t.Fatalf("adapter observe after confirmed RUNNING state: %+v %v", observed, err)
	}
	if observed.Interrupted {
		t.Fatalf("adapter reported interruption for a real, healthy running instance: %+v", observed)
	}
	// Named limit (see this file's header): a real Spot preemption cannot be
	// forced here. This only proves "not interrupted" is reported correctly
	// against a real healthy instance, not that the preemption path fires
	// against a real preemption.
	evidence.record("interruption-detection-limit", map[string]any{
		"forced_real_interruption": false,
		"reason":                   `GCP provides no supported on-demand API to force a real Spot preemption; the closest tool ("gcloud compute instances simulate-maintenance-event") exercises host-maintenance/live-migration handling, not confirmed to produce the compute.instances.preempted zone operation gcpConfirmedPreemption looks for. gcpConfirmedPreemption is covered separately by synthesized-operation-payload unit tests, not by this real-cloud run.`,
	}, nil)
	// Named limit (see this file's header, and docs/prices-gcp.md/
	// prices-gcp.background.md for issue #2's investigation): no GCPSpotClient
	// exists in internal/prices, so no real price observation is attempted
	// here, unlike aws_realcloud_test.go's real Spot price call.
	evidence.record("real-spot-price-observation", map[string]any{
		"attempted": false,
		"reason":    "no internal/prices GCP client exists (issue #2): Compute Engine's SKU catalog splits Core/RAM with no structured machine-type field, no zone-level pricing and no request-side filter, so a GCP price client was assessed as not honestly buildable without guessing - see docs/prices-gcp.md/prices-gcp.background.md",
	}, nil)
}
