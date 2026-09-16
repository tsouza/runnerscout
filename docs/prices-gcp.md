# GCP spot price observation

Part of [issue #2](https://github.com/tsouza/runnerscout/issues/2) and
[issue #143](https://github.com/tsouza/runnerscout/issues/143).

`internal/prices` observes live provider-driven spot prices for AWS
(`AWSSpotClient` in `aws.go`, via EC2's `DescribeSpotPriceHistory`, wired
through `internal/provider/aws_prices.go`'s `AWSSDK.SpotPrices()` and
`Operator.refreshAWSPrices`, gated by `Config.AWSPriceRefresh`) and for
Azure (`AzureSpotClient` in `azure.go`, via the Azure Retail Prices API,
wired through `internal/provider/azure_prices.go` and
`Operator.refreshAzurePrices`, gated by `Config.AzurePriceRefresh`).

GCP has a narrower equivalent: `GCPSkuClient` in `gcp.go`, via the Cloud
Billing Catalog API's `services.skus.list`, wired through
`internal/provider/gcp_prices.go`'s `GCPSDK.SkuPrices()` and
`Operator.refreshGCPPrices`, gated by `Config.GCPPriceRefresh`. Unlike the
AWS/Azure clients, `GCPSkuClient` never discovers which SKU corresponds to
a machine type - it only re-queries the current price of SKU IDs a human
has already identified and pinned onto the offering by hand (see
[prices-gcp.background.md](prices-gcp.background.md) for why a *generic*
machine-type-to-SKU mapping remains permanently out of scope).

## Pinning SKUs onto an offering

A `CapacityCatalog` offering optionally carries `gcpSkuRefs`, naming the
exact Cloud Billing Catalog SKU IDs for that offering's compute-core and
RAM charges:

```yaml
- id: gcp-spot
  provider: gcp
  machine: e2-medium
  cpu: 2
  memoryMiB: 4096
  gcpSkuRefs:
    coreSkuId: F179-E1EA-D97A
    ramSkuId: 9B1F-1E62-4061
```

These IDs are found by paginating `services.skus.list` for Compute Engine
(`services/6F81-5844-456A`) and matching on the exact `skuId` field for the
offering's machine family and region - never on free-text `description`.
This is a one-time, per-offering, human step; the controller never performs
it itself.

## What `Operator.refreshGCPPrices` does

With `Config.GCPPriceRefresh` enabled (in CRD mode: the catalog's
`priceRefresh.gcp`, described below), every offering with `Provider ==
"gcp"` and a non-nil `GCPSkuRefs` is re-priced on each admission cycle:

1. `GCPSkuClient.Observe` looks up the current price of exactly the pinned
   `coreSkuId` and `ramSkuId` - failing closed if either ID cannot be
   found, resolves ambiguously (more than one currently-effective price or
   pricing tier), reports a non-USD price, or has a category that does not
   look like the role it was pinned for (a `coreSkuId` that resolves to a
   RAM-priced SKU, or vice versa).
2. The offering's price is `coreUnitPrice * offering.CPU + ramUnitPrice *
   (offering.MemoryMiB / 1024)` - the offering's own already-declared `cpu`
   and `memoryMiB` fields, never a machine-type lookup or a hand-maintained
   shape table.
3. On success, the offering's `priceMicros`, `currency` and `observedAt`
   are replaced with the live observation, exactly like
   `refreshAWSPrices`/`refreshAzurePrices`. On failure, only `observedAt`
   is zeroed, so `internal/placement`'s freshness check excludes the
   offering from this cycle's admissions rather than reusing a stale price.

A GCP offering with no `gcpSkuRefs` is never touched, regardless of
`Config.GCPPriceRefresh` - there is no generic GCP live-price discovery this
flag opts an unrelated offering into.

Authentication is a static API key (`GCP_BILLING_API_KEY` in the `gcp`
provider's credential environment), separate from the OAuth2 credential VM
provisioning uses: Google documents the Cloud Billing Catalog API's public
SKU data as requiring an API key, not an OAuth2 access token.

## CRD-mode wiring

`CapacityCatalogSpec.priceRefresh` is the CRD-mode equivalent of the
mounted-JSON `Config.AWSPriceRefresh`/`AzurePriceRefresh`/`GCPPriceRefresh`
flags, keyed by provider name:

```yaml
spec:
  priceRefresh:
    aws: true
    azure: true
    gcp: true
```

`internal/configapi/compile.go` copies each entry directly onto the
compiled `operator.Config`. As with mounted-JSON mode, `gcp: true` alone
refreshes nothing without at least one offering carrying `gcpSkuRefs`.
