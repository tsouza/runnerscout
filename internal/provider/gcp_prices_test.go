package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

const gcpSkuPriceFixture = `{"skus":[{"skuId":"core-id","category":{"resourceGroup":"Core"},"pricingInfo":[{"pricingExpression":{"usageUnit":"h","tieredRates":[{"unitPrice":{"currencyCode":"USD","units":"0","nanos":31611000}}]}}]},{"skuId":"ram-id","category":{"resourceGroup":"RAM"},"pricingInfo":[{"pricingExpression":{"usageUnit":"GiBy.h","tieredRates":[{"unitPrice":{"currencyCode":"USD","units":"0","nanos":4237000}}]}}]}]}`

func TestGCPSkuPricesObservesUsingConfiguredBillingAPIKey(t *testing.T) {
	var gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("key")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, gcpSkuPriceFixture)
	}))
	defer server.Close()
	sdk := &GCPSDK{BillingAPIKey: "fixture-key", BillingHTTPClient: server.Client(), BillingEndpoint: server.URL}
	quote, err := sdk.SkuPrices().Observe(context.Background(), "core-id", "ram-id", 2, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if quote.PriceMicros != 80170 || quote.Currency != "USD" {
		t.Fatalf("unexpected quote: %+v", quote)
	}
	if gotKey != "fixture-key" {
		t.Fatalf("billing API key not forwarded to the Cloud Billing Catalog API request: got %q", gotKey)
	}
}

func TestGCPSkuPricesRequiresConfiguredObserver(t *testing.T) {
	var observer *GCPSkuPrices
	if _, err := observer.Observe(context.Background(), "core-id", "ram-id", 2, 4096); err == nil {
		t.Fatal("expected error for a nil observer")
	}
	if _, err := (&GCPSDK{}).SkuPrices().Observe(context.Background(), "core-id", "ram-id", 2, 4096); err == nil {
		t.Fatal("expected error when the SDK has no billing API key configured")
	}
}

func TestGCPSkuPricesRequiresConfiguredBillingAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should reach the network without a billing API key")
	}))
	defer server.Close()
	sdk := &GCPSDK{BillingHTTPClient: server.Client(), BillingEndpoint: server.URL}
	if _, err := sdk.SkuPrices().Observe(context.Background(), "core-id", "ram-id", 2, 4096); err == nil {
		t.Fatal("expected error when no billing API key is configured")
	}
}
