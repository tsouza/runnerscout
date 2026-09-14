package provider

import (
	"context"
	"errors"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/tsouza/runnerscout/internal/lifecycle"
)

// azureGalleryImageVersionAPIVersion is used for both
// Microsoft.Compute/galleries/images/versions and its parent
// Microsoft.Compute/galleries/images (the image definition) - verified
// directly against the real Azure API to carry every property this file
// reads from each (see docs/qualification-real-cloud.background.md for
// why a Compute Gallery image version is accepted here at all: some VM
// size families require a controller type a classic managed image cannot
// declare, and a gallery image definition's own `features` can).
const azureGalleryImageVersionAPIVersion = "2024-03-03"

// The v1 bootstrap requires a generalized Linux image with only an OS
// disk, from either of two resource shapes: a classic managed image
// (Microsoft.Compute/images/...) or a Compute Gallery image version
// (Microsoft.Compute/galleries/<gallery>/images/<definition>/versions/<version>).
// Images with data disks need additional durable cleanup identities before
// they can be supported. Neither resource shape supplies an architecture
// field on the resource this function reads.
func (p *Command) validateAzureImage(ctx context.Context, a lifecycle.Allocation) error {
	id, err := arm.ParseResourceID(a.Offering.Image)
	if err != nil || id.SubscriptionID == "" || id.ResourceGroupName == "" || a.Offering.Region == "" {
		return errors.New("Azure requires a managed-image or gallery-image-version resource ID")
	}
	switch {
	case strings.EqualFold(id.ResourceType.String(), "Microsoft.Compute/images"):
		return p.validateAzureManagedImage(ctx, a, id)
	case strings.EqualFold(id.ResourceType.String(), "Microsoft.Compute/galleries/images/versions"):
		return p.validateAzureGalleryImageVersion(ctx, a, id)
	default:
		return errors.New("Azure requires a managed-image or gallery-image-version resource ID")
	}
}

func (p *Command) validateAzureManagedImage(ctx context.Context, a lifecycle.Allocation, id *arm.ResourceID) error {
	image, err := p.azureClient().get(ctx, p.Config, a.Offering.Image, "2026-04-01")
	if err != nil || !strings.EqualFold(azureValue(image.ID), a.Offering.Image) || !strings.EqualFold(azureValue(image.Name), id.Name) || !strings.EqualFold(azureValue(image.Type), "Microsoft.Compute/images") || !strings.EqualFold(azureValue(image.Location), a.Offering.Region) {
		return errors.New("Azure image identity or region unconfirmed")
	}
	var properties struct {
		ProvisioningState string `json:"provisioningState"`
		StorageProfile    struct {
			OSDisk struct {
				OSType  string `json:"osType"`
				OSState string `json:"osState"`
			} `json:"osDisk"`
			DataDisks []any `json:"dataDisks"`
		} `json:"storageProfile"`
	}
	if azureProperties(image.Properties, &properties) != nil || properties.ProvisioningState != "Succeeded" || properties.StorageProfile.OSDisk.OSType != "Linux" || properties.StorageProfile.OSDisk.OSState != "Generalized" || len(properties.StorageProfile.DataDisks) != 0 {
		return errors.New("Azure requires an available generalized Linux image with only an OS disk")
	}
	return nil
}

// validateAzureGalleryImageVersion mirrors validateAzureManagedImage's
// checks, adapted to the gallery image version's own, different property
// shape: osType/osState live on the PARENT image definition
// (Microsoft.Compute/galleries/images), never on the version resource
// itself, so this needs a second GET the managed-image path does not.
func (p *Command) validateAzureGalleryImageVersion(ctx context.Context, a lifecycle.Allocation, id *arm.ResourceID) error {
	if id.Parent == nil || !strings.EqualFold(id.Parent.ResourceType.String(), "Microsoft.Compute/galleries/images") {
		return errors.New("Azure requires a managed-image or gallery-image-version resource ID")
	}
	version, err := p.azureClient().get(ctx, p.Config, a.Offering.Image, azureGalleryImageVersionAPIVersion)
	if err != nil || !strings.EqualFold(azureValue(version.ID), a.Offering.Image) || !strings.EqualFold(azureValue(version.Name), id.Name) || !strings.EqualFold(azureValue(version.Type), "Microsoft.Compute/galleries/images/versions") || !strings.EqualFold(azureValue(version.Location), a.Offering.Region) {
		return errors.New("Azure image identity or region unconfirmed")
	}
	var versionProperties struct {
		ProvisioningState string `json:"provisioningState"`
		StorageProfile    struct {
			DataDiskImages []any `json:"dataDiskImages"`
		} `json:"storageProfile"`
	}
	if azureProperties(version.Properties, &versionProperties) != nil || versionProperties.ProvisioningState != "Succeeded" || len(versionProperties.StorageProfile.DataDiskImages) != 0 {
		return errors.New("Azure requires an available gallery image version with only an OS disk")
	}
	definition, err := p.azureClient().get(ctx, p.Config, id.Parent.String(), azureGalleryImageVersionAPIVersion)
	if err != nil || !strings.EqualFold(azureValue(definition.Type), "Microsoft.Compute/galleries/images") {
		return errors.New("Azure gallery image definition unconfirmed")
	}
	var definitionProperties struct {
		OSType  string `json:"osType"`
		OSState string `json:"osState"`
	}
	if azureProperties(definition.Properties, &definitionProperties) != nil || definitionProperties.OSType != "Linux" || definitionProperties.OSState != "Generalized" {
		return errors.New("Azure requires an available generalized Linux gallery image")
	}
	return nil
}
