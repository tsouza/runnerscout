package provider

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armdeployments"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources/v3"
)

// AzureSDK uses the official ARM and identity libraries. Options and Credential
// allow an isolated transport in conformance tests; production uses Azure Public.
type AzureSDK struct {
	Credential azcore.TokenCredential
	Options    *arm.ClientOptions
	once       sync.Once
	credential azcore.TokenCredential
	err        error

	// PricesHTTPClient and PricesEndpoint override the Azure Retail Prices
	// API's transport and endpoint for local fixtures only, never product
	// configuration - mirroring AWSSDK's own HTTPClient/Endpoint test
	// injection fields. They are kept separate from Options/Credential
	// above on purpose: the Retail Prices API (prices.azure.com) is a
	// distinct, unauthenticated public REST endpoint, not an ARM
	// management-plane call, so it has no ARM client options or credential
	// to share.
	PricesHTTPClient *http.Client
	PricesEndpoint   string
}

func (a *AzureSDK) credentials() (azcore.TokenCredential, error) {
	a.once.Do(func() {
		if a.Credential != nil {
			a.credential = a.Credential
			return
		}
		// Select one explicit authentication mechanism. Never invoke developer CLIs
		// or fall back to a different identity after configured credentials fail.
		switch {
		case os.Getenv("AZURE_FEDERATED_TOKEN_FILE") != "":
			a.credential, a.err = azidentity.NewWorkloadIdentityCredential(nil)
		case os.Getenv("AZURE_CLIENT_SECRET") != "" || os.Getenv("AZURE_CLIENT_CERTIFICATE_PATH") != "":
			a.credential, a.err = azidentity.NewEnvironmentCredential(nil)
		default:
			options := &azidentity.ManagedIdentityCredentialOptions{}
			if id := os.Getenv("AZURE_CLIENT_ID"); id != "" {
				options.ID = azidentity.ClientID(id)
			}
			a.credential, a.err = azidentity.NewManagedIdentityCredential(options)
		}
	})
	return a.credential, a.err
}
func (a *AzureSDK) resources(c Config) (*armresources.Client, error) {
	credential, err := a.credentials()
	if err != nil {
		return nil, err
	}
	return armresources.NewClient(c.Subscription, credential, a.Options)
}
func (a *AzureSDK) deployments(c Config) (*armdeployments.DeploymentsClient, error) {
	credential, err := a.credentials()
	if err != nil {
		return nil, err
	}
	return armdeployments.NewDeploymentsClient(c.Subscription, credential, a.Options)
}
func missingAzureResource(err error) bool {
	var response *azcore.ResponseError
	return errors.As(err, &response) && response.StatusCode == 404 && (response.ErrorCode == "ResourceNotFound" || response.ErrorCode == "DeploymentNotFound")
}

// ARM deployment submission is create-or-update, so occupied names must not be
// used as a retry mechanism. This preflight does not make submission atomic;
// the dedicated resource group must still exclude competing resource writers.
func (a *AzureSDK) requireVacantCreation(ctx context.Context, c Config, id string) error {
	missing := func(err error, deployment bool) bool {
		var response *azcore.ResponseError
		return errors.As(err, &response) && response.StatusCode == 404 &&
			(response.ErrorCode == "ResourceNotFound" || deployment && response.ErrorCode == "DeploymentNotFound")
	}
	client, err := a.deployments(c)
	if err != nil {
		return errors.New("Azure creation inventory unavailable")
	}
	if _, err := client.Get(ctx, c.ResourceGroup, id, nil); !missing(err, true) {
		return errors.New("Azure deployment name occupied or absence unconfirmed")
	}
	for _, resource := range []struct{ kind, name, version string }{
		{"Microsoft.Compute/virtualMachines", id, "2024-07-01"},
		{"Microsoft.Network/networkInterfaces", id + "-nic", "2024-05-01"},
		{"Microsoft.Compute/disks", id + "-os", "2024-03-02"},
	} {
		path := "/subscriptions/" + c.Subscription + "/resourceGroups/" + c.ResourceGroup + "/providers/" + resource.kind + "/" + resource.name
		if _, err := a.get(ctx, c, path, resource.version); !missing(err, false) {
			return errors.New("Azure resource name occupied or absence unconfirmed")
		}
	}
	return nil
}
func (a *AzureSDK) list(ctx context.Context, c Config, id string) ([]azureResource, error) {
	client, err := a.resources(c)
	if err != nil {
		return nil, err
	}
	result := []azureResource{}
	pager := client.NewListByResourceGroupPager(c.ResourceGroup, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		if page.Value == nil {
			return nil, errors.New("incomplete Azure inventory")
		}
		for _, entry := range page.Value {
			if entry == nil || entry.Name == nil {
				return nil, errors.New("invalid Azure inventory entry")
			}
			if !strings.EqualFold(*entry.Name, id) && !strings.EqualFold(*entry.Name, id+"-nic") && !strings.EqualFold(*entry.Name, id+"-os") {
				continue
			}
			if entry.ID == nil || entry.Type == nil {
				return nil, errors.New("invalid Azure resource identity")
			}
			resource := azureResource{ID: *entry.ID, Name: *entry.Name, Type: *entry.Type, Tags: map[string]string{}}
			tags, err := azureTagSnapshot(entry.Tags)
			if err != nil {
				return nil, err
			}
			for key, value := range tags {
				resource.Tags[key] = *value
			}
			result = append(result, resource)
		}
	}
	return result, nil
}
func (a *AzureSDK) terminal(ctx context.Context, c Config, id string) (bool, error) {
	client, err := a.deployments(c)
	if err != nil {
		return false, err
	}
	result, err := client.Get(ctx, c.ResourceGroup, id, nil)
	if missingAzureResource(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if result.Properties == nil || result.Properties.ProvisioningState == nil {
		return false, errors.New("invalid Azure deployment state")
	}
	switch *result.Properties.ProvisioningState {
	case "Succeeded", "Failed", "Canceled":
		return true, nil
	default:
		return false, nil
	}
}

// azureCapacityErrorCodes are ARM allocation-failure codes that definitively
// mean no compute capacity was available, never a quota, permission or
// transient failure a different pool could not resolve.
var azureCapacityErrorCodes = map[string]bool{
	"OverconstrainedAllocationRequest":      true,
	"OverconstrainedZonalAllocationRequest": true,
	"AllocationFailed":                      true,
	"ZonalAllocationFailed":                 true,
}

func azureCapacityFailure(err *armdeployments.ErrorResponse) bool {
	if err == nil {
		return false
	}
	if err.Code != nil && azureCapacityErrorCodes[*err.Code] {
		return true
	}
	for _, detail := range err.Details {
		if azureCapacityFailure(detail) {
			return true
		}
	}
	return false
}

// deploymentCapacityRejected reports whether a terminal Failed deployment's
// structured error definitively identifies a capacity rejection. Any
// ambiguity - an unavailable observation, a non-Failed state or an
// unrecognized code - returns false so the caller keeps the unknown-effect
// classification instead.
func (a *AzureSDK) deploymentCapacityRejected(ctx context.Context, c Config, id string) bool {
	client, err := a.deployments(c)
	if err != nil {
		return false
	}
	result, err := client.Get(ctx, c.ResourceGroup, id, nil)
	if err != nil || result.Properties == nil || result.Properties.ProvisioningState == nil || *result.Properties.ProvisioningState != "Failed" {
		return false
	}
	return azureCapacityFailure(result.Properties.Error)
}
func (a *AzureSDK) deploy(ctx context.Context, c Config, id string, template map[string]any, bootstrap string) error {
	client, err := a.deployments(c)
	if err != nil {
		return err
	}
	mode := armdeployments.DeploymentModeIncremental
	poller, err := client.BeginCreateOrUpdate(ctx, c.ResourceGroup, id, armdeployments.Deployment{Properties: &armdeployments.DeploymentProperties{Mode: &mode, Template: template, Parameters: map[string]*armdeployments.DeploymentParameter{"bootstrap": {Value: bootstrap}}}}, nil)
	if err != nil {
		return err
	}
	_, err = poller.PollUntilDone(ctx, &runtime.PollUntilDoneOptions{Frequency: time.Second})
	return err
}
func (a *AzureSDK) get(ctx context.Context, c Config, id, version string) (armresources.GenericResource, error) {
	client, err := a.resources(c)
	if err != nil {
		return armresources.GenericResource{}, err
	}
	result, err := client.GetByID(ctx, id, version, nil)
	return result.GenericResource, err
}

func (a *AzureSDK) tagDisk(ctx context.Context, c Config, id string, tags map[string]*string) error {
	client, err := a.resources(c)
	if err != nil {
		return err
	}
	resourceID := "/subscriptions/" + c.Subscription + "/resourceGroups/" + c.ResourceGroup + "/providers/Microsoft.Compute/disks/" + id + "-os"
	poller, err := client.BeginUpdateByID(ctx, resourceID, "2024-03-02", armresources.GenericResource{Tags: tags}, nil)
	if err != nil {
		return err
	}
	_, err = poller.PollUntilDone(ctx, &runtime.PollUntilDoneOptions{Frequency: time.Second})
	return err
}

func (a *AzureSDK) delete(ctx context.Context, c Config, resource azureResource) error {
	client, err := a.resources(c)
	if err != nil {
		return err
	}
	version := ""
	switch strings.ToLower(resource.Type) {
	case "microsoft.compute/virtualmachines":
		version = "2024-07-01"
	case "microsoft.compute/disks":
		version = "2024-03-02"
	case "microsoft.network/networkinterfaces":
		version = "2024-05-01"
	default:
		return errors.New("unsupported Azure cleanup resource")
	}
	// A successful start is not absence. Lifecycle keeps the cleanup obligation
	// until a later independently observed inventory confirms removal.
	_, err = client.BeginDeleteByID(ctx, resource.ID, version, nil)
	if missingAzureResource(err) {
		return nil
	}
	return err
}
