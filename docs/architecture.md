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
Discovered cloud dependency IDs and provider generation IDs are saved before deletion and retained across restart;
providers must reconcile those exact IDs even if discovery tags disappear. Each allocation
retains at most 64 dependency references. Deletion completes only after observed absence. Unknown outcomes retain cleanup
obligations across restart. Local provisioning timeout does not cancel a GitHub
workflow or decide its result. Credentials remain external Secret files or
workload identity; they are not catalog data.

Admissions retain their original deadlines and bounded attempt counts. Unchanged
demand does not rearm expired slots. Confirmed job completion and observed VM
cleanup release successful slots. A zero-demand reset requires old resources to
be absent. Lowering maxRunners drains naturally without discarding allocations.

## Configuration surface

The controller accepts mounted JSON or one named RunnerScaleSet CRD. The
`runnerscout.io/v1alpha1` API defines ProviderConfig, RunnerClass, RunnerScaleSet,
CapacityCatalog, NetworkProfile and CapacityBudget. References stay in one
namespace; the reader rechecks object and Secret identities before accepting a
configuration snapshot.

A RunnerScaleSet may optionally reference a CapacityBudget (`budgetRef`) to
bound worst-case daily spend; see
[capacity-budget.md](capacity-budget.md) for its contract and a
[worked example](../examples/multicloud/README.md).

One Lease covers the CRD supervisor, session workers and finalization. Before
cloud operations, the supervisor persists a configuration checkpoint containing
Secret references and installs a cleanup finalizer. Invalid configuration pauses
new admissions while accepted work retains its original deadlines. Recovery can
reconcile cloud resources without GitHub credentials. Deletion drains allocations
and removes the finalizer only after confirmed cleanup. Helm supports both configuration modes with a read-only CRD uninstall guard;
[worked examples](../examples/multicloud/README.md) cover the full resource graph.

Spot retries default to disabled. Execution requires confirmed interruption,
unambiguous runner/run/attempt correlation, bounded fresh retries and explicit
acknowledgment of repeated workflow effects; all three cloud providers retry
interrupted jobs end-to-end under this composition.

Separate provider networks are the default. A single-mapping WireGuard overlay
mode is also supported and functionally usable end-to-end, retaining each
provider's native network boundary: each new allocation is assigned a unique
overlay IP address, and a reference VM-boot integration
([examples/wireguard-agent](../examples/wireguard-agent)) starts the
boot-time agent; see [operations.md](operations.md) for both. Cross-region
and cross-provider overlay peering remain unsupported by design (a
wireguard-mode `NetworkProfile` accepts exactly one `NetworkMapping`), a
permanent architectural limitation, not open work.
