package provider

import (
	"context"
	"errors"

	"github.com/tsouza/runnerscout/internal/prices"
)

// AzureSpotPrices observes live Azure Retail Prices API Spot prices. Unlike
// AWSSpotPrices, it binds no credential scope at all: the Retail Prices API
// is a public, unauthenticated endpoint, so there is nothing to resolve
// fresh per region the way AWSSpotPrices.Observe resolves AWS credentials.
type AzureSpotPrices struct {
	sdk *AzureSDK
}

// SpotPrices returns a live Azure Spot price observer. It performs no
// network calls itself, mirroring AWSSDK.SpotPrices.
func (a *AzureSDK) SpotPrices() *AzureSpotPrices {
	return &AzureSpotPrices{sdk: a}
}

func (o *AzureSpotPrices) Observe(ctx context.Context, region, zone, instanceType string) (prices.Quote, error) {
	if o == nil || o.sdk == nil {
		return prices.Quote{}, errors.New("Azure spot price observer unavailable")
	}
	client := &prices.AzureSpotClient{HTTPClient: o.sdk.PricesHTTPClient, Endpoint: o.sdk.PricesEndpoint}
	return client.Observe(ctx, region, zone, instanceType)
}
