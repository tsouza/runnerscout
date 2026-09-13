package provider

import (
	"context"
	"errors"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/tsouza/runnerscout/internal/lifecycle"
)

// The v1 bootstrap requires a generalized Linux managed image with only an OS
// disk. Images with data disks need additional durable cleanup identities before
// they can be supported. The managed-image API supplies no architecture field.
func (p *Command) validateAzureImage(ctx context.Context, a lifecycle.Allocation) error {
	id, err := arm.ParseResourceID(a.Offering.Image)
	if err != nil || !strings.EqualFold(id.ResourceType.String(), "Microsoft.Compute/images") || id.SubscriptionID == "" || id.ResourceGroupName == "" || a.Offering.Region == "" {
		return errors.New("Azure requires a managed-image resource ID")
	}
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
