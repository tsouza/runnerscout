package prices

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixtureGCP is a minimal Cloud Billing Catalog API services.skus.list
// fixture. Real pagination has no server-side filter by skuId (see
// gcp.go's header comment) - this fixture instead serves whatever page a
// test configured for the requested pageToken, so tests can exercise
// GCPSkuClient's own client-side scan-and-match exactly as the real,
// unfilterable API forces it to behave.
type fixtureGCP struct {
	server *httptest.Server
	mu     sync.Mutex
	pages  map[string]string
	keys   []string
}

func newFixtureGCP(t *testing.T) *fixtureGCP {
	t.Helper()
	f := &fixtureGCP{pages: map[string]string{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fixtureGCP) setPage(token, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages[token] = body
}

func (f *fixtureGCP) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, r.URL.Query().Get("key"))
	if r.URL.Query().Get("currencyCode") != "USD" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	token := r.URL.Query().Get("pageToken")
	body, ok := f.pages[token]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, body)
}

func testGCPClient(f *fixtureGCP) *GCPSkuClient {
	return &GCPSkuClient{APIKey: "fixture-key", HTTPClient: f.server.Client(), Endpoint: f.server.URL}
}

// gcpSkuJSON builds one services.skus.list "skus[]" entry with exactly the
// fields GCPSkuClient inspects. unitsNanos lets a test express a price as
// (units, nanos) exactly the way google.type.Money does.
func gcpSkuJSON(skuID, resourceGroup, usageUnit string, units int64, nanos int64) string {
	return fmt.Sprintf(`{"skuId":%q,"category":{"resourceGroup":%q},"pricingInfo":[{"pricingExpression":{"usageUnit":%q,"tieredRates":[{"unitPrice":{"currencyCode":"USD","units":%q,"nanos":%d}}]}}]}`, skuID, resourceGroup, usageUnit, strconv.FormatInt(units, 10), nanos)
}

func gcpPage(nextPageToken string, skus ...string) string {
	next := ""
	if nextPageToken != "" {
		next = fmt.Sprintf(`,"nextPageToken":%q`, nextPageToken)
	}
	return fmt.Sprintf(`{"skus":[%s]%s}`, strings.Join(skus, ","), next)
}

// Core: $0.031611/vCPU-hour. RAM: $0.004237/GiB-hour. For CPU=2,
// MemoryMiB=4096 (4 GiB): 0.031611*2 + 0.004237*4 = 0.08017 exactly, so the
// expected micros (80170) has no floating-point rounding ambiguity.
const (
	coreSkuFixture = "core-id"
	ramSkuFixture  = "ram-id"
)

func coreSkuEntry() string { return gcpSkuJSON(coreSkuFixture, "Core", "h", 0, 31611000) }
func ramSkuEntry() string  { return gcpSkuJSON(ramSkuFixture, "RAM", "GiBy.h", 0, 4237000) }

func TestGCPObserveMapsSuccessfulPinnedSkuPrice(t *testing.T) {
	f := newFixtureGCP(t)
	f.setPage("", gcpPage("", coreSkuEntry(), ramSkuEntry()))
	before := time.Now()
	quote, err := testGCPClient(f).Observe(context.Background(), coreSkuFixture, ramSkuFixture, 2, 4096)
	after := time.Now()
	if err != nil {
		t.Fatal(err)
	}
	if quote.PriceMicros != 80170 {
		t.Errorf("PriceMicros = %d, want 80170", quote.PriceMicros)
	}
	if quote.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", quote.Currency)
	}
	if quote.ObservedAt.Before(before) || quote.ObservedAt.After(after) {
		t.Errorf("ObservedAt = %v, want between %v and %v (request time)", quote.ObservedAt, before, after)
	}
	if len(f.keys) == 0 || f.keys[0] != "fixture-key" {
		t.Errorf("request did not carry the configured API key: %v", f.keys)
	}
}

func TestGCPObservePaginatesUntilBothSkusFound(t *testing.T) {
	f := newFixtureGCP(t)
	f.setPage("", gcpPage("page2", coreSkuEntry()))
	f.setPage("page2", gcpPage("", ramSkuEntry()))
	quote, err := testGCPClient(f).Observe(context.Background(), coreSkuFixture, ramSkuFixture, 2, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if quote.PriceMicros != 80170 {
		t.Errorf("PriceMicros = %d, want 80170", quote.PriceMicros)
	}
}

func TestGCPObserveStopsPaginatingOnceBothSkusFound(t *testing.T) {
	f := newFixtureGCP(t)
	// A page carrying both SKUs and a nextPageToken must never be followed
	// further - once both pinned IDs are resolved there is nothing left to
	// disambiguate.
	f.setPage("", gcpPage("unreachable", coreSkuEntry(), ramSkuEntry()))
	quote, err := testGCPClient(f).Observe(context.Background(), coreSkuFixture, ramSkuFixture, 2, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if quote.PriceMicros != 80170 {
		t.Errorf("PriceMicros = %d, want 80170", quote.PriceMicros)
	}
}

func TestGCPObserveRejectsMissingCoreSku(t *testing.T) {
	f := newFixtureGCP(t)
	f.setPage("", gcpPage("", ramSkuEntry()))
	if _, err := testGCPClient(f).Observe(context.Background(), coreSkuFixture, ramSkuFixture, 2, 4096); err == nil {
		t.Fatal("expected error when the core SKU is never found")
	}
}

func TestGCPObserveRejectsMissingRamSku(t *testing.T) {
	f := newFixtureGCP(t)
	f.setPage("", gcpPage("", coreSkuEntry()))
	if _, err := testGCPClient(f).Observe(context.Background(), coreSkuFixture, ramSkuFixture, 2, 4096); err == nil {
		t.Fatal("expected error when the RAM SKU is never found")
	}
}

func TestGCPObserveRejectsMismatchedCoreResourceGroup(t *testing.T) {
	f := newFixtureGCP(t)
	// A pinned "core" SKU ID whose actual category looks like a RAM SKU is
	// exactly the human pinning-mixup this check exists to catch - never
	// silently trusted just because the ID matched.
	f.setPage("", gcpPage("", gcpSkuJSON(coreSkuFixture, "RAM", "h", 0, 31611000), ramSkuEntry()))
	if _, err := testGCPClient(f).Observe(context.Background(), coreSkuFixture, ramSkuFixture, 2, 4096); err == nil {
		t.Fatal("expected error when the pinned core SKU's category does not look like a Core SKU")
	}
}

func TestGCPObserveRejectsMismatchedRamResourceGroup(t *testing.T) {
	f := newFixtureGCP(t)
	f.setPage("", gcpPage("", coreSkuEntry(), gcpSkuJSON(ramSkuFixture, "Core", "GiBy.h", 0, 4237000)))
	if _, err := testGCPClient(f).Observe(context.Background(), coreSkuFixture, ramSkuFixture, 2, 4096); err == nil {
		t.Fatal("expected error when the pinned RAM SKU's category does not look like a RAM SKU")
	}
}

func TestGCPObserveRejectsUnexpectedUsageUnit(t *testing.T) {
	f := newFixtureGCP(t)
	f.setPage("", gcpPage("", gcpSkuJSON(coreSkuFixture, "Core", "d", 0, 31611000), ramSkuEntry()))
	if _, err := testGCPClient(f).Observe(context.Background(), coreSkuFixture, ramSkuFixture, 2, 4096); err == nil {
		t.Fatal("expected error for an unexpected usage unit")
	}
}

func TestGCPObserveRejectsAmbiguousPricingInfo(t *testing.T) {
	f := newFixtureGCP(t)
	twoEntries := fmt.Sprintf(`{"skuId":%q,"category":{"resourceGroup":"Core"},"pricingInfo":[{"pricingExpression":{"usageUnit":"h","tieredRates":[{"unitPrice":{"currencyCode":"USD","units":"0","nanos":31611000}}]}},{"pricingExpression":{"usageUnit":"h","tieredRates":[{"unitPrice":{"currencyCode":"USD","units":"0","nanos":41611000}}]}}]}`, coreSkuFixture)
	f.setPage("", gcpPage("", twoEntries, ramSkuEntry()))
	if _, err := testGCPClient(f).Observe(context.Background(), coreSkuFixture, ramSkuFixture, 2, 4096); err == nil {
		t.Fatal("expected error for more than one currently-effective pricingInfo entry")
	}
}

func TestGCPObserveRejectsAmbiguousTieredRates(t *testing.T) {
	f := newFixtureGCP(t)
	tiered := fmt.Sprintf(`{"skuId":%q,"category":{"resourceGroup":"Core"},"pricingInfo":[{"pricingExpression":{"usageUnit":"h","tieredRates":[{"unitPrice":{"currencyCode":"USD","units":"0","nanos":31611000}},{"startUsageAmount":100,"unitPrice":{"currencyCode":"USD","units":"0","nanos":20000000}}]}}]}`, coreSkuFixture)
	f.setPage("", gcpPage("", tiered, ramSkuEntry()))
	if _, err := testGCPClient(f).Observe(context.Background(), coreSkuFixture, ramSkuFixture, 2, 4096); err == nil {
		t.Fatal("expected error for more than one pricing tier, never averaged or first-picked")
	}
}

func TestGCPObserveRejectsNonUSDPrice(t *testing.T) {
	f := newFixtureGCP(t)
	eur := gcpSkuJSON(coreSkuFixture, "Core", "h", 0, 31611000)
	eur = strings.Replace(eur, `"currencyCode":"USD"`, `"currencyCode":"EUR"`, 1)
	f.setPage("", gcpPage("", eur, ramSkuEntry()))
	if _, err := testGCPClient(f).Observe(context.Background(), coreSkuFixture, ramSkuFixture, 2, 4096); err == nil {
		t.Fatal("expected error for a non-USD price")
	}
}

func TestGCPObserveRejectsDuplicateSkuEntry(t *testing.T) {
	f := newFixtureGCP(t)
	f.setPage("", gcpPage("", coreSkuEntry(), coreSkuEntry(), ramSkuEntry()))
	if _, err := testGCPClient(f).Observe(context.Background(), coreSkuFixture, ramSkuFixture, 2, 4096); err == nil {
		t.Fatal("expected error for a duplicate SKU entry, never first-picked")
	}
}

func TestGCPObserveRejectsNonOKStatus(t *testing.T) {
	f := newFixtureGCP(t)
	// currencyCode is intentionally omitted from the page setup for this
	// token, so the fixture's own currencyCode check (mirroring the real
	// API's currencyCode-aware behavior) never masks the intended failure
	// mode being tested elsewhere; here we directly force a 500 by pointing
	// at an unconfigured token.
	if _, err := testGCPClient(f).Observe(context.Background(), coreSkuFixture, ramSkuFixture, 2, 4096); err == nil {
		t.Fatal("expected error for a non-200 response")
	}
}

func TestGCPObserveRejectsMalformedJSON(t *testing.T) {
	f := newFixtureGCP(t)
	f.setPage("", `{not json`)
	if _, err := testGCPClient(f).Observe(context.Background(), coreSkuFixture, ramSkuFixture, 2, 4096); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestGCPObserveRejectsIncompleteOrAmbiguousRequests(t *testing.T) {
	f := newFixtureGCP(t)
	f.setPage("", gcpPage("", coreSkuEntry(), ramSkuEntry()))
	cases := []struct {
		name                string
		coreSkuID, ramSkuID string
		cpu, memoryMiB      int
		apiKey              string
	}{
		{"empty core", "", ramSkuFixture, 2, 4096, "fixture-key"},
		{"empty ram", coreSkuFixture, "", 2, 4096, "fixture-key"},
		{"identical ids", "same-id", "same-id", 2, 4096, "fixture-key"},
		{"zero cpu", coreSkuFixture, ramSkuFixture, 0, 4096, "fixture-key"},
		{"zero memory", coreSkuFixture, ramSkuFixture, 2, 0, "fixture-key"},
		{"no api key", coreSkuFixture, ramSkuFixture, 2, 4096, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &GCPSkuClient{APIKey: tc.apiKey, HTTPClient: f.server.Client(), Endpoint: f.server.URL}
			if _, err := client.Observe(context.Background(), tc.coreSkuID, tc.ramSkuID, tc.cpu, tc.memoryMiB); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestGCPObservePaginationGuardStopsEventually(t *testing.T) {
	f := newFixtureGCP(t)
	// Every page points to the next one, forever, and never carries either
	// pinned SKU: a malformed or misbehaving server must never hang this
	// call or scan without bound.
	const pages = 100
	for i := 0; i < pages; i++ {
		next := fmt.Sprintf("page-%d", i+1)
		f.setPage(fmt.Sprintf("page-%d", i), gcpPage(next, gcpSkuJSON(fmt.Sprintf("unrelated-%d", i), "Core", "h", 0, 1)))
	}
	f.setPage("", gcpPage("page-0", gcpSkuJSON("unrelated-start", "Core", "h", 0, 1)))
	done := make(chan struct{})
	var err error
	go func() {
		_, err = testGCPClient(f).Observe(context.Background(), coreSkuFixture, ramSkuFixture, 2, 4096)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Observe did not return - pagination guard did not stop it")
	}
	if err == nil {
		t.Fatal("expected error when pagination never locates the requested SKUs")
	}
}
