//go:build realcloud

// Real-Azure provider qualification (issue #3's remaining "Final real-cloud
// qualification" scope, Azure piece only): "Verify ordinary runs-on, actual
// VM job execution, interruption/restart and independent cleanup inventory
// for each provider. Local emulator results do not discharge the real-VM
// obligations." This is the Azure sibling of aws_realcloud_test.go - see
// that file and docs/qualification-real-cloud.md for the shared design
// philosophy. This file does not modify that one, or any production
// internal/provider Azure adapter file (azure.go, azure_sdk.go,
// azure_inventory.go, azure_image.go, azure_binding.go,
// internal/prices/azure.go).
//
// This file drives the production internal/provider Azure adapter against
// real Azure infrastructure: it creates ONE real, billed Azure Spot Virtual
// Machine (plus its dependent NIC and OS disk) via the adapter's own
// CreateWithResources, independently confirms it reaches a real
// "PowerState/running" state, observes a real Spot price, deletes it via
// the adapter's own Observe/Delete reconciliation loop, and independently
// re-queries the ARM API - through a second, separately-constructed client
// pair (see newIndependentAzureClients), never just trusting the adapter's
// own Observe()/Delete() - to confirm zero leftover billable resources. That
// second, independent client pair is exactly issue #3's "independent
// cleanup inventory" requirement: proof of cleanup that does not depend on
// the code path under qualification also being the code path certifying
// its own success.
//
// Only .github/workflows/qualify.yml is meant to ever run this: it is gated
// behind the "realcloud" build tag (distinct from the "emulators" tag
// azure_emulator_test.go uses) and workflow_dispatch-only. There is no
// separate in-process confirmation-phrase guard beyond that - see
// aws_realcloud_test.go's own header comment (and qualify.yml's) for why.
//
// Real-Azure specifics that differ from the AWS piece (see
// docs/qualification-real-cloud.background.md's Azure section for the full
// reasoning behind each):
//
//   - Azure's adapter provisions through one ARM template deployment
//     (createAzure/deploy in azure_sdk.go), not a single RunInstances call,
//     and its Delete tears down exactly one dependent resource (VM, then
//     NIC, then disk) per call - never all three at once. This test drives
//     that multi-step teardown to completion with its own bounded
//     Observe/Delete reconciliation loop (driveAzureTeardown), calling only
//     the adapter's public Observe and Delete methods, exactly like a real
//     controller would.
//   - The Azure managed-image API this adapter requires (validateAzureImage
//     in azure_image.go) has no architecture field at all, so unlike AWS
//     this test never selects or validates an "architecture" - there is
//     nothing Azure-side to validate it against.
//   - A pinned Availability Zone is a required, separate input (Azure zones
//     are a property of the VM resource itself, not derivable from the
//     subnet the way an AWS subnet pins an Availability Zone).
//   - Azure requires a real-shaped SSH public key (Config.SSHPublicKey) for
//     every Linux VM with password authentication disabled - this test
//     generates a fresh, throwaway ed25519 key pair itself
//     (generateEphemeralSSHPublicKey) rather than asking an operator to
//     provision and manage one: nothing ever needs to log in with it, so
//     nothing needs to durably exist beyond this one process's lifetime.
//   - Confirming a real running Power State needs the typed Microsoft.Compute
//     VirtualMachinesClient's instanceView expansion
//     (github.com/.../armcompute) - the generic armresources.Client the
//     production adapter itself uses for every other read (GetByID) does
//     not expose runtime instance-view/power-state data at all, only
//     static ARM resource properties. armcompute is therefore a real,
//     necessary addition to go.mod for this one independent-verification
//     purpose, not a speculative one - see this file's
//     newIndependentAzureClients.
//
// What this test does NOT and honestly CANNOT qualify:
//
//   - Real GitHub Actions job execution (issue #3's "ordinary runs-on,
//     actual VM job execution"). p.Bootstrap below returns a synthetic,
//     non-functional placeholder in place of a real GitHub JIT registration
//     token - mirroring aws_realcloud_test.go's and emulator_test.go's own
//     established "emulator-fixture-not-a-github-credential" precedent - so
//     the managed image's cloud-init runner-registration step is expected
//     to fail harmlessly inside the guest. This test only confirms the VM
//     boots and reaches a real running state at the ARM/Compute API level,
//     which is independent of whether user-data succeeds inside the guest.
//     Proving a real runner actually registers and executes a real workflow
//     job is a separate, larger, not-yet-built qualification piece - see
//     docs/qualification-real-cloud.md's "Known gaps" section.
//   - A real Spot eviction. Unlike AWS (which at least has a documented
//     "no API to force one" limit already recorded), Azure's generally
//     available Compute API has no interruption-forcing mechanism either -
//     the only Azure-side capability that can simulate a Spot eviction on
//     demand is Azure Chaos Studio, a separate, distinct, opt-in paid
//     service this codebase does not integrate with anywhere, and standing
//     it up is out of scope here. This test can only confirm the adapter
//     correctly reports "not interrupted" against a real, healthy running
//     instance. Server-side VirtualMachinePreempted detection
//     (azureConfirmedPreemption in azure.go, fed by internal/azurequeue) is
//     already covered against synthesized Event Grid/Storage Queue payloads
//     by the existing unit test suite - this file does not re-prove that
//     logic, only that it stays wired to a real instance's real state today.
package provider

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v8"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources/v3"
	"github.com/google/uuid"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/placement"
	"golang.org/x/crypto/ssh"
)

// dedicatedTeardownBudget (defined in aws_realcloud_test.go, this package)
// is reused as-is: the same fixed, hard-coded deletion deadline applies
// identically to both providers' teardown obligation, for the same reason -
// see that file's own doc comment.

type qualifyAzureEnv struct {
	region, subscriptionID, resourceGroup, subnetID string
	nsgID, imageID, vmSize, zone, allocationID      string
	evidenceDir                                     string
	maxRuntime                                      time.Duration
	// diskControllerType is deliberately optional (read directly via
	// os.Getenv, never through this file's own get() helper below) -
	// empty preserves Azure's own default controller-type inference
	// exactly as before this field existed. See
	// Config.AzureDiskControllerType's own doc comment for why this
	// exists at all: a classic managed image carries no controller-type
	// metadata of its own, and some VM size families only support one
	// specific controller type Azure can't always infer correctly.
	diskControllerType string
}

func loadQualifyAzureEnv(t *testing.T) qualifyAzureEnv {
	t.Helper()
	get := func(name string) string {
		value := os.Getenv(name)
		if value == "" {
			t.Fatalf("%s must be set for real-cloud Azure qualification", name)
		}
		return value
	}
	minutes, err := strconv.Atoi(get("RUNNERSCOUT_QUALIFY_MAX_RUNTIME_MINUTES"))
	if err != nil || minutes < 1 || minutes > 20 {
		// 20 mirrors qualify-azure.yml's own hard-coded ceiling (and
		// aws_realcloud_test.go's identical 20) - checked again here so
		// this test never trusts the workflow's own validation step alone.
		t.Fatalf("RUNNERSCOUT_QUALIFY_MAX_RUNTIME_MINUTES must be an integer between 1 and 20, got %q", os.Getenv("RUNNERSCOUT_QUALIFY_MAX_RUNTIME_MINUTES"))
	}
	env := qualifyAzureEnv{
		region:         get("RUNNERSCOUT_QUALIFY_REGION"),
		subscriptionID: get("RUNNERSCOUT_QUALIFY_SUBSCRIPTION_ID"),
		resourceGroup:  get("RUNNERSCOUT_QUALIFY_RESOURCE_GROUP"),
		subnetID:       get("RUNNERSCOUT_QUALIFY_SUBNET_ID"),
		nsgID:          get("RUNNERSCOUT_QUALIFY_NSG_ID"),
		imageID:        get("RUNNERSCOUT_QUALIFY_IMAGE_ID"),
		vmSize:         get("RUNNERSCOUT_QUALIFY_VM_SIZE"),
		zone:           get("RUNNERSCOUT_QUALIFY_ZONE"),
		allocationID:   get("RUNNERSCOUT_QUALIFY_ALLOCATION_ID"),
		evidenceDir:    get("RUNNERSCOUT_QUALIFY_EVIDENCE_DIR"),
		maxRuntime:     time.Duration(minutes) * time.Minute,
		// Optional: see this field's own doc comment on qualifyAzureEnv.
		diskControllerType: os.Getenv("RUNNERSCOUT_QUALIFY_AZURE_DISK_CONTROLLER_TYPE"),
	}
	if id, err := uuid.Parse(env.subscriptionID); err != nil || id == uuid.Nil {
		t.Fatalf("RUNNERSCOUT_QUALIFY_SUBSCRIPTION_ID must be a subscription GUID, got %q", env.subscriptionID)
	}
	if env.zone != "1" && env.zone != "2" && env.zone != "3" {
		t.Fatalf("RUNNERSCOUT_QUALIFY_ZONE must be one of \"1\", \"2\", \"3\", got %q", env.zone)
	}
	return env
}

// newIndependentAzureClients builds an ARM generic-resource client and a
// typed Compute VirtualMachines client through azidentity's plain
// DefaultAzureCredential chain - which, in the qualify-azure.yml job,
// resolves the exact same AZURE_CLIENT_ID/AZURE_TENANT_ID/
// AZURE_FEDERATED_TOKEN_FILE environment variables the workflow already
// exported for the production adapter's own credential resolution
// (credentials.go's azureCredential) - never that adapter's own
// AzureSDK.credentials()/azureCredential() construction. DefaultAzureCredential
// is a chain of several distinct credential types (environment, workload
// identity, managed identity, Azure CLI, ...), a different Go type and
// resolution path than credentials.go's explicit single-mode switch, so
// this is independence of *code path*, not of *identity* - the same
// distinction aws_realcloud_test.go's newIndependentEC2Client documents for
// AWS. It exists solely so this test's own verification calls (waiting for
// real running state, re-querying after delete) never depend on the exact
// code path under qualification also certifying its own success.
//
// The Compute VirtualMachinesClient (armcompute), specifically, is not used
// anywhere in production code (azure.go/azure_sdk.go only ever construct
// the generic armresources.Client, which cannot report a VM's real runtime
// power state - see this file's header comment) - it is added here purely
// because independently confirming a real running state requires it.
func newIndependentAzureClients(subscriptionID string) (*armresources.Client, *armcompute.VirtualMachinesClient, error) {
	credential, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("independent verification credential: %w", err)
	}
	resources, err := armresources.NewClient(subscriptionID, credential, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("independent verification resources client: %w", err)
	}
	compute, err := armcompute.NewVirtualMachinesClient(subscriptionID, credential, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("independent verification compute client: %w", err)
	}
	return resources, compute, nil
}

// generateEphemeralSSHPublicKey creates a fresh ed25519 key pair and returns
// only its public half in OpenSSH authorized_keys format - the shape
// Config.SSHPublicKey (and, downstream, azure.go's
// linuxConfiguration.ssh.publicKeys[].keyData) requires. The private half is
// discarded immediately: this qualification never logs in over SSH (see
// this file's header comment on Bootstrap), so nothing needs to durably
// hold a key capable of doing so. This avoids asking an operator to
// provision, store and rotate a real SSH credential for a VM nothing is
// ever meant to actually reach.
func generateEphemeralSSHPublicKey() (string, error) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("ephemeral SSH key generation: %w", err)
	}
	signer, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("ephemeral SSH key encoding: %w", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer))), nil
}

// azureInstanceViewRunning reports whether a VirtualMachine Get response's
// (requested with InstanceView expansion) status list contains Azure's own
// "PowerState/running" code - the real, guest-independent signal that the
// VM has actually started, analogous to AWS's EC2 "running" instance state
// that aws_realcloud_test.go's NewInstanceRunningWaiter confirms.
func azureInstanceViewRunning(vm armcompute.VirtualMachinesClientGetResponse) bool {
	if vm.Properties == nil || vm.Properties.InstanceView == nil {
		return false
	}
	for _, status := range vm.Properties.InstanceView.Statuses {
		if status != nil && status.Code != nil && strings.EqualFold(*status.Code, "PowerState/running") {
			return true
		}
	}
	return false
}

// waitForAzureRunningState polls the independent Compute client (never the
// adapter's own session) until the real VM reports PowerState/running, or
// ctx is done. There is no SDK-provided waiter for VM power state
// (unlike AWS's ec2.NewInstanceRunningWaiter), so this is a small,
// deliberately simple manual poll loop.
func waitForAzureRunningState(ctx context.Context, compute *armcompute.VirtualMachinesClient, resourceGroup, name string) (armcompute.VirtualMachinesClientGetResponse, error) {
	options := &armcompute.VirtualMachinesClientGetOptions{Expand: to.Ptr(armcompute.InstanceViewTypesInstanceView)}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		vm, err := compute.Get(ctx, resourceGroup, name, options)
		if err == nil && azureInstanceViewRunning(vm) {
			return vm, nil
		}
		select {
		case <-ctx.Done():
			if err != nil {
				return vm, fmt.Errorf("independent wait for running state: %w (last Get error: %v)", ctx.Err(), err)
			}
			return vm, fmt.Errorf("independent wait for running state: %w (last observed status was not PowerState/running)", ctx.Err())
		case <-ticker.C:
		}
	}
}

// waitForAzureVMAbsent polls the independent Compute client until the VM
// resource itself is confirmed gone (a real ResourceNotFound 404), or ctx is
// done. missingAzureResource is production's own pure error-shape predicate
// (azure_sdk.go) - reused here exactly like aws_realcloud_test.go reuses
// awsOwnershipFilters: a data-shape helper, not the adapter's session or
// credential construction, so reusing it does not compromise independence.
func waitForAzureVMAbsent(ctx context.Context, compute *armcompute.VirtualMachinesClient, resourceGroup, name string) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		_, err := compute.Get(ctx, resourceGroup, name, nil)
		if missingAzureResource(err) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("independent wait for VM absence: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// azureODataLiteral escapes a value for a single-quoted OData string
// literal (a literal single quote is doubled) - the same rule
// internal/prices/azure.go's own azureODataLiteral documents and applies.
// That helper lives in a different package (prices) and is unexported, so
// it cannot be imported here; duplicating this one-line, well-documented
// escaping rule locally is simpler than any alternative (a shared exported
// helper would exist for exactly one non-production caller).
func azureODataLiteral(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

// azureIndependentLeftovers lists every resource in the resource group
// tagged with this allocation's ownership, via the independent client -
// never production's own name-based AzureSDK.list(). A tag-based sweep
// catches anything tagged this way regardless of resource type or name,
// which is a strictly stronger independent check than re-deriving the same
// three expected names (VM/NIC/disk) production already assumes: it would
// also catch an unexpected leftover of a kind this adapter's template does
// not even provision today, such as a public IP address (see this file's
// header comment: azure.go's deployment template never allocates one, so
// none is expected here - this sweep is a defensive check for that,
// not a symptom of one existing).
func azureIndependentLeftovers(ctx context.Context, client *armresources.Client, resourceGroup, allocationID string) ([]string, error) {
	filter := fmt.Sprintf("tagName eq 'runnerscout-operation' and tagValue eq '%s'", azureODataLiteral(allocationID))
	pager := client.NewListByResourceGroupPager(resourceGroup, &armresources.ClientListByResourceGroupOptions{Filter: &filter})
	var ids []string
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, entry := range page.Value {
			if entry == nil || entry.ID == nil {
				return nil, errors.New("invalid Azure inventory entry during independent leftover sweep")
			}
			ids = append(ids, *entry.ID)
		}
	}
	return ids, nil
}

// driveAzureTeardown repeatedly calls the adapter's own public Observe and
// Delete - never any unexported adapter internal - until Observe reports no
// tracked resource still exists, or ctx's deadline (the caller's
// dedicatedTeardownBudget-bounded context) is reached. This is necessary,
// not optional, because deleteAzure (azure.go) deletes exactly one
// dependent resource (VM, then NIC, then disk) per call and requires a
// fresh Observe between calls to see the next one become deletable - unlike
// AWS's single TerminateInstances call, which cascades through
// DeleteOnTermination in one shot. A transient Observe/Delete error is
// recorded as evidence but does not abort the loop: teardown must keep
// retrying until the budget is genuinely exhausted, never give up early on
// one bad cycle.
func driveAzureTeardown(ctx context.Context, p *Command, a lifecycle.Allocation, evidence *qualifyEvidence) (lifecycle.Observation, error) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		observed, err := p.Observe(ctx, a)
		evidence.record("teardown-observe", observed, err)
		if err == nil && observed.Known && !observed.Exists {
			return observed, nil
		}
		if deleteErr := p.Delete(ctx, a); deleteErr != nil {
			evidence.record("teardown-delete-step", nil, deleteErr)
		}
		select {
		case <-ctx.Done():
			return observed, fmt.Errorf("teardown budget exhausted with resources still present (last observe error: %v): %w", err, ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestQualifyRealAzureSpotLifecycle(t *testing.T) {
	env := loadQualifyAzureEnv(t)

	evidence := newQualifyEvidence(env.evidenceDir)
	// Registered first, so - per testing.T.Cleanup's documented last-added,
	// first-called order - it runs LAST, after the delete/independent-
	// verify cleanup registered below once a real VM exists.
	t.Cleanup(func() { evidence.flush(t) })

	verifyResources, verifyCompute, err := newIndependentAzureClients(env.subscriptionID)
	if err != nil {
		evidence.record("independent-client-setup", nil, err)
		t.Fatalf("independent verification clients: %v", err)
	}

	sshPublicKey, err := generateEphemeralSSHPublicKey()
	if err != nil {
		evidence.record("ephemeral-ssh-key", nil, err)
		t.Fatalf("ephemeral SSH key: %v", err)
	}

	p, cleanupCreds, err := NewCommand(Config{
		Kind:                    "azure",
		Owner:                   "runnerscout-qualify-azure",
		Subscription:            env.subscriptionID,
		ResourceGroup:           env.resourceGroup,
		Subnet:                  env.subnetID,
		SecurityGroup:           env.nsgID,
		SSHPublicKey:            sshPublicKey,
		AzureDiskControllerType: env.diskControllerType,
	}, nil) // nil environment: reads AZURE_* directly from this process's own env, exactly as the workflow's federated-token step populated it.
	if err != nil {
		evidence.record("adapter-setup", nil, err)
		t.Fatalf("NewCommand: %v", err)
	}
	t.Cleanup(func() { _ = cleanupCreds() })
	// See this file's header: a synthetic, non-functional placeholder,
	// never a real GitHub credential - this test qualifies the Azure
	// provider adapter's real-VM lifecycle, not real runner registration.
	p.Bootstrap = func(context.Context, string) (string, error) {
		return "realcloud-fixture-not-a-github-credential", nil
	}

	a := lifecycle.Allocation{
		ID: env.allocationID,
		Offering: placement.Offering{
			Region:  env.region,
			Zone:    env.zone,
			Machine: env.vmSize,
			Image:   env.imageID,
			Spot:    true,
		},
		Requirements: placement.Requirements{
			// -1 (in micros) is Azure's own documented sentinel for
			// "no price cap, evict only on capacity, pay up to the
			// on-demand rate" - see azure.go's billingProfile.maxPrice
			// construction. AWS's RunInstances needs no equivalent value
			// at all (omitting a max price already means "cap at
			// on-demand"), which is why aws_realcloud_test.go never sets
			// Requirements - a real Azure-specific difference, not an
			// oversight.
			MaxPriceMicros: -1_000_000,
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
			t.Skipf("Azure reported no Spot capacity for this pinned pool right now - not a defect, just unavailable capacity: %v", err)
		}
		t.Fatalf("real Azure create failed: %+v %v", receipt, err)
	}
	wantVMID := p.azureID("Microsoft.Compute/virtualMachines", a.ID)
	if !strings.EqualFold(receipt.ResourceID, wantVMID) {
		t.Fatalf("real Azure create returned an unexpected VM id: got %q, want %q", receipt.ResourceID, wantVMID)
	}
	if len(receipt.Resources) != 3 {
		t.Fatalf("real Azure create did not confirm all three dependent resources (VM, NIC, disk): %+v", receipt)
	}
	a.ResourceID, a.Resources = receipt.ResourceID, receipt.Resources
	t.Logf("real Azure Spot VM created: %s", receipt.ResourceID)

	// Registered after create succeeded, so it is the most-recently-added
	// Cleanup func and therefore runs FIRST - before evidence.flush above.
	// Uses its own fresh, fixed-budget context (never createCtx, which may
	// already be exhausted by the time cleanup runs) so a create/observe/
	// price phase that used its whole budget still gets a full, unhurried
	// teardown attempt. This is this test's own in-process cleanup
	// guarantee; qualify-azure.yml's own `if: always()` final step is the
	// authoritative backstop for the case where this process is killed
	// (job timeout/cancellation) before this Cleanup func can even run.
	t.Cleanup(func() {
		teardownCtx, cancel := context.WithTimeout(context.Background(), dedicatedTeardownBudget)
		defer cancel()

		_, teardownErr := driveAzureTeardown(teardownCtx, p, a, evidence)
		if teardownErr != nil {
			t.Errorf("real Azure teardown did not confirm zero tracked resources within budget: %v", teardownErr)
		}

		vmAbsentErr := waitForAzureVMAbsent(teardownCtx, verifyCompute, env.resourceGroup, a.ID)
		evidence.record("independent-vm-absent-wait", nil, vmAbsentErr)
		if vmAbsentErr != nil {
			t.Errorf("independent wait for VM absence failed: %v", vmAbsentErr)
		}

		leftovers, leftoverErr := azureIndependentLeftovers(teardownCtx, verifyResources, env.resourceGroup, env.allocationID)
		evidence.record("independent-post-delete-leftovers", leftovers, leftoverErr)
		if leftoverErr != nil {
			t.Errorf("independent post-delete inventory query failed: %v", leftoverErr)
			return
		}
		if len(leftovers) != 0 {
			t.Errorf("independent inventory found leftover billable Azure resource(s) after delete: %v", leftovers)
		}
	})

	// Independent confirmation of a real running state - not merely
	// "exists" (lifecycle.Observation.Exists also covers every other
	// non-terminal provisioning state), and not derived from the adapter's
	// own session, but from the separately-constructed verification client.
	runningVM, err := waitForAzureRunningState(createCtx, verifyCompute, env.resourceGroup, a.ID)
	if err != nil {
		evidence.record("independent-running-wait", nil, err)
		t.Fatalf("independent wait for running state failed: %v", err)
	}
	evidence.record("independent-running-wait", runningVM.Properties.InstanceView.Statuses, nil)

	observed, err := p.Observe(createCtx, a)
	evidence.record("post-running-observe", observed, err)
	if err != nil || !observed.Known || !observed.Exists {
		t.Fatalf("adapter observe after confirmed running state: %+v %v", observed, err)
	}
	if observed.Interrupted {
		t.Fatalf("adapter reported interruption for a real, healthy running instance: %+v", observed)
	}
	// Named limit (see this file's header): a real Spot eviction cannot be
	// forced here. This only proves "not interrupted" is reported correctly
	// against a real healthy instance, not that the interruption path fires
	// against a real eviction.
	evidence.record("interruption-detection-limit", map[string]any{
		"forced_real_interruption": false,
		"reason":                   "Azure's generally available Compute API provides no way to force a Spot eviction on demand (only the separate, unintegrated Azure Chaos Studio service can); azureConfirmedPreemption's Event Grid/Storage Queue-fed detection is covered separately by synthesized-payload unit tests, not by this real-cloud run",
	}, nil)

	quote, priceErr := p.Azure.SpotPrices().Observe(createCtx, env.region, env.zone, env.vmSize)
	evidence.record("real-spot-price-observation", quote, priceErr)
	if priceErr != nil {
		// Non-fatal: a pricing-endpoint miss must not skip the teardown
		// registered above, and provisioning already succeeded regardless.
		t.Errorf("real Azure spot price observation failed: %v", priceErr)
	} else {
		t.Logf("real Azure spot price observed: %d micros %s at %s", quote.PriceMicros, quote.Currency, quote.ObservedAt)
	}
}

// qualifyRecord, qualifyEvidence and newQualifyEvidence are defined once in
// aws_realcloud_test.go (this package) and reused here unmodified - the
// evidence-manifest shape has nothing provider-specific about it.
