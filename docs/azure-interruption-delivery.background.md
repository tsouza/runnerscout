# Azure spot interruption delivery: investigation notes

This document records the investigation behind
[azure-interruption-delivery.md](azure-interruption-delivery.md)'s
recommendations - why each rejected option was actually rejected, the
external evidence consulted, and the confidence level behind the one
assumption that document could not verify directly.

## Why a background file at all, here

`networking-control-plane.md` (the sibling WireGuard library-choice
document this document's structure is modeled on) has no
`.background.md` companion - its "Why" table is short enough that the
rationale fits inline. This document's investigation touched external,
unverifiable-in-repo facts (Azure's own documented behavior, a
third-party project's bug report) that materially shaped the
recommendation but do not themselves change what a future implementer
does, which is exactly the split this codebase's documentation
convention draws.

## The Event Grid destination comparison, in more depth

The four rejected destinations were not rejected by generic familiarity
with Azure - each was checked against this codebase's actual, already-
established conventions:

- **Webhook** was checked against Microsoft's own current documentation
  on private-link event delivery
  (https://learn.microsoft.com/en-us/azure/event-grid/consume-private-endpoints,
  fetched during this investigation): "with push delivery... your
  application can't receive events over private IP space." That is a
  stronger statement than "webhooks need a public IP" - it means even the
  managed-identity-based secure alternative Microsoft documents there
  still terminates at a public endpoint, not a private one. Given this
  codebase's `charts/runnerscout/README.md` NetworkPolicy section already
  treats "no unnecessary exposure" as a design constraint (a
  default-deny NetworkPolicy, controller-hosted endpoints limited to
  `/healthz`/`/readyz`), accepting a public inbound endpoint purely to
  receive an interruption signal would be a strictly larger concession
  than anything else this codebase's networking model has accepted so
  far - larger, in fact, than the poll-based WireGuard peer endpoint
  `networking-peer-model.md` designs, which is inbound but namespaced,
  bearer-token-scoped, and never internet-facing by requirement.

- **Storage Queue as a first-class Event Grid handler** was confirmed
  directly against
  https://learn.microsoft.com/en-us/azure/event-grid/handler-storage-queues
  (fetched during this investigation), which also surfaced a detail worth
  recording: Event Grid subscriptions support a `deadLetterDestination`
  (a separate Storage Blob container) for events Event Grid itself could
  not *deliver* after exhausting retries. This is a different failure
  mode from anything `internal/azurequeue.Client` handles - it applies
  before a message ever reaches the queue - so it does not change this
  document's recommendation, but it is worth an install-doc mention as an
  optional operational safety net, listed under "What this document does
  not decide."

- **Event Hub / Service Bus** were rejected on shape (low-volume, one-shot
  facts vs. streaming/session primitives), not on any Azure-specific
  limitation found during research - both are equally capable of carrying
  this event type. The rejection is purely "this codebase should not pay
  for machinery its own signal shape does not need," consistent with this
  codebase's general preference (visible throughout `docs/operations.md`)
  for the simplest mechanism that satisfies a stated requirement.

## Why message deletion is keyed on "classified", not "preempted"

`azureevents.ParsePreemptionEvent`'s own doc comment is explicit that it
"fails closed" - and, on inspection of the actual code
(`internal/azureevents/azureevents.go:91-93`), that failure-closed
behavior extends to the *event type itself*: a well-formed
`AvailabilityStatusChanged` event (HealthResources' other, sibling event
type) is treated as an **error**, not as "false, no error." This matters
for `internal/azurequeue.Client.Poll`'s deletion policy: if the Event Grid
subscription's filter were not scoped precisely to
`Microsoft.ResourceNotifications.HealthResources.ResourceAnnotated` only,
every legitimate `AvailabilityStatusChanged` event would arrive as a
`Result.Err` and never be deleted, permanently wedging the queue with
messages this client can never resolve. This is why
[azure-interruption-delivery.md](azure-interruption-delivery.md)'s
Recommendation states the subscription-level event-type filter as a firm
requirement of the install steps, not a suggestion - it is not just
tidiness, it is what keeps `azurequeue.Client`'s fail-closed-on-error
design from becoming a self-inflicted poison-queue.

Given a correctly scoped subscription, the only messages `Poll` should
ever see are `ResourceAnnotated` events, so deleting every message that
classifies successfully (whether `Preempted` is true or false) is safe
and desired: a differently-annotated event
(`VirtualMachineDeallocationInitiated` and the like) is a legitimate,
uninteresting event this client has correctly recognized and disposed of,
not an error condition.

## The base64 assumption, and how much confidence it deserves

Neither of Event Grid's own health-resources schema pages
(https://learn.microsoft.com/en-us/azure/event-grid/event-schema-health-resources,
https://learn.microsoft.com/en-us/azure/event-grid/handler-storage-queues,
both fetched during this investigation) states an encoding for the queue
message body outright. Corroborating evidence was found instead in two
indirect sources: a web search surfaced the general claim that "when
connecting an Azure service bus queue or Azure storage queue to a storage
account to receive blob created events, all queue messages/events are
base64 encoded," and a real bug report against Argo Events' Azure Queue
Storage event source
(https://github.com/argoproj/argo-events/issues/3302) shows that
project's own consumer code unconditionally base64-decodes dequeued
message text before treating it as JSON - which only makes sense as a
design choice if Event Grid's queue deliveries are, in fact, normally
base64-encoded.

This is real but indirect evidence, not a primary-source confirmation,
so `internal/azurequeue.decodeMessageBody` was written defensively rather
than on a single unverified assumption: it tries base64-decoding first
(and only accepts the result if it decodes to something that looks like a
JSON object), falling back to the raw message text otherwise. Either an
Event Grid-delivered base64 payload or a raw JSON payload (from a
differently configured subscription, or a future local emulator/qualification
fixture) parses correctly; a truly malformed payload still fails closed
through `azureevents.ParsePreemptionEvent` either way. Confirming which
encoding real Event Grid deliveries actually use is explicitly named in
the main document's "What this document does not decide," pending the
live Azure qualification budget `docs/operations.md` says is not yet
allocated.

## Why `internal/azurequeue.QueueClient` is a narrow interface, not a transport fixture

`internal/provider/azure_sdk.go`'s `AzureSDK` tests its ARM calls by
injecting a custom `Options *arm.ClientOptions` (a transport-level
fixture) rather than an interface, because ARM's generated clients
(`armdeployments`, `armresources`) have no natural narrow interface to
extract - the whole point of using the generated SDK there is its
completeness. `azqueue.QueueClient` is different: this package calls
exactly two of its methods (`DequeueMessages`, `DeleteMessage`), and Go's
implicit interface satisfaction means a two-method interface declared
locally is satisfied by the real SDK type with no adapter code at all.
This mirrors `internal/operator/prices.go`'s own
`awsPriceObserver`/`azurePriceObserver` pattern (a narrow interface next
to the field it backs) more closely than it mirrors `azure_sdk.go`'s
transport-fixture style - the two existing precedents in this codebase
are both legitimate, and the choice here follows whichever one actually
fits: a two-method surface calls for an interface, not an HTTP fixture.

## Why AWS/GCP's correlation model does not translate directly

It would be easy to assume Azure's confirmed-interruption code should
look like `gcpConfirmedPreemption` (`internal/provider/gcp_sdk.go:239-261`):
list operations, filter by target, called from inside a specific
allocation's `Observe`. But GCP's list call is itself scoped per-`Observe`
invocation - it is called *because* a specific allocation is already being
examined, and its zone-scoped list only has to match one instance's
target link. A Storage Queue has no equivalent "ask about this one
allocation" operation: dequeuing is inherently a drain of whatever is
currently sitting in the queue, decoupled from any particular allocation
being observed at that moment. This is the real structural difference
between a push-delivered signal and a poll-scoped one, and it's why this
document does not simply say "make Azure work like GCP" - `internal/azurequeue`
is deliberately a queue-wide drain (like a mailbox check), while the
*correlation* step against a specific allocation happens afterward, in a
future caller, using the ARM-name-equals-`a.ID` convention rather than
GCP's per-call API filter. The convenient part is that this future
correlation step is trivial (a string comparison) specifically because
Azure's VM-naming convention already happens to equal the allocation ID,
the same way GCP's does - if it didn't, a real resourceID-to-allocation
index would have been unavoidable, and that index (not the queue client)
would have been this document's hardest open question.
