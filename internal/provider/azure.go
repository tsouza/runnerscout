package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"strings"
)

func (p *Command) az(ctx context.Context, args ...string) ([]byte, error) {
	return p.Exec.Run(ctx, "az", append(args, "--subscription", p.Config.Subscription, "--output", "json", "--only-show-errors")...)
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
	query := fmt.Sprintf("[?name=='%s' || name=='%s-nic' || name=='%s-os']", a.ID, a.ID, a.ID)
	out, e := p.az(ctx, "resource", "list", "--resource-group", p.Config.ResourceGroup, "--query", query)
	if e != nil {
		return nil, e
	}
	var resources []azureResource
	if e = json.Unmarshal(out, &resources); e != nil || resources == nil {
		return nil, errors.New("invalid Azure resource inventory")
	}
	expected := map[string]string{a.ID: "Microsoft.Compute/virtualMachines", a.ID + "-nic": "Microsoft.Network/networkInterfaces", a.ID + "-os": "Microsoft.Compute/disks"}
	seen := map[string]bool{}
	for _, resource := range resources {
		kind, ok := expected[resource.Name]
		if !ok || seen[resource.Name] || !strings.EqualFold(resource.Type, kind) || !strings.EqualFold(resource.ID, p.azureID(kind, resource.Name)) {
			return nil, errors.New("Azure resource identity mismatch")
		}
		if resource.Tags["runnerscout-owner"] != p.Config.Owner || resource.Tags["runnerscout-operation"] != a.ID {
			return nil, errors.New("Azure resource ownership unconfirmed; cleanup retained")
		}
		seen[resource.Name] = true
	}
	return resources, nil
}
func (p *Command) azureTerminal(ctx context.Context, a lifecycle.Allocation) (bool, error) {
	out, e := p.az(ctx, "deployment", "group", "list", "--resource-group", p.Config.ResourceGroup, "--query", fmt.Sprintf("[?name=='%s']", a.ID))
	if e != nil {
		return false, e
	}
	var deployments []struct {
		Properties struct {
			State string `json:"provisioningState"`
		} `json:"properties"`
	}
	if e = json.Unmarshal(out, &deployments); e != nil || deployments == nil || len(deployments) > 1 {
		return false, errors.New("invalid Azure deployment observation")
	}
	if len(deployments) == 0 {
		return true, nil
	} // Absence never permits blind create replay in the lifecycle.
	switch deployments[0].Properties.State {
	case "Succeeded", "Failed", "Canceled":
		return true, nil
	default:
		return false, nil
	}
}
func (p *Command) createAzure(ctx context.Context, a lifecycle.Allocation, script string) (string, error) {
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
	b, e := json.Marshal(template)
	if e != nil {
		return "", e
	}
	path, clean, e := privateFile(string(b))
	if e != nil {
		return "", e
	}
	defer clean()
	b, e = json.Marshal(map[string]any{"bootstrap": map[string]string{"value": base64.StdEncoding.EncodeToString([]byte(script))}})
	if e != nil {
		return "", e
	}
	params, cleanParams, e := privateFile(string(b))
	if e != nil {
		return "", e
	}
	defer cleanParams()
	if _, e = p.az(ctx, "deployment", "group", "create", "--name", a.ID, "--resource-group", p.Config.ResourceGroup, "--mode", "Incremental", "--template-file", path, "--parameters", "@"+params); e != nil {
		return "", e
	}
	// Explicitly tag the generated managed disk. If this response is lost or fails,
	// reconciliation remains unknown; an untagged residual disk is never deleted.
	diskID := p.azureID("Microsoft.Compute/disks", a.ID+"-os")
	if _, e = p.az(ctx, "disk", "update", "--ids", diskID, "--set", "tags.runnerscout-owner="+p.Config.Owner, "tags.runnerscout-operation="+a.ID); e != nil {
		return "", e
	}
	return p.azureID("Microsoft.Compute/virtualMachines", a.ID), nil
}
func (p *Command) observeAzure(ctx context.Context, a lifecycle.Allocation) (lifecycle.Observation, error) {
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
				_, e = p.az(ctx, "resource", "delete", "--ids", resource.ID)
				return e
			}
		}
	}
	return nil
}
