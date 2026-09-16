# Azure spot interruption delivery: provisioning, consumption and correlation

> **Status: IMPLEMENTED**, except step 1 (provisioning), which remains a
> human/install-doc responsibility by design - see the Recommendation below.
> `internal/azureevents` parses the Event Grid payload;
> `internal/azurequeue.Client` polls the Storage Queue; `observeAzure`
> (`internal/provider/azure.go`) sets `Observation.Interrupted` for Azure
> from that poll's result via `Operator.Tick`'s `pollAzureInterruptions`/
> `applyAzureInterruptions`, gated behind the opt-in
> `Config.AzureInterruptionQueueURL` (default `""`, disabled). AWS, Azure and
> GCP interruption retries all work end-to-end as of this implementation
> (issue #4, closed).

## Recommendation

1. **Provisioning is a human/install-doc responsibility**, exactly like
   every other cloud IAM/credential setup step this codebase already
   defers to a human (`docs/operations.md`'s Azure/AWS/GCP credential
   sections; `charts/runnerscout/README.md`'s "Credentials and isolation").
   Neither the controller nor the Helm chart creates the Event Grid system
   topic, the event subscription, or the Storage Queue. The install
   documentation should be extended with the exact steps (system topic
   scoped to the subscription, a queue, a subscription filtered to
   `Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated`
   only, and read/process/delete rights on that queue for the controller's
   identity).

2. **The delivery target is an Azure Storage Queue.** Event Grid's other
   destination types for this event source (Webhook, Service Bus, Event
   Hub, Hybrid Connections) are all worse fits for this codebase's existing
   conventions than Storage Queue - see Rejected alternatives.

3. **Consumption is a bounded-interval poll, not a persistent
   connection**, shaped like `internal/githubjobs`/`internal/prices`'s
   plain synchronous clients and driven by `internal/operator.Operator`'s
   own polling cadence (`Tick`), not like
   `github.com/actions/scaleset`'s `MessageSessionClient`/`listener.New`
   live session. `internal/azurequeue` ships exactly that shape, and is
   wired into `internal/operator`'s own polling cadence
   (`internal/operator/azure_interruptions.go`), constructed via
   `internal/provider.AzureSDK.InterruptionQueue`.

4. **Correlation is exact VM-name equality**, mirroring GCP's
   `a.ID`-as-instance-name convention, not a new lookup structure:
   `azureevents.ParsePreemptionEvent`'s returned ARM resource ID has the
   allocation's own `a.ID` as its trailing path segment (the same ID
   `internal/provider/azure.go`'s `azureID` helper and `createAzure`'s ARM
   template already use as the VM's resource name), so a future caller
   recovers the allocation identity by string comparison, no cache or
   index required. The eventual call site is
   `internal/provider/azure.go`'s `observeAzure`
   (`internal/provider/azure.go:134-152`), populating the same
   `lifecycle.Observation.Interrupted` field AWS/GCP already populate,
   consumed by `internal/lifecycle/lifecycle.go`'s existing, provider-agnostic
   Running-phase absence handling (`internal/lifecycle/lifecycle.go:265-271`)
   with **no change needed there**.

All of steps 2-4 are implemented: `internal/azurequeue.Client` (poll a
Storage Queue, classify each message via `azureevents.ParsePreemptionEvent`,
delete what it can definitively classify); `Operator.Tick` polls it once per
cycle and threads the result into every azure-kind `provider.Command`
(`internal/operator/azure_interruptions.go`'s `pollAzureInterruptions`/
`applyAzureInterruptions`); `observeAzure` correlates by exact ARM resource
ID (`azureConfirmedPreemption` in `internal/provider/azure.go`) and sets
`Observation.Interrupted`, consumed by `internal/lifecycle/lifecycle.go`'s
existing, provider-agnostic Running-phase absence handling with no change
needed there, exactly as originally planned. `Operator.AzureInterruptions`
and `Config.AzureInterruptionQueueURL` default to nil/`""` (disabled) -
existing deployments never start making live Azure Storage Queue calls
without an explicit choice to do so. Step 1 (provisioning the Event Grid
system topic, subscription and Storage Queue itself) remains the one piece
still deferred to a human, by design - see the Recommendation above.

## Why

**Provisioning stays human/install-doc (never controller, never Helm).**
This codebase has never had the controller provision a non-VM cloud
resource, by what all three providers agree is a deliberate omission, not
an oversight - `docs/networking-peer-model.md`'s own "Rejected
alternatives" already establishes this for cloud identity (GCP's
`ServiceAccounts: []*compute.ServiceAccount{}`, AWS's `RunInstancesInput`
having no `IamInstanceProfile` field, Azure's ARM VM `properties` map
having no `identity` block). The same pattern holds for networking inputs:
`internal/provider/command.go:15-26`'s `Config` struct takes `Subnet`,
`SecurityGroup`, `ResourceGroup`, `Project` and `Profile` as pre-existing
external references, never as resources any provider adapter creates.
Cloud IAM/credential setup is already entirely install-doc territory
(`docs/operations.md`'s per-provider "Azure authentication uses the native
Go SDK..." / "GCP uses the native Go SDK..." sections;
`charts/runnerscout/README.md`'s "Credentials and isolation";
`examples/multicloud/README.md`'s manual `kubectl create secret` steps) -
there is no separate `docs/installation.md`; this codebase already folds
exactly this kind of human setup step into `docs/operations.md` itself,
which is where the Event Grid/queue setup steps belong too.

Helm-hook provisioning (an ARM/Bicep template deployed by a chart hook
Job) is actively worse than doing nothing: CQ-08 exists specifically
because "Helm-driven object replacement must not double-run or lose
cleanup" (`docs/qualification.md`: "Orphan or double-own a CRD-owned cloud
resource across a Helm upgrade or rollback"), and
`charts/runnerscout/README.md` already states plainly that "CRDs are
externally managed: rolling back the chart does not rewind their settings
or replace durable allocation state." An Event Grid subscription created
by a Helm hook has no CRD-like durable-state reconciliation loop behind
it at all - a chart rollback or reinstall would either orphan it (a stray
subscription silently draining a queue no controller reads) or attempt to
recreate/delete it with no ownership record, the exact class of failure
CQ-08 was written to prevent, for a resource this codebase has no existing
machinery to protect.

**Storage Queue over the other Event Grid destinations.** A subscription
preemption event is a single low-volume, one-shot-per-VM fact, not a
telemetry stream: Event Hub's throughput/partitioning model and Service
Bus's sessions/ordering/dead-lettering both solve problems this signal
does not have, at real operational cost this codebase's own aversion to
speculative infrastructure argues against. Webhook is worse on a more
fundamental axis: it requires the controller to expose a public,
internet-reachable HTTPS receiver, which has no precedent anywhere in this
codebase (the only inbound endpoints today are `/healthz`/`/readyz`, and
`docs/networking-peer-model.md`'s own "Rejected alternatives" already
rejected a push-only model for the unrelated WireGuard peer-poll design on
the same "not available... would defeat the point" grounds). Microsoft's
own documentation confirms Event Grid push delivery cannot even use a
private endpoint at all ("with push delivery... your application can't
receive events over private IP space" -
https://learn.microsoft.com/en-us/azure/event-grid/consume-private-endpoints),
so a Webhook receiver would force a new public attack surface this
codebase has never accepted for anything else it does. Hybrid Connections
solves relaying through an on-premises firewall to a service Azure cannot
otherwise reach - irrelevant, since the controller already reaches every
other Azure control-plane endpoint over ordinary outbound HTTPS. Storage
Queue is Azure's own simplest, cheapest, natively poll-only destination
for exactly this shape of signal, and Event Grid supports it as a
first-class, officially documented handler
(https://learn.microsoft.com/en-us/azure/event-grid/handler-storage-queues).

**Poll on Tick's cadence, not a persistent connection.** Azure Storage
Queue's `GetMessages` operation has no long-poll or streaming mode -
every call is a single bounded request, structurally identical to
`internal/githubjobs.Client` and `internal/prices.AWSSpotClient`/
`AzureSpotClient`: "plain synchronous-call clients with no background
listener of their own." This codebase's one genuine live-connection
precedent, `github.com/actions/scaleset`'s
`MessageSessionClient`/`listener.New` (`internal/operator/operator.go:461-470`,
run under `Operator.RunSession`/`runLeader`), exists specifically because
the GitHub Actions scale-set protocol itself is a persistent session API -
a shape Storage Queue does not share. `internal/azurequeue.Client.Poll`
therefore mirrors `refreshAWSPrices`/`refreshAzurePrices`
(`internal/operator/prices.go`), a single bounded call meant to be driven
by `Operator.Tick`'s own loop (`internal/operator/operator.go:417` calls
`o.Controller.Step(call, a.ID)` once per allocation each cycle), not a
second goroutine or session.

**Correlation by exact VM-name equality, not a new index.** AWS's
`awsInventory.observation` (`internal/provider/aws_inventory.go:227-254`)
and GCP's `gcpConfirmedPreemption`
(`internal/provider/gcp_sdk.go:239-261`) both already report `Interrupted`
by asking the cloud API about one specific, already-known allocation - AWS
via its cloud-assigned `i-...` ID stored in `a.ResourceID`, GCP by matching
`op.TargetLink` against `p.gcpResource(a, "instances", a.ID)`, i.e. GCP's
own VM name is `a.ID`. Azure's VM name is `a.ID` too:
`internal/provider/azure.go:75`'s ARM template names the
`Microsoft.Compute/virtualMachines` resource `a.ID` directly, and
`azureID` (`internal/provider/azure.go:23-25`) builds every Azure resource
ID from that same convention. `azureevents.ParsePreemptionEvent` already
returns the full ARM resource ID
(`/subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.Compute/virtualMachines/<name>`);
its trailing segment is exactly the allocation ID a future caller already
has in hand while iterating allocations in `Operator.Tick`. This needs no
new resourceID-to-allocation index, cache, or reverse lookup - a plain
string comparison against the allocation already being processed
(`internal/lifecycle/lifecycle.go:258`'s `p.Observe(ctx, a)` call, inside
the Running-phase branch) is sufficient, exactly the same evidentiary
shape CQ-09 already requires: "interruption requires the provider's own
definitive signal present" (`docs/qualification.md`), which the
`VirtualMachinePreempted` annotation already is, alongside AWS's
`Server.SpotInstanceTermination` state-reason code and GCP's
`compute.instances.preempted` operation type.

## Rejected alternatives

**The controller provisioning the Event Grid subscription/queue itself at
startup**, using the Azure SDK it already imports. Rejected: no code path
in `internal/provider/*.go` has ever called a create-topic/queue/
subscription-shaped SDK method; every non-VM identifier that isn't a
cloud-assigned VM/disk/NIC name is a pre-existing external reference
threaded through `Config` (Subnet, SecurityGroup, ResourceGroup, Project).
Adding controller-side provisioning of a subscription-scoped Event Grid
resource would be a first-of-its-kind scope expansion with no supporting
precedent, and duplicates exactly the reasoning
`docs/networking-peer-model.md` already used to reject giving runner VMs a
cloud-native identity: a new, security-relevant capability this document
declines to fold into a delivery-transport decision.

**A Helm chart hook (ARM/Bicep template, or a Job running `az` /
Terraform) provisioning it on install.** Rejected on CQ-08 grounds - see
Why above. Helm's own upgrade/rollback lifecycle has no ownership record
for an out-of-band Azure subscription resource the way it does for
in-cluster objects it owns via `ownerReferences`, or the way CRDs are
explicitly carved out as "externally managed."

**Event Hub, Service Bus (Queue or Topic), Webhook, and Hybrid
Connections** as the delivery target - see Why above for each.

**A persistent Storage Queue "listener"** (a goroutine holding a queue
client open, using a short internal sleep loop to simulate long-polling).
Rejected: Storage Queue's REST API gives no efficiency or latency benefit
over calling `GetMessages` once per `Tick`, so a second background
goroutine would only duplicate `Operator.Tick`'s own scheduling
responsibility for no gain, unlike the scale-set listener, which exists
because GitHub's own protocol requires holding a session open.

**A resourceID-to-allocation cache or index**, built by draining the queue
independently of any specific allocation's `Observe` call and looking
values up later. Rejected as unnecessary machinery: because the ARM
resource name already equals `a.ID`, the correlation is a string
comparison performed exactly when `observeAzure(ctx, a)` is called for
that specific allocation - no separate cache, TTL, or invalidation policy
is needed, unlike a design that had to map an opaque cloud-assigned
identifier back to an allocation the way AWS's `i-...` IDs would require
were AWS's interruption signal not already scoped per-allocation by its
own inventory poll.

## What this document does not decide

Only step 1 (provisioning) remains outside this document's scope, by the
same design as every other cloud IAM/credential setup step this codebase
defers to a human (see Recommendation 1 above) - `docs/operations.md` does
not yet document it. Two pieces of that provisioning step are genuinely
undecided: the exact install-doc IAM role/permission grant (Storage Queue
Data Message Processor scoped to the one queue, versus a broader role),
and the Event Grid subscription's own delivery retry policy and optional
dead-letter destination (a separate Storage Blob container Event Grid can
target when *delivery itself* is exhausted - a different failure mode from
a message this client dequeues but cannot classify, which
`internal/azurequeue.Client.Poll` already handles by leaving it
undeleted).

Separately, whether `internal/azurequeue`'s assumption that Event Grid
base64-encodes message bodies before placing them in the queue holds
against a real subscription remains unverified (no live Azure Event Grid
budget is currently allocated per `docs/operations.md`, so
`internal/azurequeue`'s decoder defensively tries both raw and
base64-decoded bytes; see
[azure-interruption-delivery.background.md](azure-interruption-delivery.background.md)
for the investigation behind that assumption).

Everything else this section previously listed as undecided - the
`provider.Config` field referencing the queue endpoint
(`Config.AzureInterruptionQueueURL`), the poll interval (rides
`Operator.Tick`'s existing cadence unchanged), and the
`internal/provider/azure.go`/`internal/operator/operator.go` code
constructing `azurequeue.Client` and consulting it from `observeAzure` -
is implemented; see the Status banner and steps 2-4 above.
