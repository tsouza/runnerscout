# GCP spot price observation

Part of [issue #2](https://github.com/tsouza/runnerscout/issues/2).

`internal/prices` observes live provider-driven spot prices for AWS
(`AWSSpotClient` in `aws.go`, via EC2's `DescribeSpotPriceHistory`, wired
through `internal/provider/aws_prices.go`'s `AWSSDK.SpotPrices()` and
`Operator.refreshAWSPrices`, gated by `Config.AWSPriceRefresh`).

No GCP equivalent exists in this package. There is no `GCPSpotClient`, no
`internal/provider/gcp_prices.go`, no `Operator.GCPPrices` field, and no
`Config.GCPPriceRefresh` flag. GCP catalog offerings are refreshed by
nothing: `Operator.HandleDesiredRunnerCount` passes GCP offerings through
`refreshAWSPrices` untouched (it filters on `Provider != "aws"`), so they
keep whatever `PriceMicros`, `Currency` and `ObservedAt` the static catalog
already assigned them.

Building this is currently not recommended. See
[gcp.background.md](gcp.background.md) for the investigation and the
reasoning behind that recommendation.
