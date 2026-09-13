package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources/v3"
	"github.com/google/uuid"
	"github.com/tsouza/runnerscout/internal/lifecycle"
)

// ARM resource names can be reused. Retain both service-generated identities
// while checking a disk's association with the VM created by this allocation.
type azureDiskBinding struct {
	vmUID, diskUID string
	tags           map[string]*string
}

type azureVMProperties struct {
	VMID              string `json:"vmId"`
	ProvisioningState string `json:"provisioningState"`
	StorageProfile    struct {
		DataDisks      []any `json:"dataDisks"`
		ImageReference struct {
			ID string `json:"id"`
		} `json:"imageReference"`
		OSDisk struct {
			Name         string `json:"name"`
			CreateOption string `json:"createOption"`
			ManagedDisk  struct {
				ID string `json:"id"`
			} `json:"managedDisk"`
		} `json:"osDisk"`
	} `json:"storageProfile"`
	NetworkProfile struct {
		NetworkInterfaces []struct {
			ID string `json:"id"`
		} `json:"networkInterfaces"`
	} `json:"networkProfile"`
}
type azureDiskProperties struct {
	UniqueID          string `json:"uniqueId"`
	ProvisioningState string `json:"provisioningState"`
	CreationData      struct {
		CreateOption   string `json:"createOption"`
		ImageReference struct {
			ID string `json:"id"`
		} `json:"imageReference"`
	} `json:"creationData"`
}

func azureProperties(value any, target any) error {
	if value == nil {
		return errors.New("Azure resource properties missing")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return errors.New("Azure resource properties invalid")
	}
	if err = json.Unmarshal(data, target); err != nil {
		return errors.New("Azure resource properties invalid")
	}
	return nil
}
func azureValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func azureUUID(value string) bool { id, err := uuid.Parse(value); return err == nil && id != uuid.Nil }
func (p *Command) azureIdentity(resource armresources.GenericResource, kind, name string) bool {
	return strings.EqualFold(azureValue(resource.ID), p.azureID(kind, name)) && strings.EqualFold(azureValue(resource.Type), kind) && strings.EqualFold(azureValue(resource.Name), name)
}

// Azure tag names are case-insensitive. Preserve unrelated spelling, but reject
// duplicate spellings and canonicalize ownership keys before merging an update.
func azureTagSnapshot(tags map[string]*string) (map[string]*string, error) {
	result := make(map[string]*string, len(tags))
	seen := map[string]bool{}
	for name, value := range tags {
		key := strings.ToLower(name)
		if seen[key] || value == nil {
			return nil, errors.New("Azure disk tag inventory ambiguous or incomplete")
		}
		seen[key] = true
		if key == "runnerscout-owner" || key == "runnerscout-operation" {
			name = key
		}
		copied := *value
		result[name] = &copied
	}
	return result, nil
}
func (p *Command) azureDiskBinding(ctx context.Context, a lifecycle.Allocation) (azureDiskBinding, error) {
	references, err := p.azureReferences(a)
	if err != nil {
		return azureDiskBinding{}, err
	}
	diskID := p.azureID("Microsoft.Compute/disks", a.ID+"-os")
	vmID := p.azureID("Microsoft.Compute/virtualMachines", a.ID)
	disk, err := p.azureClient().get(ctx, p.Config, diskID, "2024-03-02")
	if err != nil || !p.azureIdentity(disk, "Microsoft.Compute/disks", a.ID+"-os") {
		return azureDiskBinding{}, errors.New("Azure disk identity unavailable")
	}
	diskTags, err := azureTagSnapshot(disk.Tags)
	if err != nil {
		return azureDiskBinding{}, err
	}
	for name, want := range map[string]string{"runnerscout-owner": p.Config.Owner, "runnerscout-operation": a.ID} {
		if value, exists := diskTags[name]; exists && (value == nil || *value != want) {
			return azureDiskBinding{}, errors.New("Azure disk has conflicting ownership")
		}
	}
	if !strings.EqualFold(azureValue(disk.ManagedBy), vmID) {
		return azureDiskBinding{}, errors.New("Azure disk is not attached to the allocation VM")
	}
	var diskProperties azureDiskProperties
	if azureProperties(disk.Properties, &diskProperties) != nil || !azureUUID(diskProperties.UniqueID) || diskProperties.ProvisioningState != "Succeeded" || !strings.EqualFold(diskProperties.CreationData.CreateOption, "FromImage") {
		return azureDiskBinding{}, errors.New("Azure disk creation binding unconfirmed")
	}
	if image := diskProperties.CreationData.ImageReference.ID; image != "" && !strings.EqualFold(image, a.Offering.Image) {
		return azureDiskBinding{}, errors.New("Azure disk image binding differs")
	}
	vm, err := p.azureClient().get(ctx, p.Config, vmID, "2024-07-01")
	vmTags, tagError := azureTagSnapshot(vm.Tags)
	if err != nil || tagError != nil || !p.azureIdentity(vm, "Microsoft.Compute/virtualMachines", a.ID) || azureValue(vmTags["runnerscout-owner"]) != p.Config.Owner || azureValue(vmTags["runnerscout-operation"]) != a.ID {
		return azureDiskBinding{}, errors.New("Azure VM ownership unconfirmed")
	}
	var vmProperties azureVMProperties
	if azureProperties(vm.Properties, &vmProperties) != nil || !azureUUID(vmProperties.VMID) || vmProperties.ProvisioningState != "Succeeded" {
		return azureDiskBinding{}, errors.New("Azure VM creation binding unconfirmed")
	}
	storage := vmProperties.StorageProfile
	if !strings.EqualFold(storage.ImageReference.ID, a.Offering.Image) || !strings.EqualFold(storage.OSDisk.Name, a.ID+"-os") || !strings.EqualFold(storage.OSDisk.CreateOption, "FromImage") || !strings.EqualFold(storage.OSDisk.ManagedDisk.ID, diskID) {
		return azureDiskBinding{}, errors.New("Azure VM and disk creation graph differs")
	}
	binding := azureDiskBinding{vmUID: strings.ToLower(vmProperties.VMID), diskUID: strings.ToLower(diskProperties.UniqueID), tags: diskTags}
	for kind, uid := range map[string]string{"azure-vm": binding.vmUID, "azure-disk": binding.diskUID} {
		if previous, exists := references[kind]; exists && previous.UID != uid {
			return azureDiskBinding{}, errors.New("Azure recorded creation generation changed")
		}
	}
	return binding, nil
}
