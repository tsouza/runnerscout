package prices

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixtureAzure is a minimal Azure Retail Prices API fixture. It always
// returns whatever raw JSON body is currently configured, regardless of the
// request's own $filter - real server-side filtering is Azure's concern,
// exercised live during development (see azure.go's header comment); this
// fixture instead lets each test hand back exactly the response shape real
// queries are known to produce, including the ambiguous ones.
type fixtureAzure struct {
	server     *httptest.Server
	mu         sync.Mutex
	status     int
	body       string
	lastFilter string
}

func newFixtureAzure(t *testing.T) *fixtureAzure {
	t.Helper()
	f := &fixtureAzure{status: http.StatusOK}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fixtureAzure) setResponse(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func (f *fixtureAzure) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastFilter = r.URL.Query().Get("$filter")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.status)
	fmt.Fprint(w, f.body)
}

func testAzureClient(f *fixtureAzure) *AzureSpotClient {
	return &AzureSpotClient{HTTPClient: f.server.Client(), Endpoint: f.server.URL}
}

func azureResponse(nextPage string, items ...string) string {
	next := "null"
	if nextPage != "" {
		next = `"` + nextPage + `"`
	}
	return fmt.Sprintf(`{"BillingCurrency":"USD","Items":[%s],"NextPageLink":%s,"Count":%d}`, strings.Join(items, ","), next, len(items))
}

const (
	linuxSpotItem   = `{"currencyCode":"USD","retailPrice":0.018816,"unitPrice":0.018816,"armRegionName":"eastus","armSkuName":"Standard_D2s_v3","meterName":"D2s v3 Spot","productName":"Virtual Machines DSv3 Series","serviceName":"Virtual Machines","type":"Consumption","effectiveStartDate":"2026-06-01T00:00:00Z"}`
	windowsSpotItem = `{"currencyCode":"USD","retailPrice":0.036848,"unitPrice":0.036848,"armRegionName":"eastus","armSkuName":"Standard_D2s_v3","meterName":"D2s v3 Spot","productName":"Virtual Machines DSv3 Series Windows","serviceName":"Virtual Machines","type":"Consumption","effectiveStartDate":"2026-06-01T00:00:00Z"}`
	onDemandItem    = `{"currencyCode":"USD","retailPrice":0.096,"unitPrice":0.096,"armRegionName":"eastus","armSkuName":"Standard_D2s_v3","meterName":"D2s v3","productName":"Virtual Machines DSv3 Series","serviceName":"Virtual Machines","type":"Consumption","effectiveStartDate":"2017-12-15T00:00:00Z"}`
)

func TestObserveMapsSuccessfulAzureSpotPrice(t *testing.T) {
	f := newFixtureAzure(t)
	// Realistic real-world shape: Azure's own Spot-meter filter still returns
	// both a Linux and a Windows-licensed row for the same armSkuName/region
	// (confirmed live against prices.azure.com for Standard_D2s_v3/eastus).
	f.setResponse(http.StatusOK, azureResponse("", linuxSpotItem, windowsSpotItem))
	before := time.Now()
	quote, err := testAzureClient(f).Observe(context.Background(), "eastus", "1", "Standard_D2s_v3")
	after := time.Now()
	if err != nil {
		t.Fatal(err)
	}
	if quote.PriceMicros != 18816 {
		t.Errorf("PriceMicros = %d, want 18816", quote.PriceMicros)
	}
	if quote.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", quote.Currency)
	}
	// effectiveStartDate (2026-06-01) must never be used as ObservedAt: it
	// marks when this pricing tier took effect, not when it was observed.
	if quote.ObservedAt.Before(before) || quote.ObservedAt.After(after) {
		t.Errorf("ObservedAt = %v, want between %v and %v (request time, not effectiveStartDate)", quote.ObservedAt, before, after)
	}
	if !strings.Contains(f.lastFilter, "Standard_D2s_v3") || !strings.Contains(f.lastFilter, "eastus") || !strings.Contains(f.lastFilter, "Spot") {
		t.Errorf("request filter %q missing expected fields", f.lastFilter)
	}
}

func TestObserveRejectsEmptyItems(t *testing.T) {
	f := newFixtureAzure(t)
	f.setResponse(http.StatusOK, azureResponse(""))
	if _, err := testAzureClient(f).Observe(context.Background(), "eastus", "1", "Standard_D2s_v3"); err == nil {
		t.Fatal("expected error for an empty item list")
	}
}

func TestObserveRejectsWindowsOnlyResponse(t *testing.T) {
	f := newFixtureAzure(t)
	// RunnerScout only ever provisions Linux VMs; a response carrying only
	// the Windows-licensed meter must never be mistaken for a Linux price.
	f.setResponse(http.StatusOK, azureResponse("", windowsSpotItem))
	if _, err := testAzureClient(f).Observe(context.Background(), "eastus", "1", "Standard_D2s_v3"); err == nil {
		t.Fatal("expected error when only the Windows-licensed meter is present")
	}
}

func TestObserveRejectsOnDemandOnlyResponse(t *testing.T) {
	f := newFixtureAzure(t)
	// Defense in depth against a broken or absent server-side Spot filter:
	// an on-demand-only response (no "Spot" in meterName) must never be
	// accepted as a Spot price.
	f.setResponse(http.StatusOK, azureResponse("", onDemandItem))
	if _, err := testAzureClient(f).Observe(context.Background(), "eastus", "1", "Standard_D2s_v3"); err == nil {
		t.Fatal("expected error when no returned entry is a Spot meter")
	}
}

func TestObserveRejectsMismatchedRegionOrSku(t *testing.T) {
	f := newFixtureAzure(t)
	f.setResponse(http.StatusOK, azureResponse("", linuxSpotItem))
	if _, err := testAzureClient(f).Observe(context.Background(), "westus", "1", "Standard_D2s_v3"); err == nil {
		t.Fatal("expected error when the returned entry's region does not match the request")
	}
	if _, err := testAzureClient(f).Observe(context.Background(), "eastus", "1", "Standard_D4s_v3"); err == nil {
		t.Fatal("expected error when the returned entry's SKU does not match the request")
	}
}

func TestObserveRejectsMultipleAmbiguousLinuxEntries(t *testing.T) {
	f := newFixtureAzure(t)
	other := `{"currencyCode":"USD","retailPrice":0.02,"unitPrice":0.02,"armRegionName":"eastus","armSkuName":"Standard_D2s_v3","meterName":"D2s v3 Spot","productName":"Virtual Machines DSv3 Series","serviceName":"Virtual Machines","type":"Consumption"}`
	f.setResponse(http.StatusOK, azureResponse("", linuxSpotItem, other))
	if _, err := testAzureClient(f).Observe(context.Background(), "eastus", "1", "Standard_D2s_v3"); err == nil {
		t.Fatal("expected error for an ambiguous multi-entry response, never averaged or first-picked")
	}
}

func TestObserveRejectsPaginatedResponse(t *testing.T) {
	f := newFixtureAzure(t)
	f.setResponse(http.StatusOK, azureResponse("https://prices.azure.com/api/retail/prices?$skip=1000", linuxSpotItem))
	if _, err := testAzureClient(f).Observe(context.Background(), "eastus", "1", "Standard_D2s_v3"); err == nil {
		t.Fatal("expected error when the response is paginated (more matches may exist)")
	}
}

func TestObserveRejectsMalformedJSON(t *testing.T) {
	f := newFixtureAzure(t)
	f.setResponse(http.StatusOK, `{not json`)
	if _, err := testAzureClient(f).Observe(context.Background(), "eastus", "1", "Standard_D2s_v3"); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestObserveRejectsMissingAzurePrice(t *testing.T) {
	f := newFixtureAzure(t)
	missing := `{"currencyCode":"USD","armRegionName":"eastus","armSkuName":"Standard_D2s_v3","meterName":"D2s v3 Spot","productName":"Virtual Machines DSv3 Series","serviceName":"Virtual Machines","type":"Consumption"}`
	f.setResponse(http.StatusOK, azureResponse("", missing))
	if _, err := testAzureClient(f).Observe(context.Background(), "eastus", "1", "Standard_D2s_v3"); err == nil {
		t.Fatal("expected error for a response missing a retail price")
	}
}

func TestObserveRejectsWrongCurrency(t *testing.T) {
	f := newFixtureAzure(t)
	eur := `{"currencyCode":"EUR","retailPrice":0.018816,"unitPrice":0.018816,"armRegionName":"eastus","armSkuName":"Standard_D2s_v3","meterName":"D2s v3 Spot","productName":"Virtual Machines DSv3 Series","serviceName":"Virtual Machines","type":"Consumption"}`
	f.setResponse(http.StatusOK, azureResponse("", eur))
	if _, err := testAzureClient(f).Observe(context.Background(), "eastus", "1", "Standard_D2s_v3"); err == nil {
		t.Fatal("expected error for a non-USD currency")
	}
}

func TestObserveRejectsNonOKAzureStatus(t *testing.T) {
	f := newFixtureAzure(t)
	f.setResponse(http.StatusBadRequest, `{"Error":{"Code":"BadRequest","Message":"fixture"}}`)
	if _, err := testAzureClient(f).Observe(context.Background(), "eastus", "1", "Standard_D2s_v3"); err == nil {
		t.Fatal("expected error for a non-200 response")
	}
}

func TestObserveRejectsMissingRegionOrInstanceType(t *testing.T) {
	f := newFixtureAzure(t)
	f.setResponse(http.StatusOK, azureResponse("", linuxSpotItem))
	cases := [][2]string{{"", "Standard_D2s_v3"}, {"eastus", ""}}
	for _, tc := range cases {
		if _, err := testAzureClient(f).Observe(context.Background(), tc[0], "1", tc[1]); err == nil {
			t.Fatalf("expected error for incomplete request %v", tc)
		}
	}
}

func TestObserveIgnoresZoneParameter(t *testing.T) {
	// Azure's Retail Prices API has no zone-level Spot pricing dimension
	// (confirmed live and via Microsoft's own field/filter documentation:
	// the only location field is armRegionName). The same region+SKU must
	// resolve identically regardless of which zone string is passed.
	f := newFixtureAzure(t)
	f.setResponse(http.StatusOK, azureResponse("", linuxSpotItem, windowsSpotItem))
	q1, err1 := testAzureClient(f).Observe(context.Background(), "eastus", "1", "Standard_D2s_v3")
	q2, err2 := testAzureClient(f).Observe(context.Background(), "eastus", "3", "Standard_D2s_v3")
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v %v", err1, err2)
	}
	if q1.PriceMicros != q2.PriceMicros {
		t.Fatalf("zone changed the resolved price: %+v vs %+v", q1, q2)
	}
}
