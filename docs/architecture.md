# Architecture

RunnerScout hosts its control plane in Kubernetes and provisions standalone,
ephemeral VMs. VMs do not join the cluster. Each controller owns one class and
GitHub scale set; ARC must use different scale sets. The direct scale-set client
supplies aggregate demand. Provisioning allocations count toward the runner cap.

## Placement

A finite catalog defines allowed provider pools, resources and compute prices.
Unknown required capabilities are ineligible. Prices use integer USD
microdollars per hour and expire after five minutes; storage, egress and tax are
excluded. Eligible pools sort by price, then pool ID. Every allowed provider must
declare enumeration complete before exhaustion can justify fallback.

On-demand fallback is disabled by default. When enabled, it requires definitive
capacity rejection from every eligible spot pool in a complete, fresh snapshot.
Unknown outcomes, cooldowns and unvisited pools do not establish exhaustion.
Hard resource, region and price constraints never relax during fallback.

## Recovery and cleanup

Allocations transition through pending, creating, running, deleting and deleted.
Persist creating intent before cloud effects. Kubernetes ConfigMaps use
resourceVersion compare-and-swap; a Lease coordinates leadership. Every cloud
effect uses a durable allocation identity and explicit provider/account binding.

An ambiguous create outcome requires observation, never blind replacement.
Deletion completes only after observed absence. Unknown outcomes retain cleanup
obligations across restart. Local provisioning timeout does not cancel a GitHub
workflow or decide its result. Credentials remain external Secret files or
workload identity; they are not catalog data.

Admissions retain their original deadlines and bounded attempt counts. Unchanged
demand does not rearm expired slots. Confirmed job completion and observed VM
cleanup release successful slots. A zero-demand reset requires old resources to
be absent. Lowering maxRunners drains naturally without discarding allocations.

## Configuration surface

The runtime uses mounted configuration and an atomically refreshed catalog file.
The experimental `runnerscout.io/v1alpha1` schemas define ProviderConfig,
RunnerClass, RunnerScaleSet, CapacityCatalog and NetworkProfile. References stay
in one namespace; the reader checks stable UID/resourceVersion snapshots.
CRD-driven runtime reconciliation and chart installation are not yet implemented
([#15](https://github.com/tsouza/runnerscout/issues/15)).

Spot retries default to disabled. Execution requires confirmed interruption,
unambiguous runner/run/attempt correlation, bounded fresh retries and explicit
acknowledgment of repeated workflow effects; composition is tracked in
[#4](https://github.com/tsouza/runnerscout/issues/4).

Separate provider networks are the default. Optional shared private connectivity
retains each provider's native network boundary; enabled overlay execution is
currently rejected. Integration and isolated local qualification are tracked in
[#16](https://github.com/tsouza/runnerscout/issues/16).
