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
}

func (p *Command) azureResources(ctx context.Context, a lifecycle.Allocation) ([]azureResource, error) {
	return p.azureInventory(ctx, a, false)
}

func (p *Command) azureTerminal(ctx context.Context, a lifecycle.Allocation) (bool, error) {
	terminal, err := p.azureClient().terminal(ctx, p.Config, a.ID)
	if err != nil {
		return false, errors.New("Azure deployment observation unavailable")
	}
	return terminal, nil
}
func (p *Command) createAzure(ctx context.Context, a lifecycle.Allocation, script string) (lifecycle.Creation, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
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
	properties := map[string]any{
		"hardwareProfile": map[string]string{"vmSize": a.Offering.Machine},
		"storageProfile":  map[string]any{"imageReference": map[string]string{"id": a.Offering.Image}, "osDisk": map[string]any{"name": a.ID + "-os", "createOption": "FromImage", "deleteOption": "Delete", "managedDisk": map[string]string{"storageAccountType": "StandardSSD_LRS"}}},
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
		return lifecycle.Creation{}, errors.New("Azure deployment commitment unknown")
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
	if len(resources) != 3 {
		return receipt, errors.New("Azure creation dependencies incomplete")
	}
	if azureValue(binding.tags["runnerscout-owner"]) == p.Config.Owner && azureValue(binding.tags["runnerscout-operation"]) == a.ID {
		return receipt, nil
	}
	binding.tags["runnerscout-owner"] = &p.Config.Owner
	binding.tags["runnerscout-operation"] = &a.ID
	if err := p.azureClient().tagDisk(ctx, p.Config, a.ID, binding.tags); err != nil {
		return receipt, errors.New("Azure disk ownership tagging incomplete")
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
	if e != nil || !terminal {
		return lifecycle.Observation{}, errors.New("Azure deployment commitment still unknown")
	}
	resources, e := p.azureResources(ctx, a)
	if e != nil {
		return lifecycle.Observation{}, e
	}
	return p.azureObservation(a, resources)
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
		if strings.EqualFold(resource.Type, "Microsoft.Compute/virtualMachines") || strings.EqualFold(resource.Type, "Microsoft.Compute/disks") && (resource.Tags["runnerscout-owner"] != p.Config.Owner || resource.Tags["runnerscout-operation"] != a.ID) {
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
					return errors.New("Azure deletion commitment unknown")
				}
				return nil
			}
		}
	}
	return nil
}
