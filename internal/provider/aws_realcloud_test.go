//go:build realcloud

// Real-AWS provider qualification (issue #3's remaining "Final real-cloud
// qualification" scope, AWS piece only): "Verify ordinary runs-on, actual VM
// job execution, interruption/restart and independent cleanup inventory for
// each provider. Local emulator results do not discharge the real-VM
// obligations."
//
// This file drives the production internal/provider AWS adapter (aws.go,
// aws_sdk.go, aws_inventory.go, internal/prices/aws.go - none of which this
// file modifies) against real AWS infrastructure: it creates ONE real,
// billed EC2 Spot Instance, confirms it independently reaches EC2's own
// "running" state, observes a real Spot price, deletes it, and independently
// re-queries the AWS API - through a second, separately-constructed EC2
// client (see newIndependentEC2Client), never just trusting the adapter's
// own Observe()/Delete() - to confirm zero leftover billable resources. That
// second, independent client is exactly issue #3's "independent cleanup
// inventory" requirement: proof of cleanup that does not depend on the code
// path under qualification also being the code path certifying its own
// success.
//
// Only .github/workflows/qualify.yml is meant to ever run this: it is gated
// behind the "realcloud" build tag (distinct from the "emulators" tag
// emulator_test.go uses) and workflow_dispatch-only. There is no separate
// in-process confirmation-phrase guard beyond that: a human choosing to
// dispatch that workflow, with these specific inputs, already is the
// deliberate act this test needs - see qualify.yml's own header comment for
// why a second "type an exact phrase" gate was deliberately not added on
// top of workflow_dispatch itself.
//
// What this test does NOT and honestly CANNOT qualify:
//
//   - Real GitHub Actions job execution (issue #3's "ordinary runs-on,
//     actual VM job execution"). p.Bootstrap below returns a synthetic,
//     non-functional placeholder in place of a real GitHub JIT registration
//     token - mirroring emulator_test.go's own established
//     "emulator-fixture-not-a-github-credential" precedent - so the AMI's
//     cloud-init runner-registration step is expected to fail harmlessly
//     inside the guest. This test only confirms the VM boots and reaches a
//     real running state at the EC2 API level, which is independent of
//     whether user-data succeeds inside the guest. Proving a real runner
//     actually registers and executes a real workflow job is a separate,
//     larger, not-yet-built qualification piece (it needs a live scale set
//     and a real ephemeral registration token, not just the provider
//     adapter this file exercises) - see docs/qualification-real-cloud.md's
//     "Known gaps" section.
//   - A real Spot interruption. AWS provides no API to force one on demand.
//     This test can only confirm the real adapter correctly reports "not
//     interrupted" against a real, healthy running instance. The actual
//     Server.SpotInstanceTermination detection logic (aws_inventory.go's
//     observation()) is already covered against synthesized StateReason
//     payloads by the existing unit test suite (see
//     TestObserveAWSInterruption* in aws_inventory_test.go) - this file does
//     not re-prove that logic, only that it stays wired to a real instance's
//     real state today.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/placement"
)

// dedicatedTeardownBudget is a fixed, hard-coded deletion deadline,
// deliberately independent of the create/observe/price budget
// (qualifyAWSEnv.maxRuntime, derived from max_runtime_minutes). A run that
// spent its whole create/observe/price budget just confirming the VM works
// must still be able to tear it down - teardown is never shortened by
// running out of that earlier budget.
const dedicatedTeardownBudget = 5 * time.Minute

type qualifyAWSEnv struct {
	region, zone, ami, subnet, securityGroup, accountID   string
	instanceType, architecture, allocationID, evidenceDir string
	maxRuntime                                            time.Duration
}

func loadQualifyAWSEnv(t *testing.T) qualifyAWSEnv {
	t.Helper()
	get := func(name string) string {
		value := os.Getenv(name)
		if value == "" {
			t.Fatalf("%s must be set for real-cloud AWS qualification", name)
		}
		return value
	}
	minutes, err := strconv.Atoi(get("RUNNERSCOUT_QUALIFY_MAX_RUNTIME_MINUTES"))
	if err != nil || minutes < 1 || minutes > 20 {
		// 20 mirrors qualify-aws.yml's own hard-coded ceiling - checked
		// again here so this test never trusts the workflow's own
		// validation step alone.
		t.Fatalf("RUNNERSCOUT_QUALIFY_MAX_RUNTIME_MINUTES must be an integer between 1 and 20, got %q", os.Getenv("RUNNERSCOUT_QUALIFY_MAX_RUNTIME_MINUTES"))
	}
	env := qualifyAWSEnv{
		region:        get("RUNNERSCOUT_QUALIFY_REGION"),
		zone:          get("RUNNERSCOUT_QUALIFY_ZONE"),
		ami:           get("RUNNERSCOUT_QUALIFY_AMI"),
		subnet:        get("RUNNERSCOUT_QUALIFY_SUBNET"),
		securityGroup: get("RUNNERSCOUT_QUALIFY_SECURITY_GROUP"),
		accountID:     get("RUNNERSCOUT_QUALIFY_ACCOUNT_ID"),
		instanceType:  get("RUNNERSCOUT_QUALIFY_INSTANCE_TYPE"),
		architecture:  get("RUNNERSCOUT_QUALIFY_ARCHITECTURE"),
		allocationID:  get("RUNNERSCOUT_QUALIFY_ALLOCATION_ID"),
		evidenceDir:   get("RUNNERSCOUT_QUALIFY_EVIDENCE_DIR"),
		maxRuntime:    time.Duration(minutes) * time.Minute,
	}
	if env.architecture != "amd64" && env.architecture != "arm64" {
		t.Fatalf("RUNNERSCOUT_QUALIFY_ARCHITECTURE must be amd64 or arm64, got %q", env.architecture)
	}
	if len(env.accountID) != 12 {
		t.Fatalf("RUNNERSCOUT_QUALIFY_ACCOUNT_ID must be a 12-digit AWS account id, got %q", env.accountID)
	}
	return env
}

// newIndependentEC2Client builds an EC2 client through the standard
// aws-sdk-go-v2 default credential chain (which, in the qualify-aws.yml
// job, resolves the exact same AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY/
// AWS_SESSION_TOKEN environment variables aws-actions/configure-aws-
// credentials exported) - never the adapter's own internal/provider
// awsCredentialScope/session(). It exists solely so this test's own
// verification calls (waiting for real state, re-querying after delete)
// never depend on the exact code path under qualification also being the
// code path that certifies its own success.
func newIndependentEC2Client(ctx context.Context, region string) (*ec2.Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("independent verification client: %w", err)
	}
	return ec2.NewFromConfig(cfg), nil
}

type qualifyRecord struct {
	Step  string      `json:"step"`
	At    time.Time   `json:"at"`
	Data  interface{} `json:"data,omitempty"`
	Error string      `json:"error,omitempty"`
}

type qualifyEvidence struct {
	dir     string
	mu      sync.Mutex
	records []qualifyRecord
}

func newQualifyEvidence(dir string) *qualifyEvidence { return &qualifyEvidence{dir: dir} }

func (e *qualifyEvidence) record(step string, data interface{}, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rec := qualifyRecord{Step: step, At: time.Now().UTC(), Data: data}
	if err != nil {
		rec.Error = err.Error()
	}
	e.records = append(e.records, rec)
}

func (e *qualifyEvidence) flush(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(e.dir, 0o755); err != nil {
		t.Errorf("evidence directory: %v", err)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	payload, err := json.MarshalIndent(struct {
		Scope   string          `json:"scope"`
		Records []qualifyRecord `json:"records"`
	}{
		Scope:   "real AWS EC2 Spot lifecycle qualification (issue #3): one real billed instance created, observed, priced and deleted through the production adapter, with cleanup independently re-verified via a separate EC2 client",
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

func TestQualifyRealAWSSpotLifecycle(t *testing.T) {
	env := loadQualifyAWSEnv(t)

	evidence := newQualifyEvidence(env.evidenceDir)
	// Registered first, so - per testing.T.Cleanup's documented last-added,
	// first-called order - it runs LAST, after the delete/independent-
	// verify cleanup registered below once a real instance exists. This is
	// what lets the final manifest.json include the cleanup outcome instead
	// of being written before cleanup even ran.
	t.Cleanup(func() { evidence.flush(t) })

	verify, err := newIndependentEC2Client(context.Background(), env.region)
	if err != nil {
		evidence.record("independent-client-setup", nil, err)
		t.Fatalf("independent verification client: %v", err)
	}

	p, cleanupCreds, err := NewCommand(Config{
		Kind:          "aws",
		AccountID:     env.accountID,
		Owner:         "runnerscout-qualify-aws",
		Subnet:        env.subnet,
		SecurityGroup: env.securityGroup,
	}, nil) // nil environment: reads AWS_* directly from this process's own env, exactly as configure-aws-credentials populated it.
	if err != nil {
		evidence.record("adapter-setup", nil, err)
		t.Fatalf("NewCommand: %v", err)
	}
	t.Cleanup(func() { _ = cleanupCreds() })
	// See this file's header: a synthetic, non-functional placeholder,
	// never a real GitHub credential - this test qualifies the AWS
	// provider adapter's real-VM lifecycle, not real runner registration.
	p.Bootstrap = func(context.Context, string) (string, error) {
		return "realcloud-fixture-not-a-github-credential", nil
	}

	a := lifecycle.Allocation{
		ID: env.allocationID,
		Offering: placement.Offering{
			Region:       env.region,
			Zone:         env.zone,
			Architecture: env.architecture,
			Machine:      env.instanceType,
			Image:        env.ami,
			Spot:         true,
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
			t.Skipf("AWS reported no Spot capacity for this pinned pool right now - not a defect, just unavailable capacity: %v", err)
		}
		t.Fatalf("real AWS create failed: %+v %v", receipt, err)
	}
	if receipt.ResourceID == "" || !strings.HasPrefix(receipt.ResourceID, "i-") {
		t.Fatalf("real AWS create returned no usable instance id: %+v", receipt)
	}
	a.ResourceID, a.Resources = receipt.ResourceID, receipt.Resources
	t.Logf("real AWS Spot instance created: %s", receipt.ResourceID)

	// Registered after create succeeded, so it is the most-recently-added
	// Cleanup func and therefore runs FIRST - before evidence.flush above.
	// Uses its own fresh, fixed-budget context (never createCtx, which may
	// already be exhausted by the time cleanup runs) so a create/observe/
	// price phase that used its whole budget still gets a full, unhurried
	// teardown attempt. This is this test's own in-process cleanup
	// guarantee; qualify-aws.yml's own `if: always()` final step is the
	// authoritative backstop for the case where this process is killed
	// (job timeout/cancellation) before this Cleanup func can even run.
	t.Cleanup(func() {
		teardownCtx, cancel := context.WithTimeout(context.Background(), dedicatedTeardownBudget)
		defer cancel()

		deleteErr := p.Delete(teardownCtx, a)
		evidence.record("delete", nil, deleteErr)
		if deleteErr != nil {
			t.Errorf("real AWS delete failed: %v", deleteErr)
		}

		terminated, waitErr := ec2.NewInstanceTerminatedWaiter(verify).WaitForOutput(teardownCtx, &ec2.DescribeInstancesInput{InstanceIds: []string{receipt.ResourceID}}, dedicatedTeardownBudget)
		evidence.record("independent-terminated-wait", terminated, waitErr)
		if waitErr != nil {
			t.Errorf("independent wait for terminated state failed: %v", waitErr)
		}

		leftoverVolumes, volErr := verify.DescribeVolumes(teardownCtx, &ec2.DescribeVolumesInput{Filters: awsOwnershipFilters("runnerscout-qualify-aws", env.allocationID)})
		evidence.record("independent-post-delete-volumes", leftoverVolumes, volErr)
		leftoverInterfaces, ifaceErr := verify.DescribeNetworkInterfaces(teardownCtx, &ec2.DescribeNetworkInterfacesInput{Filters: awsOwnershipFilters("runnerscout-qualify-aws", env.allocationID)})
		evidence.record("independent-post-delete-network-interfaces", leftoverInterfaces, ifaceErr)
		if volErr != nil || ifaceErr != nil {
			t.Errorf("independent post-delete inventory query failed: volumes=%v interfaces=%v", volErr, ifaceErr)
			return
		}
		if len(leftoverVolumes.Volumes) != 0 || len(leftoverInterfaces.NetworkInterfaces) != 0 {
			t.Errorf("independent inventory found leftover billable resources after delete: %d volume(s), %d network interface(s)", len(leftoverVolumes.Volumes), len(leftoverInterfaces.NetworkInterfaces))
		}
	})

	// Independent confirmation of a real running state - not merely
	// "exists" (lifecycle.Observation.Exists also covers pending/stopping/
	// stopped/shutting-down), and not derived from the adapter's own
	// session, but from the separately-constructed verification client.
	if err := ec2.NewInstanceRunningWaiter(verify).Wait(createCtx, &ec2.DescribeInstancesInput{InstanceIds: []string{receipt.ResourceID}}, env.maxRuntime); err != nil {
		evidence.record("independent-running-wait", nil, err)
		t.Fatalf("independent wait for running state failed: %v", err)
	}
	evidence.record("independent-running-wait", "confirmed", nil)

	observed, err := p.Observe(createCtx, a)
	evidence.record("post-running-observe", observed, err)
	if err != nil || !observed.Known || !observed.Exists {
		t.Fatalf("adapter observe after confirmed running state: %+v %v", observed, err)
	}
	if observed.Interrupted {
		t.Fatalf("adapter reported interruption for a real, healthy running instance: %+v", observed)
	}
	// Named limit (see this file's header): a real Spot interruption cannot
	// be forced here. This only proves "not interrupted" is reported
	// correctly against a real healthy instance, not that the interruption
	// path fires against a real interruption.
	evidence.record("interruption-detection-limit", map[string]any{
		"forced_real_interruption": false,
		"reason":                   "AWS provides no API to force a Spot interruption on demand; Server.SpotInstanceTermination detection is covered separately by synthesized-payload unit tests, not by this real-cloud run",
	}, nil)

	quote, priceErr := p.AWS.SpotPrices().Observe(createCtx, env.region, env.zone, env.instanceType)
	evidence.record("real-spot-price-observation", quote, priceErr)
	if priceErr != nil {
		// Non-fatal: a pricing-endpoint miss must not skip the teardown
		// registered above, and provisioning already succeeded regardless.
		t.Errorf("real AWS spot price observation failed: %v", priceErr)
	} else {
		t.Logf("real AWS spot price observed: %d micros %s at %s", quote.PriceMicros, quote.Currency, quote.ObservedAt)
	}
}
