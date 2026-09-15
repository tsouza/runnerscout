package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"errors"
	"fmt"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"strings"
	"time"
)

func (p *Command) azureClient() *AzureSDK {
	p.azureOnce.Do(func() {
		if p.Azure == nil {
			p.Azure = &AzureSDK{}
		}
	})
	return p.Azure
}
func (p *Command) azureID(kind, name string) string {
	return "/subscriptions/" + p.Config.Subscription + "/resourceGroups/" + p.Config.ResourceGroup + "/providers/" + kind + "/" + name
}

type azureResource struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	Type      string             `json:"type"`
	Tags      map[string]string  `json:"tags"`
	UID       string             `json:"-"`
	ManagedBy string             `json:"-"`
	VM        *azureVMProperties `json:"-"`
	// PrivateIP is this network interface's private IP address, read from
	// the same GET response azureInventory already fetches for UID/ownership
	// verification (properties.ipConfigurations[].properties.privateIPAddress
	// - see azureInventory). Empty for every non-NIC resource and for a NIC
	// whose ipConfigurations shape isn't exactly the single entry this
	// codebase's own createAzure template ever produces.
	PrivateIP string `json:"-"`
}

func (p *Command) azureResources(ctx context.Context, a lifecycle.Allocation) ([]azureResource, error) {
	return p.azureInventory(ctx, a, false)
}

func (p *Command) azureTerminal(ctx context.Context, a lifecycle.Allocation) (bool, error) {
	terminal, err := p.azureClient().terminal(ctx, p.Config, a.ID)
	if err != nil {
		return false, fmt.Errorf("Azure deployment observation unavailable: %w", err)
	}
	return terminal, nil
}
func (p *Command) createAzure(ctx context.Context, a lifecycle.Allocation, script string) (lifecycle.Creation, error) {
	// Unlike this file's other 30-second budgets (inventory GETs, delete
	// submissions), deploy() below blocks on PollUntilDone for the entire
	// NIC+VM ARM template deployment to reach a terminal state - a real
	// deployment routinely takes well over 30 seconds, confirmed live
	// (a real dispatch's own deployment converged at ~26s, right at that
	// margin, and a second real dispatch exceeded it outright). A 30s
	// budget here was making "commitment unknown" - meant for genuine
	// transport ambiguity - the routine outcome instead, needlessly
	// relying on Observe-driven reconciliation (or, for the one-shot
	// qualification harness, the external safety net) to pick up
	// resources a merely-slow-but-healthy deployment already created.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if !strings.HasPrefix(strings.ToLower(a.Offering.Image), "/subscriptions/") {
		return lifecycle.Creation{}, errors.New("Azure requires a pinned managed-image resource ID")
	}
	if err := p.azureClient().requireVacantCreation(ctx, p.Config, a.ID); err != nil {
		return lifecycle.Creation{}, err
	}
	if err := p.validateAzureImage(ctx, a); err != nil {
		return lifecycle.Creation{}, err
	}
	tags := map[string]string{"runnerscout-owner": p.Config.Owner, "runnerscout-operation": a.ID}
	nicID := p.azureID("Microsoft.Network/networkInterfaces", a.ID+"-nic")
	storageProfile := map[string]any{"imageReference": map[string]string{"id": a.Offering.Image}, "osDisk": map[string]any{"name": a.ID + "-os", "createOption": "FromImage", "deleteOption": "Delete", "managedDisk": map[string]string{"storageAccountType": "StandardSSD_LRS"}}}
	if p.Config.AzureDiskControllerType != "" {
		storageProfile["diskControllerType"] = p.Config.AzureDiskControllerType
	}
	properties := map[string]any{
		"hardwareProfile": map[string]string{"vmSize": a.Offering.Machine},
		"storageProfile":  storageProfile,
		"osProfile":       map[string]any{"computerName": a.ID, "adminUsername": "runner", "customData": "[parameters('bootstrap')]", "linuxConfiguration": map[string]any{"disablePasswordAuthentication": true, "ssh": map[string]any{"publicKeys": []any{map[string]string{"path": "/home/runner/.ssh/authorized_keys", "keyData": p.Config.SSHPublicKey}}}}},
		"networkProfile":  map[string]any{"networkInterfaces": []any{map[string]any{"id": nicID, "properties": map[string]any{"primary": true, "deleteOption": "Delete"}}}},
	}
	if a.Offering.Spot {
		properties["priority"] = "Spot"
		properties["evictionPolicy"] = "Delete"
		properties["billingProfile"] = map[string]any{"maxPrice": json.Number(fmt.Sprintf("%d.%06d", a.Requirements.MaxPriceMicros/1000000, a.Requirements.MaxPriceMicros%1000000))}
	}
	template := map[string]any{"$schema": "https://schema.management.azure.com/schemas/2019-04-01/deploymentTemplate.json#", "contentVersion": "1.0.0.0", "parameters": map[string]any{"bootstrap": map[string]string{"type": "securestring"}}, "resources": []any{
		map[string]any{"type": "Microsoft.Network/networkInterfaces", "apiVersion": "2024-05-01", "name": a.ID + "-nic", "location": a.Offering.Region, "tags": tags, "properties": map[string]any{"networkSecurityGroup": map[string]string{"id": p.Config.SecurityGroup}, "ipConfigurations": []any{map[string]any{"name": "private", "properties": map[string]any{"privateIPAllocationMethod": "Dynamic", "subnet": map[string]string{"id": p.Config.Subnet}}}}}},
		map[string]any{"type": "Microsoft.Compute/virtualMachines", "apiVersion": "2024-07-01", "name": a.ID, "location": a.Offering.Region, "zones": []string{a.Offering.Zone}, "tags": tags, "dependsOn": []string{nicID}, "properties": properties},
	}}
	bootstrap := base64.StdEncoding.EncodeToString([]byte(script))
	if err := p.azureClient().deploy(ctx, p.Config, a.ID, template, bootstrap); err != nil {
		if p.azureClient().deploymentCapacityRejected(ctx, p.Config, a.ID) {
			if resources, ierr := p.azureResources(ctx, a); ierr == nil && len(resources) == 0 {
				return lifecycle.Creation{}, lifecycle.ErrCapacity
			}
		}
		// The commitment is unknown regardless of *why* deploy() failed (a
		// capacity rejection is definitive and returns above; everything
		// else - a transient transport error, a non-capacity Failed
		// deployment, a context deadline - leaves survivorship unconfirmed
		// the same way), but the real cause matters for diagnosing which of
		// those it was. Wrapping instead of discarding is what let the
		// "fast poller failure" investigation identify its actual error on
		// the next dispatch instead of guessing from Activity Log timing
		// alone (see docs/qualification-real-cloud.background.md).
		return lifecycle.Creation{}, fmt.Errorf("Azure deployment commitment unknown: %w", err)
	}
	return p.finishAzureDiskOwnership(ctx, a)
}

func (p *Command) finishAzureDiskOwnership(ctx context.Context, a lifecycle.Allocation) (lifecycle.Creation, error) {
	// A completed deployment gives a deterministic VM receipt even if later
	// ownership confirmation fails. Lifecycle retains it with unknown commitment.
	vmID := p.azureID("Microsoft.Compute/virtualMachines", a.ID)
	receipt := lifecycle.Creation{ResourceID: vmID}
	binding, err := p.azureDiskBinding(ctx, a)
	if err != nil {
		return receipt, err
	}
	receipt.Resources = []lifecycle.ResourceReference{
		{Kind: "azure-vm", ID: vmID, UID: binding.vmUID},
		{Kind: "azure-disk", ID: p.azureID("Microsoft.Compute/disks", a.ID+"-os"), UID: binding.diskUID},
	}
	proof := a
	proof.Resources, _, err = lifecycle.MergeResources(a.Resources, receipt.Resources)
	if err != nil {
		return receipt, err
	}
	resources, err := p.azureInventory(ctx, proof, true)
	if err != nil {
		return receipt, err
	}
	observed, err := p.azureObservation(proof, resources)
	if err != nil {
		return receipt, err
	}
	receipt.Resources = observed.Resources
	// The NIC's private IP is already present in the exact network interface
	// GET response azureInventory just performed above for ownership/UID
	// verification - see lifecycle.Allocation.WireGuardEndpoint's doc comment
	// for why this is captured (no new API call) and what it resolves.
	for _, resource := range resources {
		if strings.EqualFold(resource.Type, "Microsoft.Network/networkInterfaces") {
			receipt.WireGuardEndpoint = wireGuardEndpoint(resource.PrivateIP)
		}
	}
	if len(resources) != 3 {
		return receipt, errors.New("Azure creation dependencies incomplete")
	}
	if azureValue(binding.tags["runnerscout-owner"]) == p.Config.Owner && azureValue(binding.tags["runnerscout-operation"]) == a.ID {
		return receipt, nil
	}
	binding.tags["runnerscout-owner"] = &p.Config.Owner
	binding.tags["runnerscout-operation"] = &a.ID
	if err := p.azureClient().tagDisk(ctx, p.Config, a.ID, binding.tags); err != nil {
		return receipt, fmt.Errorf("Azure disk ownership tagging incomplete: %w", err)
	}
	confirmed, err := p.azureDiskBinding(ctx, proof)
	if err != nil || confirmed.vmUID != binding.vmUID || confirmed.diskUID != binding.diskUID || azureValue(confirmed.tags["runnerscout-owner"]) != p.Config.Owner || azureValue(confirmed.tags["runnerscout-operation"]) != a.ID {
		return receipt, errors.New("Azure disk ownership update unconfirmed")
	}
	return receipt, nil
}

func (p *Command) observeAzure(ctx context.Context, a lifecycle.Allocation) (lifecycle.Observation, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := p.azureReferences(a); err != nil {
		return lifecycle.Observation{}, err
	}
	if a.ResourceID != "" && !strings.EqualFold(a.ResourceID, p.azureID("Microsoft.Compute/virtualMachines", a.ID)) {
		return lifecycle.Observation{}, errors.New("Azure allocation VM identity differs")
	}
	terminal, e := p.azureTerminal(ctx, a)
	if e != nil {
		return lifecycle.Observation{}, fmt.Errorf("Azure deployment commitment still unknown: %w", e)
	}
	if !terminal {
		return lifecycle.Observation{}, errors.New("Azure deployment commitment still unknown")
	}
	resources, e := p.azureResources(ctx, a)
	if e != nil {
		return lifecycle.Observation{}, e
	}
	observed, e := p.azureObservation(a, resources)
	if e != nil {
		return lifecycle.Observation{}, e
	}
	// VirtualMachinePreempted is Azure's own definitive confirmed-preemption
	// signal (delivered via Event Grid/Storage Queue, see
	// internal/azureevents and internal/azurequeue), correlated here by
	// exact ARM resource ID equality against p.AzureInterrupted - mirroring
	// AWS's Server.SpotInstanceTermination state reason
	// (aws_inventory.go's observation) and GCP's compute.instances.preempted
	// operation (gcp_sdk.go's gcpConfirmedPreemption). It is meaningful only
	// once the resource is independently confirmed absent, and only for
	// offerings requested as spot, exactly like both siblings.
	observed.Interrupted = !observed.Exists && a.Offering.Spot && p.azureConfirmedPreemption(a)
	return observed, nil
}

// azureConfirmedPreemption reports whether this Tick's AzureInterrupted
// snapshot (see Command.AzureInterrupted) confirms this allocation's own VM
// - not any other resource id observed this cycle - received a definitive
// VirtualMachinePreempted annotation. A nil map, an absent entry, or an
// entry recorded false all report false; never inferred from a bare
// absence, matching CQ-09.
func (p *Command) azureConfirmedPreemption(a lifecycle.Allocation) bool {
	if len(p.AzureInterrupted) == 0 {
		return false
	}
	want := p.azureID("Microsoft.Compute/virtualMachines", a.ID)
	for id, preempted := range p.AzureInterrupted {
		if preempted && strings.EqualFold(id, want) {
			return true
		}
	}
	return false
}

func (p *Command) reconcileAzureCreation(ctx context.Context, a lifecycle.Allocation) (lifecycle.Observation, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := p.azureReferences(a); err != nil {
		return lifecycle.Observation{}, err
	}
	if a.ResourceID != "" && !strings.EqualFold(a.ResourceID, p.azureID("Microsoft.Compute/virtualMachines", a.ID)) {
		return lifecycle.Observation{}, errors.New("Azure allocation VM identity differs")
	}
	terminal, err := p.azureTerminal(ctx, a)
	if err != nil || !terminal {
		return lifecycle.Observation{}, errors.New("Azure deployment commitment still unknown")
	}
	resources, err := p.azureInventory(ctx, a, true)
	if err != nil {
		return lifecycle.Observation{}, err
	}
	for _, resource := range resources {
		if (strings.EqualFold(resource.Type, "Microsoft.Compute/virtualMachines") || strings.EqualFold(resource.Type, "Microsoft.Compute/disks")) && (resource.Tags["runnerscout-owner"] != p.Config.Owner || resource.Tags["runnerscout-operation"] != a.ID) {
			receipt, err := p.finishAzureDiskOwnership(ctx, a)
			if err != nil {
				return lifecycle.Observation{ResourceID: receipt.ResourceID, Resources: receipt.Resources}, err
			}
			a.Resources, _, err = lifecycle.MergeResources(a.Resources, receipt.Resources)
			if err != nil {
				return lifecycle.Observation{}, err
			}
			a.ResourceID = receipt.ResourceID
			break
		}
	}
	observed, err := p.observeAzure(ctx, a)
	if err != nil {
		return lifecycle.Observation{ResourceID: a.ResourceID, Resources: a.Resources}, err
	}
	return observed, nil
}
func (p *Command) deleteAzure(ctx context.Context, a lifecycle.Allocation) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := p.azureReferences(a); err != nil {
		return err
	}
	terminal, e := p.azureTerminal(ctx, a)
	if e != nil || !terminal {
		return errors.New("Azure deployment still active; deletion deferred")
	}
	resources, e := p.azureResources(ctx, a)
	if e != nil {
		return e
	}
	observation, e := p.azureObservation(a, resources)
	if e != nil {
		return e
	}
	if _, changed, err := lifecycle.MergeResources(a.Resources, observation.Resources); err != nil || changed {
		return errors.New("Azure dependencies require durable checkpoint before deletion")
	}
	// Delete one dependent resource per reconciliation and re-observe before the next.
	for _, kind := range []string{"Microsoft.Compute/virtualMachines", "Microsoft.Network/networkInterfaces", "Microsoft.Compute/disks"} {
		for _, resource := range resources {
			if strings.EqualFold(resource.Type, kind) {
				if resource.ManagedBy != "" {
					continue
				}
				if strings.EqualFold(kind, "Microsoft.Compute/virtualMachines") {
					if err := p.azureVMDeletionBindings(a, resource.VM); err != nil {
						return err
					}
				}
				if err := p.azureClient().delete(ctx, p.Config, resource); err != nil {
					return fmt.Errorf("Azure deletion commitment unknown: %w", err)
				}
				return nil
			}
		}
	}
	return nil
}
