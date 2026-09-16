package provider

import (
	"context"
	"errors"

	"github.com/tsouza/runnerscout/internal/prices"
)

// GCPSkuPrices observes live Cloud Billing Catalog API prices for exact,
// already-pinned SKU IDs. Unlike AWSSpotPrices, it resolves no per-call
// credential scope: the Cloud Billing Catalog API authenticates with a
// single static API key, resolved once at GCPSDK construction (see
// internal/provider/gcp_credentials.go's newGCPSDK), not a per-region
// credential chain the way AWS provisioning/pricing share.
type GCPSkuPrices struct {
	sdk *GCPSDK
}

// SkuPrices returns a live price observer bound to this GCPSDK's billing
// API key. It performs no network calls itself, mirroring
// AWSSDK.SpotPrices/AzureSDK.SpotPrices.
func (s *GCPSDK) SkuPrices() *GCPSkuPrices {
	return &GCPSkuPrices{sdk: s}
}

func (o *GCPSkuPrices) Observe(ctx context.Context, coreSkuID, ramSkuID string, cpu, memoryMiB int) (prices.Quote, error) {
	if o == nil || o.sdk == nil || o.sdk.BillingAPIKey == "" {
		return prices.Quote{}, errors.New("GCP SKU price observer unavailable")
	}
	client := &prices.GCPSkuClient{APIKey: o.sdk.BillingAPIKey, HTTPClient: o.sdk.BillingHTTPClient, Endpoint: o.sdk.BillingEndpoint}
	return client.Observe(ctx, coreSkuID, ramSkuID, cpu, memoryMiB)
}
