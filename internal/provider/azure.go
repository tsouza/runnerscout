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
	ID   string            `json:"id"`
	Name string            `json:"name"`
	Type string            `json:"type"`
	Tags map[string]string `json:"tags"`
}

func (p *Command) azureResources(ctx context.Context, a lifecycle.Allocation) ([]azureResource, error) {
	return p.azureInventory(ctx, a, false)
}

func (p *Command) azureInventory(ctx context.Context, a lifecycle.Allocation, allowUntaggedDisk bool) ([]azureResource, error) {
	resources, e := p.azureClient().list(ctx, p.Config, a.ID)
	if e != nil {
		return nil, errors.New("Azure resource inventory unavailable")
	}
	expected := map[string]string{a.ID: "Microsoft.Compute/virtualMachines", a.ID + "-nic": "Microsoft.Network/networkInterfaces", a.ID + "-os": "Microsoft.Compute/disks"}
	seen := map[string]bool{}
	for _, resource := range resources {
		name := strings.ToLower(resource.Name)
		kind, ok := expected[name]
		if !ok || seen[name] || !strings.EqualFold(resource.Type, kind) || !strings.EqualFold(resource.ID, p.azureID(kind, name)) {
			return nil, errors.New("Azure resource identity mismatch")
		}
		if resource.Tags["runnerscout-owner"] != p.Config.Owner || resource.Tags["runnerscout-operation"] != a.ID {
			if !allowUntaggedDisk || !strings.EqualFold(kind, "Microsoft.Compute/disks") {
				return nil, errors.New("Azure resource ownership unconfirmed; cleanup retained")
			}
			for key, want := range map[string]string{"runnerscout-owner": p.Config.Owner, "runnerscout-operation": a.ID} {
				if value, exists := resource.Tags[key]; exists && value != want {
					return nil, errors.New("Azure disk has conflicting ownership")
				}
			}
		}
		seen[name] = true
	}
	return resources, nil
}
func (p *Command) azureTerminal(ctx context.Context, a lifecycle.Allocation) (bool, error) {
	terminal, err := p.azureClient().terminal(ctx, p.Config, a.ID)
	if err != nil {
		return false, errors.New("Azure deployment observation unavailable")
	}
	return terminal, nil
}
func (p *Command) createAzure(ctx context.Context, a lifecycle.Allocation, script string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !strings.HasPrefix(strings.ToLower(a.Offering.Image), "/subscriptions/") {
		return "", errors.New("Azure requires a pinned managed-image resource ID")
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
		return "", errors.New("Azure deployment commitment unknown")
	}
	return p.finishAzureDiskOwnership(ctx, a)
}

func (p *Command) finishAzureDiskOwnership(ctx context.Context, a lifecycle.Allocation) (string, error) {
	// A completed deployment gives a deterministic VM receipt even if later
	// ownership confirmation fails. Lifecycle retains it with unknown commitment.
	vmID := p.azureID("Microsoft.Compute/virtualMachines", a.ID)
	binding, err := p.azureDiskBinding(ctx, a)
	if err != nil {
		return vmID, err
	}
	if azureValue(binding.tags["runnerscout-owner"]) == p.Config.Owner && azureValue(binding.tags["runnerscout-operation"]) == a.ID {
		return vmID, nil
	}
	binding.tags["runnerscout-owner"] = &p.Config.Owner
	binding.tags["runnerscout-operation"] = &a.ID
	if err := p.azureClient().tagDisk(ctx, p.Config, a.ID, binding.tags); err != nil {
		return vmID, errors.New("Azure disk ownership tagging incomplete")
	}
	observed, err := p.azureDiskBinding(ctx, a)
	if err != nil || observed.vmUID != binding.vmUID || observed.diskUID != binding.diskUID || azureValue(observed.tags["runnerscout-owner"]) != p.Config.Owner || azureValue(observed.tags["runnerscout-operation"]) != a.ID {
		return vmID, errors.New("Azure disk ownership update unconfirmed")
	}
	return vmID, nil
}

func (p *Command) observeAzure(ctx context.Context, a lifecycle.Allocation) (lifecycle.Observation, error) {
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
	return lifecycle.Observation{Known: true, Exists: len(resources) > 0, ResourceID: p.azureID("Microsoft.Compute/virtualMachines", a.ID)}, nil
}

func (p *Command) reconcileAzureCreation(ctx context.Context, a lifecycle.Allocation) (lifecycle.Observation, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
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
			if _, err := p.finishAzureDiskOwnership(ctx, a); err != nil {
				return lifecycle.Observation{}, err
			}
			break
		}
	}
	return p.observeAzure(ctx, a)
}
func (p *Command) deleteAzure(ctx context.Context, a lifecycle.Allocation) error {
	terminal, e := p.azureTerminal(ctx, a)
	if e != nil || !terminal {
		return errors.New("Azure deployment still active; deletion deferred")
	}
	resources, e := p.azureResources(ctx, a)
	if e != nil {
		return e
	}
	// Delete one dependent resource per reconciliation and re-observe before the next.
	for _, kind := range []string{"Microsoft.Compute/virtualMachines", "Microsoft.Network/networkInterfaces", "Microsoft.Compute/disks"} {
		for _, resource := range resources {
			if strings.EqualFold(resource.Type, kind) {
				if err := p.azureClient().delete(ctx, p.Config, resource); err != nil {
					return errors.New("Azure deletion commitment unknown")
				}
				return nil
			}
		}
	}
	return nil
}
