package prices

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// gcpBillingEndpoint is the Cloud Billing Catalog API v1
// (https://docs.cloud.google.com/billing/docs/reference/rest/v1/services.skus/list),
// confirmed via Google's own published reference documentation during
// development (this endpoint has no unauthenticated or SDK-free
// alternative the way Azure's Retail Prices API does - see
// GCPSkuClient's own doc comment for the API key it requires).
const gcpBillingEndpoint = "https://cloudbilling.googleapis.com/v1"

// gcpComputeEngineService is Compute Engine's fixed, publicly documented
// Cloud Billing Catalog service ID - the same constant Google's own sample
// code and every third-party integration against this API uses. It never
// varies per project or region: the Catalog API's "service" resource names
// a Google product line, not a caller's own GCP project.
const gcpComputeEngineService = "services/6F81-5844-456A"

// gcpSkuPageSize keeps each page's response body to a manageable size (the
// Compute Engine service publishes thousands of SKUs across every family,
// tier and region - see docs/prices-gcp.background.md point 4: there is no
// server-side filter to narrow this beyond a page size and token).
const gcpSkuPageSize = 500

// gcpMaxSkuPages bounds how many pages findSkus will ever scan before
// failing closed. It guards against an unbounded or misbehaving paginated
// response looping this call forever; the full Compute Engine catalog is
// not observed to need anywhere near gcpMaxSkuPages*gcpSkuPageSize entries.
const gcpMaxSkuPages = 64

// gcpCoreUsageUnit and gcpRamUsageUnit are the Cloud Billing Catalog API's
// documented usage-unit shorthand for Compute Engine's "Core" (vCPU) and
// "RAM" (memory) resource groups respectively - per-vCPU-hour and
// per-GiB-hour, the same convention Google's own public SKU-groups page
// (cloud.google.com/skus/sku-groups/spot-preemptible-n1-vms, cited in
// docs/prices-gcp.background.md) prices Core/RAM SKUs by. A SKU reporting
// any other usage unit is rejected rather than combined - see
// gcpSkuUnitPrice.
const (
	gcpCoreUsageUnit = "h"
	gcpRamUsageUnit  = "GiBy.h"
)

// GCPSkuClient observes the current price of exact, already-known Cloud
// Billing Catalog SKU IDs - never a machine-type-to-SKU mapping (see
// docs/prices-gcp.background.md for why a *generic* GCP price client was
// rejected). It never returns a stale or synthesized price: an empty,
// ambiguous or otherwise incomplete response is always an error, mirroring
// AWSSpotClient/AzureSpotClient's own defensive style.
//
// Unlike AWSSpotClient (signed SDK credentials) and AzureSpotClient (no
// credentials at all), this client authenticates with a single static API
// key: Google's own documentation for this endpoint states public SKU data
// requires an API key, not an OAuth2 access token
// (https://docs.cloud.google.com/billing/docs/how-to/get-pricing-information-api).
type GCPSkuClient struct {
	APIKey     string
	HTTPClient *http.Client
	Endpoint   string // Set only by local API fixtures, never from the environment.
}

func (*GCPSkuClient) String() string   { return "GCP SKU price client (credentials redacted)" }
func (*GCPSkuClient) GoString() string { return "GCP SKU price client (credentials redacted)" }

type gcpSkuMoney struct {
	CurrencyCode string `json:"currencyCode"`
	Units        string `json:"units"`
	Nanos        int64  `json:"nanos"`
}
type gcpTierRate struct {
	UnitPrice gcpSkuMoney `json:"unitPrice"`
}
type gcpPricingExpression struct {
	UsageUnit   string        `json:"usageUnit"`
	TieredRates []gcpTierRate `json:"tieredRates"`
}
type gcpPricingInfo struct {
	PricingExpression gcpPricingExpression `json:"pricingExpression"`
}
type gcpCategory struct {
	ResourceGroup string `json:"resourceGroup"`
}
type gcpSku struct {
	SkuID       string           `json:"skuId"`
	Category    gcpCategory      `json:"category"`
	PricingInfo []gcpPricingInfo `json:"pricingInfo"`
}
type gcpSkuListResponse struct {
	Skus          []gcpSku `json:"skus"`
	NextPageToken string   `json:"nextPageToken"`
}

// Observe returns the current combined price of one offering's pinned core
// and RAM SKUs: coreUnitPrice*cpu + ramUnitPrice*(memoryMiB/1024). cpu and
// memoryMiB are the offering's own already-declared catalog fields (see
// placement.Offering.CPU/MemoryMiB), never inferred or looked up here -
// combining a per-vCPU price and a per-GiB price with counts the catalog
// author already supplied is arithmetic on known values, not a guess about
// which SKU applies to which machine type (see
// docs/prices-gcp.background.md point 5, which this deliberately avoids
// reintroducing).
func (c *GCPSkuClient) Observe(ctx context.Context, coreSkuID, ramSkuID string, cpu, memoryMiB int) (Quote, error) {
	if c == nil || c.APIKey == "" {
		return Quote{}, errors.New("GCP SKU price client configuration unavailable")
	}
	if coreSkuID == "" || ramSkuID == "" || coreSkuID == ramSkuID {
		return Quote{}, errors.New("GCP SKU price request incomplete or ambiguous")
	}
	if cpu < 1 || memoryMiB < 1 {
		return Quote{}, errors.New("GCP SKU price request incomplete")
	}
	found, err := c.findSkus(ctx, map[string]bool{coreSkuID: true, ramSkuID: true})
	if err != nil {
		return Quote{}, err
	}
	core, ok := found[coreSkuID]
	if !ok {
		return Quote{}, errors.New("GCP SKU price observation has no entry for the pinned core SKU")
	}
	ram, ok := found[ramSkuID]
	if !ok {
		return Quote{}, errors.New("GCP SKU price observation has no entry for the pinned RAM SKU")
	}
	coreDollars, err := gcpSkuUnitPrice(core, gcpCoreUsageUnit)
	if err != nil {
		return Quote{}, fmt.Errorf("pinned core SKU: %w", err)
	}
	ramDollars, err := gcpSkuUnitPrice(ram, gcpRamUsageUnit)
	if err != nil {
		return Quote{}, fmt.Errorf("pinned RAM SKU: %w", err)
	}
	total := coreDollars*float64(cpu) + ramDollars*(float64(memoryMiB)/1024.0)
	if total <= 0 {
		return Quote{}, errors.New("GCP SKU price value invalid")
	}
	// Neither pinned SKU's pricingInfo effectiveTime is used as ObservedAt,
	// for the exact reason AWSSpotClient/AzureSpotClient's own Observe
	// comments give for their equivalent timestamps: it marks when this
	// price took effect, not when it was observed. ObservedAt is therefore
	// this successful request's own completion time.
	return Quote{PriceMicros: int64(math.Round(total * 1e6)), Currency: "USD", ObservedAt: time.Now()}, nil
}

// findSkus scans services.skus.list pages (there is no server-side filter
// by skuId - see docs/prices-gcp.background.md point 4) until every ID in
// want is located or gcpMaxSkuPages is exhausted, whichever comes first.
func (c *GCPSkuClient) findSkus(ctx context.Context, want map[string]bool) (map[string]gcpSku, error) {
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	found := map[string]gcpSku{}
	pageToken := ""
	for page := 0; page < gcpMaxSkuPages; page++ {
		request, err := c.request(ctx, pageToken)
		if err != nil {
			return nil, err
		}
		response, err := httpClient.Do(request)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil || response == nil {
			return nil, errors.New("GCP SKU list observation unavailable")
		}
		envelope, err := gcpDecodeSkuPage(response)
		if err != nil {
			return nil, err
		}
		for _, sku := range envelope.Skus {
			if !want[sku.SkuID] {
				continue
			}
			if _, duplicate := found[sku.SkuID]; duplicate {
				return nil, errors.New("GCP SKU list returned a duplicate entry for a requested SKU")
			}
			found[sku.SkuID] = sku
		}
		if len(found) == len(want) {
			return found, nil
		}
		if envelope.NextPageToken == "" {
			return found, nil
		}
		pageToken = envelope.NextPageToken
	}
	return nil, errors.New("GCP SKU list pagination exceeded without locating every requested SKU")
}

func gcpDecodeSkuPage(response *http.Response) (gcpSkuListResponse, error) {
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return gcpSkuListResponse{}, errors.New("GCP SKU list observation unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return gcpSkuListResponse{}, errors.New("GCP SKU list response unavailable or excessive")
	}
	var envelope gcpSkuListResponse
	if json.NewDecoder(bytes.NewReader(data)).Decode(&envelope) != nil {
		return gcpSkuListResponse{}, errors.New("GCP SKU list response malformed")
	}
	return envelope, nil
}

func (c *GCPSkuClient) request(ctx context.Context, pageToken string) (*http.Request, error) {
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = gcpBillingEndpoint
	}
	parsed, err := url.Parse(strings.TrimRight(endpoint, "/") + "/" + gcpComputeEngineService + "/skus")
	if err != nil {
		return nil, errors.New("GCP SKU price endpoint invalid")
	}
	query := parsed.Query()
	query.Set("key", c.APIKey)
	query.Set("currencyCode", "USD")
	query.Set("pageSize", strconv.Itoa(gcpSkuPageSize))
	if pageToken != "" {
		query.Set("pageToken", pageToken)
	}
	parsed.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, errors.New("GCP SKU price request invalid")
	}
	request.Header.Set("Accept", "application/json")
	return request, nil
}

// gcpSkuUnitPrice extracts sku's single, currently-effective, USD unit
// price and re-verifies it actually looks like the role it was pinned for
// by checking wantUsageUnit ("h" for a core SKU, "GiBy.h" for a RAM SKU) -
// catching the real, human-scale failure mode this design has to guard
// against: an operator swapping which ID they pasted into coreSkuId vs
// ramSkuId. A swapped pin resolves to the other role's SKU, whose usage
// unit never matches what was asked for, regardless of category.
// category.resourceGroup is deliberately NOT used for this check: for
// standard machine families (N1, E2, ...) both the Core and RAM SKUs of a
// family share the family name as resourceGroup (e.g. "N1Standard"), not
// "Core"/"RAM" - a resourceGroup-substring check would reject correctly
// pinned SKUs for those families. It never infers which machine type the
// SKU belongs to; that remains entirely the pinning operator's own,
// already-completed verification.
func gcpSkuUnitPrice(sku gcpSku, wantUsageUnit string) (float64, error) {
	if len(sku.PricingInfo) != 1 {
		return 0, errors.New("pricing ambiguous: expected exactly one currently effective price")
	}
	expr := sku.PricingInfo[0].PricingExpression
	if expr.UsageUnit != wantUsageUnit {
		return 0, fmt.Errorf("usage unit %q unexpected, want %q", expr.UsageUnit, wantUsageUnit)
	}
	if len(expr.TieredRates) != 1 {
		return 0, errors.New("pricing ambiguous: expected exactly one pricing tier")
	}
	money := expr.TieredRates[0].UnitPrice
	if !strings.EqualFold(money.CurrencyCode, "USD") {
		return 0, errors.New("price currency unexpected")
	}
	units, err := strconv.ParseInt(money.Units, 10, 64)
	if err != nil {
		return 0, errors.New("price value invalid")
	}
	dollars := float64(units) + float64(money.Nanos)/1e9
	if dollars <= 0 {
		return 0, errors.New("price value invalid")
	}
	return dollars, nil
}
