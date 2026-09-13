package provider

import (
	"context"
	"errors"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/google/uuid"
	"github.com/tsouza/runnerscout/internal/lifecycle"
)

type azureResourceSpec struct{ kind, resourceType, name, version string }

func azureSpecs(a lifecycle.Allocation) []azureResourceSpec {
	return []azureResourceSpec{
		{"azure-vm", "Microsoft.Compute/virtualMachines", a.ID, "2024-07-01"},
		{"azure-network-interface", "Microsoft.Network/networkInterfaces", a.ID + "-nic", "2024-05-01"},
		{"azure-disk", "Microsoft.Compute/disks", a.ID + "-os", "2024-03-02"},
	}
}

func (p *Command) azureReferences(a lifecycle.Allocation) (map[string]lifecycle.ResourceReference, error) {
	if a.ResourceID != "" && !strings.EqualFold(a.ResourceID, p.azureID("Microsoft.Compute/virtualMachines", a.ID)) {
		return nil, errors.New("Azure allocation VM identity differs")
	}
	result := map[string]lifecycle.ResourceReference{}
	references, _, err := lifecycle.MergeResources(nil, a.Resources)
	if err != nil {
		return nil, err
	}
	for _, reference := range references {
		valid := false
		for _, spec := range azureSpecs(a) {
			if reference.Kind != spec.kind {
				continue
			}
			uid, err := uuid.Parse(reference.UID)
			if err != nil || uid == uuid.Nil || reference.UID != uid.String() || !strings.EqualFold(reference.ID, p.azureID(spec.resourceType, spec.name)) {
				return nil, errors.New("invalid Azure durable resource identity")
			}
			if _, duplicate := result[reference.Kind]; duplicate {
				return nil, errors.New("duplicate Azure resource identity")
			}
			result[reference.Kind] = reference
			valid = true
		}
		if !valid {
			return nil, errors.New("unsupported Azure dependency kind")
		}
	}
	return result, nil
}

func azureResourceMissing(err error) bool {
	var response *azcore.ResponseError
	return errors.As(err, &response) && response.StatusCode == 404 && response.ErrorCode == "ResourceNotFound"
}

func (p *Command) azureOwnership(tags map[string]string, a lifecycle.Allocation, allowUntagged bool) error {
	for key, want := range map[string]string{"runnerscout-owner": p.Config.Owner, "runnerscout-operation": a.ID} {
		value, exists := tags[key]
		if value != want && (exists || !allowUntagged) {
			return errors.New("Azure resource ownership unconfirmed; cleanup retained")
		}
	}
	return nil
}

func (p *Command) azureInventory(ctx context.Context, a lifecycle.Allocation, allowUntaggedDisk bool) ([]azureResource, error) {
	references, err := p.azureReferences(a)
	if err != nil {
		return nil, err
	}
	listed, err := p.azureClient().list(ctx, p.Config, a.ID)
	if err != nil {
		return nil, errors.New("Azure resource inventory unavailable")
	}
	seen := map[string]bool{}
	for _, resource := range listed {
		valid := false
		for _, spec := range azureSpecs(a) {
			if !strings.EqualFold(resource.Name, spec.name) {
				continue
			}
			if seen[spec.kind] || !strings.EqualFold(resource.Type, spec.resourceType) || !strings.EqualFold(resource.ID, p.azureID(spec.resourceType, spec.name)) {
				return nil, errors.New("Azure inventory identity mismatch")
			}
			if err := p.azureOwnership(resource.Tags, a, allowUntaggedDisk && spec.kind == "azure-disk"); err != nil {
				return nil, err
			}
			seen[spec.kind], valid = true, true
		}
		if !valid {
			return nil, errors.New("unexpected Azure inventory resource")
		}
	}
	result := []azureResource{}
	// Resource paths are known before creation. Direct reads also cover omitted
	// inventory entries and retain recorded generations after discovery changes.
	for _, spec := range azureSpecs(a) {
		id := p.azureID(spec.resourceType, spec.name)
		resource, err := p.azureClient().get(ctx, p.Config, id, spec.version)
		if azureResourceMissing(err) {
			if seen[spec.kind] {
				return nil, errors.New("Azure inventory changed during observation")
			}
			continue
		}
		if err != nil || !p.azureIdentity(resource, spec.resourceType, spec.name) {
			return nil, errors.New("Azure resource identity unavailable")
		}
		tags, err := azureTagSnapshot(resource.Tags)
		if err != nil {
			return nil, err
		}
		item := azureResource{ID: id, Name: spec.name, Type: spec.resourceType, Tags: map[string]string{}, ManagedBy: azureValue(resource.ManagedBy)}
		for key, value := range tags {
			item.Tags[key] = *value
		}
		if err := p.azureOwnership(item.Tags, a, allowUntaggedDisk && spec.kind == "azure-disk"); err != nil {
			return nil, err
		}
		var properties struct {
			VMID           string `json:"vmId"`
			UniqueID       string `json:"uniqueId"`
			ResourceGUID   string `json:"resourceGuid"`
			VirtualMachine struct {
				ID string `json:"id"`
			} `json:"virtualMachine"`
		}
		if azureProperties(resource.Properties, &properties) != nil {
			return nil, errors.New("Azure resource generation missing")
		}
		value := properties.VMID
		if spec.kind == "azure-disk" {
			value = properties.UniqueID
		}
		if spec.kind == "azure-network-interface" {
			value = properties.ResourceGUID
			item.ManagedBy = properties.VirtualMachine.ID
		}
		uid, err := uuid.Parse(value)
		if err != nil || uid == uuid.Nil {
			return nil, errors.New("Azure resource generation invalid")
		}
		item.UID = uid.String()
		if spec.kind == "azure-vm" {
			item.VM = &azureVMProperties{}
			if azureProperties(resource.Properties, item.VM) != nil {
				return nil, errors.New("Azure VM dependency metadata unavailable")
			}
		}
		if previous, exists := references[spec.kind]; exists && previous.UID != item.UID {
			return nil, errors.New("Azure recorded resource generation changed")
		}
		if item.ManagedBy != "" && !strings.EqualFold(item.ManagedBy, p.azureID("Microsoft.Compute/virtualMachines", a.ID)) {
			return nil, errors.New("Azure dependency attached to another VM")
		}
		result = append(result, item)
	}
	return result, nil
}

func (p *Command) azureObservation(a lifecycle.Allocation, resources []azureResource) (lifecycle.Observation, error) {
	retained, err := p.azureReferences(a)
	if err != nil {
		return lifecycle.Observation{}, err
	}
	references := append([]lifecycle.ResourceReference{}, a.Resources...)
	for _, resource := range resources {
		for _, spec := range azureSpecs(a) {
			if strings.EqualFold(resource.Type, spec.resourceType) {
				reference := lifecycle.ResourceReference{Kind: spec.kind, ID: resource.ID, UID: resource.UID}
				if previous, exists := retained[spec.kind]; exists {
					if previous.UID != resource.UID {
						return lifecycle.Observation{}, errors.New("Azure recorded resource generation changed")
					}
					reference = previous // Preserve serialized ARM path casing across restarts.
				}
				references = append(references, reference)
			}
		}
	}
	references, _, err = lifecycle.MergeResources(nil, references)
	if err != nil {
		return lifecycle.Observation{}, err
	}
	return lifecycle.Observation{Known: true, Exists: len(resources) > 0, ResourceID: p.azureID("Microsoft.Compute/virtualMachines", a.ID), Resources: references}, nil
}

func (p *Command) azureVMDeletionBindings(a lifecycle.Allocation, vm *azureVMProperties) error {
	if vm == nil || len(vm.StorageProfile.DataDisks) != 0 || !strings.EqualFold(vm.StorageProfile.OSDisk.ManagedDisk.ID, p.azureID("Microsoft.Compute/disks", a.ID+"-os")) || len(vm.NetworkProfile.NetworkInterfaces) != 1 || !strings.EqualFold(vm.NetworkProfile.NetworkInterfaces[0].ID, p.azureID("Microsoft.Network/networkInterfaces", a.ID+"-nic")) {
		return errors.New("Azure VM has unconfirmed deletion dependencies")
	}
	return nil
}
