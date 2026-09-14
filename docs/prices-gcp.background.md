# GCP spot price observation — background

## What was checked

Google's own documentation for its two public pricing surfaces, fetched
live (not recalled from memory) on 2026-09-13:

- Cloud Billing Catalog API v1 — `services.skus.list`:
  <https://docs.cloud.google.com/billing/docs/reference/rest/v1/services.skus/list>
  and its how-to, <https://docs.cloud.google.com/billing/v1/how-tos/catalog-api>.
- The newer Cloud Billing Pricing API v2beta — `skus.list` /
  `skus/{id}/prices`:
  <https://docs.cloud.google.com/billing/docs/reference/pricing-api/rest/v2beta/skus>,
  <https://docs.cloud.google.com/billing/docs/how-to/get-pricing-information-api>.
- Google's own published SKU-group listing for Spot/Preemptible N1 VMs
  (the public pricing-calculator-backed page, not third-party):
  <https://cloud.google.com/skus/sku-groups/spot-preemptible-n1-vms>.
- Spot VM pricing behavior: <https://docs.cloud.google.com/compute/docs/instances/spot>.

## Why this is not the same shape as AWS's `DescribeSpotPriceHistory`

AWS's `DescribeSpotPriceHistory` (`aws.go`) returns one row keyed by exactly
`(AvailabilityZone, InstanceType, ProductDescription)`, with a single price.
`AWSSpotClient.Observe` treats anything other than exactly one matching row
as an error — never a best-effort pick. GCP has no equivalent single-call,
single-key, single-price endpoint. Concretely, from the docs above:

1. **A SKU is a billed resource *component*, not a priced instance.**
   Compute Engine SKUs split `resourceGroup` by "Core" (vCPU) and "RAM"
   (memory) — confirmed both in the v1 SKU `category` schema and in
   Google's own public SKU-group page, whose example descriptions include
   `"Spot Preemptible N1 Predefined Instance Core running in Berlin"` and
   `"Spot Preemptible Custom Extended Instance Ram running in APAC"` as
   *separate* SKUs with separate SKU IDs. Getting one instance-type price
   requires locating the right Core SKU and the right RAM SKU (and, for
   attached GPUs/Local SSD, more SKUs still) and combining them — never a
   single authoritative row.

2. **Machine type is never a structured field, in either API version.**
   The v1 `Sku.category` has only `serviceDisplayName`, `resourceFamily`,
   `resourceGroup`, `usageType` — no machine-type or instance-type field.
   The newer v2beta `Sku` resource (`productTaxonomy` / `geoTaxonomy`)
   doesn't add one either: `productTaxonomy.taxonomyCategories` names broad
   service categories ("Serverless", "Cloud Run", "TaskQueue"), and the
   only place a machine family like "N1 Predefined", "A2" or "Custom
   Extended" appears at all is inside the free-text `displayName`/
   `description` string. Matching a specific `instanceType` string (e.g.
   `n2-standard-4`) to the right family bucket in that text is pattern
   matching against prose Google documents as a 256-character human-readable
   field, not a machine-readable key, in both API generations.

3. **The free-text location field uses human place names, not zone/region
   codes.** The same example descriptions read `"running in Berlin"` and
   `"running in APAC"` — city names and continent abbreviations, not
   `europe-west3` or `us-central1`. The *structured* region field that does
   exist (`serviceRegions` in v1; `geoTaxonomy.regionalMetadata.region` in
   v2beta) is confirmed **region-only** in both versions — there is no zone
   field anywhere in either SKU schema. GCP's own Spot VM pricing page
   documents pricing as varying by "machine families, GPUs, TPUs" and
   region, never by zone within a region. This package's `Observe(ctx,
   region, zone, instanceType)` shape mirrors AWS's genuinely
   per-Availability-Zone spot pricing; GCP has no per-zone price for
   `Observe`'s `zone` parameter to ever legitimately disambiguate — it
   would be accepted and silently ignored, which is a materially weaker
   contract than AWS's and Azure's than the shared signature implies.

4. **No documented request-side filter by region or machine type.**
   `services.skus.list` (v1) takes only `pageSize`/`pageToken`/`startTime`/
   `endTime`/currency — matching is entirely client-side, over the full SKU
   list for the Compute Engine service (thousands of SKUs across every
   family, tier and region). The v2beta `skus.list` is no better in this
   regard.

5. **Getting from "the right Core/RAM SKUs" to one price would require an
   externally maintained shape table anyway.** Even after correctly
   isolating a family's Core and RAM SKUs for a region, computing a specific
   `instanceType`'s price means multiplying by that type's vCPU count and
   memory-GB — neither of which the pricing API supplies. That mapping
   (`n2-standard-4` → 4 vCPU / 16 GB) would have to live in this codebase as
   a second, hand-maintained source of truth, silently stale the moment
   Google ships a new machine family, with no API to validate it against.

6. **The v2beta Pricing API is explicitly not GA.** Google's own reference
   material for it carries no GA/stability marking of the kind its v1
   surfaces do, and it changes the schema shape (`productTaxonomy`/
   `geoTaxonomy` replacing `category`/`serviceRegions`) while keeping the
   same free-text-only machine-type limitation described above — adopting
   it would trade one undocumented mapping for another, not remove it.

## Why this fails this codebase's classification discipline

`AWSSpotClient.Observe`'s own doc comment states the bar directly: "It
never returns a stale or synthesized price: an empty, ambiguous or
otherwise incomplete response is always an error." The same discipline
underlies `docs/qualification.md`'s CQ-01 ("misclassify a... rejection")
and `gcpCapacityFailure`/`gcpConfirmedPreemption` in `gcp_sdk.go`, which
only ever return true against GCP's own definitive operation-error codes or
system operation types — never inferred from absence or pattern-matched
against a description. A GCP price client built on family/location string
matching plus a hand-maintained vCPU/RAM table would be exactly the kind of
"usually works" inference this project's provider adapters otherwise refuse
to ship. It would compile, pass fixture tests written against today's
description strings, and silently mismatch or misprice the moment Google
rewords a SKU description, ships a new machine family, or changes a tiered
rate boundary — with no error surfaced, because there is no authoritative
signal to check the guess against.

## Decision

`internal/prices/gcp.go` will not be built. GCP catalog offerings stay on
static pricing, as they are today, permanently — accepted as this
project's product decision for issue #2, closing that issue's remaining
scope rather than leaving it open indefinitely on a hypothetical future
API change. Two paths that could justify revisiting this were considered
and explicitly declined:

- Waiting for Google to publish a structured, documented, GA field that
  maps a specific Compute Engine machine type and region (zone-level
  pricing does not exist to map to) to exactly one SKU or exactly one
  deterministic combination of SKUs — not observed in either API
  generation as of this investigation, and not something this project
  controls the timeline of.
- Accepting a materially weaker contract than AWS/Azure's (e.g. a
  region-level-only price, refreshed from a hand-maintained family/shape
  table, explicitly documented as best-effort rather than authoritative) —
  declined because it would still be pattern-matching against prose
  Google can reword at any time, the exact "usually works" inference this
  project's provider adapters otherwise refuse to ship (see "Why this
  fails this codebase's classification discipline" above).

If Google ever publishes the structured field described in the first
option, revisiting this decision is a small, well-scoped follow-up: this
document and issue #2 stay as the record of why it wasn't built sooner.

This mirrors other places this session's line of work left something
genuinely undone rather than faked, since resolved with the same
discipline: GCP real-cloud qualification (see
`docs/qualification-real-cloud.md`) and Azure's full Event Grid wiring
(see `docs/azure-interruption-delivery.md`, issue #4, closed) were both
tracked as real, honestly-scoped open work rather than shipped as a false
pass, until actually built and verified.
