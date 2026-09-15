# GCP spot price observation

Part of [issue #2](https://github.com/tsouza/runnerscout/issues/2).

`internal/prices` observes live provider-driven spot prices for AWS
(`AWSSpotClient` in `aws.go`, via EC2's `DescribeSpotPriceHistory`, wired
through `internal/provider/aws_prices.go`'s `AWSSDK.SpotPrices()` and
`Operator.refreshAWSPrices`, gated by `Config.AWSPriceRefresh`) and for
Azure (`AzureSpotClient` in `azure.go`, via the Azure Retail Prices API,
wired through `internal/provider/azure_prices.go` and
`Operator.refreshAzurePrices`, gated by `Config.AzurePriceRefresh`).

No GCP equivalent exists in this package. There is no `GCPSpotClient`, no
`internal/provider/gcp_prices.go`, no `Operator.GCPPrices` field, and no
`Config.GCPPriceRefresh` flag. GCP catalog offerings are refreshed by
neither live-refresh path: `Operator.HandleDesiredRunnerCount` passes GCP
offerings through both `refreshAWSPrices` (filters on `Provider != "aws"`)
and `refreshAzurePrices` (filters on `Provider != "azure"`) untouched, so
they keep whatever `PriceMicros`, `Currency` and `ObservedAt` the static
catalog already assigned them.

This is a deliberate, permanent product decision for this project, not a
deferral: GCP catalog offerings stay on static pricing indefinitely, unless
Google later publishes a structured field this package could key off
without guessing. See [prices-gcp.background.md](prices-gcp.background.md)
for the investigation and the reasoning behind that decision.
