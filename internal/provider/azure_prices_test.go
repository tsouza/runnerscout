package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

const azureSpotPriceFixture = `{"BillingCurrency":"USD","Items":[{"currencyCode":"USD","retailPrice":0.018816,"unitPrice":0.018816,"armRegionName":"eastus","armSkuName":"Standard_D2s_v3","meterName":"D2s v3 Spot","productName":"Virtual Machines DSv3 Series","serviceName":"Virtual Machines","type":"Consumption"}],"NextPageLink":null,"Count":1}`

func TestAzureSpotPricesObservesWithoutAnyCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected Authorization header on unauthenticated Retail Prices API request")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, azureSpotPriceFixture)
	}))
	defer server.Close()
	sdk := &AzureSDK{PricesHTTPClient: server.Client(), PricesEndpoint: server.URL}
	quote, err := sdk.SpotPrices().Observe(context.Background(), "eastus", "1", "Standard_D2s_v3")
	if err != nil {
		t.Fatal(err)
	}
	if quote.PriceMicros != 18816 || quote.Currency != "USD" {
		t.Fatalf("unexpected quote: %+v", quote)
	}
}

func TestAzureSpotPricesRequiresConfiguredObserver(t *testing.T) {
	var observer *AzureSpotPrices
	if _, err := observer.Observe(context.Background(), "eastus", "1", "Standard_D2s_v3"); err == nil {
		t.Fatal("expected error for a nil observer")
	}
}

// Unlike AWSSDK, AzureSDK has no "unconfigured credential scope" state to
// reject: the Retail Prices API needs no credentials at all, so a
// zero-value AzureSDK's SpotPrices() is already fully usable (it would only
// ever fail for the same reasons AzureSpotClient.Observe fails on its own -
// an incomplete request or an unreachable/malformed response - exercised
// directly in internal/prices/azure_test.go). There is deliberately no
// "requires configured SDK" test here mirroring AWS's, since that
// precondition does not exist for this provider.
func TestAzureSpotPricesRequiresCompleteRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should reach the network for an incomplete request")
	}))
	defer server.Close()
	sdk := &AzureSDK{PricesHTTPClient: server.Client(), PricesEndpoint: server.URL}
	if _, err := sdk.SpotPrices().Observe(context.Background(), "", "1", "Standard_D2s_v3"); err == nil {
		t.Fatal("expected error for a request missing a region")
	}
}
