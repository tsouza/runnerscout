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
	"strings"
	"time"
)

// azureRetailPricesEndpoint is Azure's public, unauthenticated Retail Prices
// API (https://learn.microsoft.com/en-us/rest/api/cost-management/retail-prices/azure-retail-prices),
// confirmed live during development against real Standard_D2s_v3/eastus
// queries. Unlike AWS's DescribeSpotPriceHistory, it needs no credentials,
// no signing and no SDK module at all - a plain HTTPS GET with an OData
// "$filter" query string.
const azureRetailPricesEndpoint = "https://prices.azure.com/api/retail/prices"

// AzureSpotClient observes Azure Virtual Machine Spot prices via the Retail
// Prices API. It never returns a stale or synthesized price: an empty,
// ambiguous or otherwise incomplete response is always an error, mirroring
// AWSSpotClient's own defensive style.
type AzureSpotClient struct {
	HTTPClient *http.Client
	Endpoint   string // Set only by local API fixtures, never from the environment.
}

// azureRetailPriceItem mirrors the fields of one Retail Prices API "Items"
// entry that this client actually inspects. Unrecognized fields (there are
// several more - tierMinimumUnits, unitOfMeasure, meterId, and so on) are
// intentionally left undecoded; adding a filter dimension later only needs a
// field added here, never a schema rewrite.
type azureRetailPriceItem struct {
	CurrencyCode  string  `json:"currencyCode"`
	RetailPrice   float64 `json:"retailPrice"`
	ArmRegionName string  `json:"armRegionName"`
	ArmSkuName    string  `json:"armSkuName"`
	MeterName     string  `json:"meterName"`
	ProductName   string  `json:"productName"`
	ServiceName   string  `json:"serviceName"`
	Type          string  `json:"type"`
}

// azureRetailPriceResponse mirrors the Retail Prices API's response
// envelope. NextPageLink is checked (never followed): a paginated response
// means more potentially-matching rows exist beyond this page, which this
// client must treat as ambiguous rather than silently accepting page one.
type azureRetailPriceResponse struct {
	Items        []azureRetailPriceItem `json:"Items"`
	NextPageLink *string                `json:"NextPageLink"`
}

// Observe returns the current Linux Spot price for one Azure VM size in one
// region. zone is accepted only to match the shared price-observer
// interface (internal/operator's awsPriceObserver/azurePriceObserver
// shape) and is otherwise unused: the Retail Prices API's documented
// filterable and returned fields have no zone concept at all, only
// armRegionName - confirmed both live (identical region+SKU queries return
// identical results regardless of any zone) and against Microsoft's own
// field/filter tables at the URL above. Azure has no meaningful zone-level
// Spot pricing surface to query.
func (c *AzureSpotClient) Observe(ctx context.Context, region, zone string, instanceType string) (Quote, error) {
	_ = zone
	if region == "" || instanceType == "" {
		return Quote{}, errors.New("Azure spot price request incomplete")
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	request, err := c.request(ctx, region, instanceType)
	if err != nil {
		return Quote{}, err
	}
	response, err := httpClient.Do(request)
	if ctx.Err() != nil {
		return Quote{}, ctx.Err()
	}
	if err != nil || response == nil {
		return Quote{}, errors.New("Azure spot price observation unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Quote{}, errors.New("Azure spot price observation unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return Quote{}, errors.New("Azure spot price response unavailable or excessive")
	}
	var envelope azureRetailPriceResponse
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&envelope); err != nil {
		return Quote{}, errors.New("Azure spot price response malformed")
	}
	if envelope.NextPageLink != nil && *envelope.NextPageLink != "" {
		return Quote{}, errors.New("Azure spot price response paginated; ambiguous match")
	}
	match, err := azureUnambiguousSpotMatch(envelope.Items, region, instanceType)
	if err != nil {
		return Quote{}, err
	}
	if !strings.EqualFold(match.CurrencyCode, "USD") {
		return Quote{}, errors.New("Azure spot price currency unexpected")
	}
	if match.RetailPrice <= 0 {
		return Quote{}, errors.New("Azure spot price value invalid")
	}
	// effectiveStartDate marks when this pricing tier took effect, not when
	// it was observed: confirmed live, on-demand Standard_D2s_v3/eastus
	// carries effectiveStartDate 2017-12-15 and its Spot sibling
	// 2026-06-01, yet both are the live, currently-billed price today. The
	// API documents effectiveStartDate itself only as "the date when the
	// retail prices are effective" - never as an observation or freshness
	// timestamp. Using it as ObservedAt would let a months- or years-old
	// date silently mark a just-fetched, fully current price as stale.
	// ObservedAt is therefore this successful request's own completion
	// time.
	return Quote{
		PriceMicros: azureRetailPriceMicros(match.RetailPrice),
		Currency:    "USD",
		ObservedAt:  time.Now(),
	}, nil
}

// request builds the Retail Prices API GET request for one region+SKU's
// unexpired Spot meter. serviceName/priceType/armRegionName/armSkuName are
// exact `eq` matches; the Spot meter itself is selected with
// contains(meterName, 'Spot') - Azure's own documented Retail Prices API
// sample code (see the URL on azureRetailPricesEndpoint) uses the identical
// "contains(meterName, 'Spot')" clause to find Spot VM pricing, since Spot
// is a meter variant of an otherwise-identical SKU rather than a separate
// filterable field. currencyCode=USD is requested explicitly (not just
// relied on as the default) so a misconfigured endpoint can never silently
// return a different currency's numbers.
func (c *AzureSpotClient) request(ctx context.Context, region, instanceType string) (*http.Request, error) {
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = azureRetailPricesEndpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("Azure spot price endpoint invalid")
	}
	filter := fmt.Sprintf(
		"serviceName eq 'Virtual Machines' and priceType eq 'Consumption' and armRegionName eq '%s' and armSkuName eq '%s' and contains(meterName, 'Spot')",
		azureODataLiteral(region), azureODataLiteral(instanceType),
	)
	query := parsed.Query()
	query.Set("currencyCode", "USD")
	query.Set("$filter", filter)
	parsed.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, errors.New("Azure spot price request invalid")
	}
	request.Header.Set("Accept", "application/json")
	return request, nil
}

// azureODataLiteral escapes a value for use inside a single-quoted OData
// string literal (the standard OData escaping rule: a literal single quote
// is doubled). Region names and ARM SKU names never legitimately contain a
// quote, but the request is built from caller-supplied strings, so this is
// defensive rather than assumed.
func azureODataLiteral(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

// azureUnambiguousSpotMatch narrows a Retail Prices API response down to
// exactly one Linux Spot entry for the requested region+SKU, or fails
// closed. It re-verifies every dimension the outgoing request's $filter
// already asked for (region, SKU, service, consumption type, "Spot" meter)
// rather than trusting the server applied them, mirroring
// AWSSpotClient.Observe's own re-verification of AvailabilityZone,
// InstanceType and ProductDescription on its response.
//
// It additionally excludes any entry whose productName contains "Windows":
// RunnerScout only ever provisions Linux Azure VMs (see
// internal/provider/azure.go's linuxConfiguration-only deployment
// template), but the Retail Prices API returns a separate, differently
// priced Windows-licensed meter for the exact same armSkuName, region and
// "D2s v3 Spot"-style meterName - distinguishable only by productName
// (confirmed live: Standard_D2s_v3/eastus returns both "Virtual Machines
// DSv3 Series" and "Virtual Machines DSv3 Series Windows" for one Spot
// query). An OData "not contains(productName, 'Windows')" filter would
// exclude this server-side, but the API rejects that clause outright with
// an HTTP 400 "Invalid OData parameters supplied" (confirmed live), so this
// exclusion happens here instead.
//
// Never averages or picks the first of multiple remaining matches: more
// than one is treated exactly like zero - an error.
func azureUnambiguousSpotMatch(items []azureRetailPriceItem, region, instanceType string) (azureRetailPriceItem, error) {
	var match *azureRetailPriceItem
	for i := range items {
		item := items[i]
		if item.ServiceName != "Virtual Machines" || item.Type != "Consumption" {
			continue
		}
		if item.ArmRegionName != region || item.ArmSkuName != instanceType {
			continue
		}
		if !strings.Contains(item.MeterName, "Spot") {
			continue
		}
		if strings.Contains(item.ProductName, "Windows") {
			continue
		}
		if match != nil {
			return azureRetailPriceItem{}, errors.New("Azure spot price observation ambiguous for the requested pool")
		}
		found := item
		match = &found
	}
	if match == nil {
		return azureRetailPriceItem{}, errors.New("Azure spot price observation has no entry for the requested pool")
	}
	return *match, nil
}

// Azure retail prices are published with at most a handful of decimal
// digits (e.g. 0.018816), which a float64 represents exactly at this
// magnitude, so parsing then scaling to micros never loses precision -
// exactly AWS's own awsSpotPriceMicros reasoning.
func azureRetailPriceMicros(value float64) int64 {
	return int64(math.Round(value * 1e6))
}
