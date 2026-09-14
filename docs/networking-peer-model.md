# WireGuard peer model: trust, revocation, secrets and qualification

> **Status: IMPLEMENTED and reachable.** This document answers the four
> questions [networking-control-plane.md](networking-control-plane.md)
> explicitly left open for issue #16's `wireguard` `NetworkProfile` mode, and
> all four are now implemented: "Peer trust" and "Secret shape"
> (`internal/wireguard`'s keypair/poll-token generation and peer-snapshot
> computation; `internal/lifecycle.Allocation`'s checkpointed
> public-key/overlay-address/poll-token-hash fields; `provider.Command`'s
> cloud-init embedding), "Revocation"'s poll endpoint
> (`internal/health.WireGuardPeersHandler`), and the actual WireGuard
> tunnel/data-plane device bring-up plus its qualification lane
> (`internal/wireguard/tunnel`). `internal/configapi/compile.go`'s
> `network()` accepts `NetworkProfileSpec.Mode: "wireguard"` (restricted to
> exactly one `NetworkMapping` - see "What this document does not decide"
> below for why that restriction exists), `internal/operator` threads the
> compiled NetworkProfile's identity onto every allocation it creates, and
> `provider.Command.NetworkPeers` is a real implementation
> (`internal/operator.New`) reached from both `cmd/runnerscout/main.go`'s
> mounted-config path and `internal/configapi/runtime.go`'s CRD-driven path
> (both build on `operator.NewWithCredentials`). `health.Status.WireGuardPeers`
> is likewise a real, always-mounted `*health.WireGuardPeersHandler`,
> constructed once in `cmd/runnerscout/main.go` (the only place a
> `health.Status` exists) for whichever entry point `main.go` selects. An
> operator who never configures a `wireguard` mode `NetworkProfile` sees no
> behavior change: every code path this document describes remains a
> complete no-op for an allocation whose `NetworkProfile` is `""`.
>
> The remaining real gap this status line's earlier versions named -
> "no code path assigns `lifecycle.Allocation.WireGuardOverlayAddress`" - is
> now also closed: `internal/wireguard.NextOverlayAddress` allocates one from
> the `NetworkProfile`'s own `NetworkMapping.CIDRs`, and
> `internal/operator.HandleDesiredRunnerCount` assigns it to every new
> wireguard-mode allocation at the same point it sets `NetworkProfile`. See
> "5. Overlay address allocation" below.

## Recommendation

### 1. Peer trust: transitive, from cloud-init, no handshake

There is no peer-to-peer or peer-to-controller trust handshake. The controller
is the sole source of truth for the peer list of a `NetworkProfile`. At the
same `Pending` → `Creating` transition where `lifecycle.Controller.Step`
(`internal/lifecycle/lifecycle.go`) already asks the allocation's provider to
mint a GitHub JIT token and bake it into the cloud-init script
(`provider.Command.CreateWithResources`, `internal/provider/command.go:64-91`),
it also generates a fresh WireGuard keypair for that allocation and embeds the
private key, the allocation's overlay address, its poll bearer token (§2),
and the full current authorized-peers snapshot for that `NetworkProfile` into
the same cloud-init payload, via the same `Bootstrap()` channel
(`internal/provider/command.go:54-58`). Trust is "the controller booted this
VM," exactly as it already is for the JIT token, the SSH key
(`internal/provider/azure.go:65`) and every other credential this VM holds.
No CA, no pre-shared enrollment handshake, no gossip.

### 2. Revocation: eager controller-side removal, delivered by VM-side polling

A WireGuard peer entry is removed from the controller's authoritative peer
list the instant `lifecycle.Controller.Step` decides an allocation is leaving
`Running` — i.e. at the same moment it sets `Phase = Deleting` on retirement
or expiry, or `Phase = Deleted`/`TimedOut` on an interruption or absence
observation (`internal/lifecycle/lifecycle.go`, the `Running`/`Creating`
branches). This removal is **not** gated on the observed-drain precondition
that CQ-11 imposes on removing the *cloud-resource* finalizer
(`TestRuntimeDeletionRequiresObservedDrainAndRetainsOtherFinalizers`); it is a
separate, eagerly-triggered, independently retried effect.

Delivery is pull, not push: cloud-init cannot be re-delivered to a running
instance on any of the three providers, so each allocation's VM runs a small
agent that polls a narrow, allocation-scoped, bearer-token-authenticated HTTP
endpoint the controller already exposes for `/healthz`/`/readyz`
(`docs/runtime-image.md`), at a bounded interval, to refresh its local peer
list. The controller's authoritative list (computed from `Running`
allocations of that `NetworkProfile`) converges every surviving peer within
one polling interval of a revocation, regardless of whether the revoked VM
itself is reachable, gone, or mid-interruption.

### 3. Secret shape: no new per-VM Secret; one optional NetworkProfile-scoped Secret

Per-allocation WireGuard private keys are never persisted as a Kubernetes
Secret. They are generated in controller memory at `Creating`-transition time
and consumed immediately into the cloud-init payload, exactly like the JIT
token today — the controller never needs a VM's private key again after
boot. The corresponding **public** key and overlay address are added as new
`omitempty` fields on `lifecycle.Allocation` (alongside `ResourceID` and
`Resources`) and persisted through the existing `Store.Save` checkpoint, so a
controller restart recovers the peer's public identity the same way it
recovers cloud resource identities (CQ-07).

The one Secret this mode introduces is optional: `NetworkProfileSpec.EnrollmentRef`
(`api/v1alpha1/types.go:222`, already declared, currently rejected outright
for `separate` mode at `compile.go:163`) names a namespace-scoped Secret
holding an operator-supplied WireGuard pre-shared key (PSK), read with the
same `secret()` validation every other `SecretKeyReference` already goes
through (`internal/configapi/compile.go:48-53`). It is a defense-in-depth
layer on top of the Noise handshake, not a trust root: trust already comes
entirely from §1, so an unset `EnrollmentRef` is fully supported and is the
default.

### 4. Local qualification lane: two same-process `Tunnel` instances over real loopback UDP, no TUN, no containers

The data-plane device bring-up itself lives in `internal/wireguard/tunnel`
(a subpackage of `internal/wireguard`, kept separate so
`golang.zx2c4.com/wireguard/tun/netstack`'s own gVisor dependency tree does
not reach `cmd/runnerscout` until something actually calls it): `tunnel.BringUp`
takes a private key, an overlay address and a peer list, and returns a
`*tunnel.Tunnel` backed by a real `golang.zx2c4.com/wireguard/device.Device`
on the netstack (gVisor) TUN backend, with `Close` and `RemovePeer` methods
and a `Net()` accessor exposing netstack's own `net.Dialer`/`net.Listener`-
compatible surface for sending or receiving traffic through the tunnel.

The qualification lane is that package's own `//go:build emulators` test
(`internal/wireguard/tunnel/qualification_emulator_test.go`, mirroring
`internal/provider/azure_emulator_test.go`'s structure: a header comment
naming exactly which real, non-mocked code is under test and why). It does
not add a `wireguard` path to `tools/emulators.py`, and no Docker container
is involved: it brings up two independent `tunnel.Tunnel` instances — the
same `BringUp` production code path any future caller uses — each bound to
its own real loopback UDP port, and proves the two properties this
document actually requires: a payload sent from one peer over a real Noise
handshake and real per-peer encryption is delivered correctly to the other,
and once one side's peer list is reconfigured to no longer include the
other (`Tunnel.RemovePeer` — the data-plane action a controller-side
revocation ultimately drives), the next attempted packet from the revoked
peer is silently dropped rather than delivered. This is the same
observed-effect testing philosophy this codebase already uses for
interruption detection (CQ-09). Neither instance is given
`--cap-add=NET_ADMIN`, `--device=/dev/net/tun`, `--privileged` or
`--network host` — the netstack (gVisor) TUN backend never touches a kernel
TUN device, so none of those are ever requested, the same guarantee
[networking-control-plane.md](networking-control-plane.md) already relies
on, regardless of container topology. See
[networking-peer-model.background.md](networking-peer-model.background.md)
for why two same-process instances over loopback prove the same
protocol-level properties a real two-container setup would, for this
specific test, without container/network-namespace scaffolding no code
path here actually depends on.

### 5. Overlay address allocation: deterministic scan of the NetworkMapping's own CIDRs, no new CRD field

A wireguard-mode `NetworkProfile`'s single `NetworkMapping` already carries
`CIDRs []string` (`api/v1alpha1/types.go`), validated by `compile.go`'s
`network()` as canonical, non-overlapping prefixes for every mode that uses
it. Wireguard mode reuses that same field as its overlay address pool
instead of introducing a second CIDR concept: `network()` now returns those
CIDRs alongside the `NetworkProfile` identity it already returned, and
`internal/configapi/compile.go`'s `Compile()` threads them onto
`operator.Config.NetworkOverlayCIDRs` - a new field with the same
emptiness rule as `NetworkProfile` (non-empty only for wireguard mode). The
mounted-config entry point (`cmd/runnerscout/main.go`) needed no new code:
`operator.Config` is decoded directly from JSON, so a mounted config file
can already set this field, and `Config.Validate()` enforces the same
canonical-prefix requirement `compile.go` enforces for the CRD path.

`internal/wireguard.NextOverlayAddress(cidrs []string, taken map[string]bool)
(string, error)` does the actual allocation: a deterministic linear scan,
in the order the CIDRs are given and each CIDR's host range in ascending
address order, skipping every address already in `taken` and excluding
each CIDR's network and highest ("broadcast") address. It returns
`ErrOverlayAddressesExhausted` when nothing is left. It is a pure function
with no I/O and no persisted state of its own - collision avoidance is the
caller's responsibility, by construction of what it passes as `taken`.

`internal/operator.HandleDesiredRunnerCount` is that caller, and is where
allocation actually happens: at the same point it already sets
`NetworkProfile: o.Config.NetworkProfile` on every newly created `Pending`
`lifecycle.Allocation`, it also computes `taken` from every currently
active allocation sharing that `NetworkProfile` (from `o.Store.List`, the
same source `wireguard.Snapshot` already reads, plus any allocation still
sitting in the fleet ConfigMap's own `Pending` map from an earlier call
that has not yet been materialized into the `Store` by `Tick`) and assigns
every allocation admitted in the current batch a distinct address before
any of them exists as a real `Store` record. The address is then
checkpointed automatically: `lifecycle.Controller.Step`'s `save()` already
serializes the full in-memory `Allocation` value on every transition, the
same mechanism that already checkpoints `NetworkProfile` and (once a
provider sets it) `WireGuardPublicKey` - no additional Store or Controller
wiring was needed. Because the address is assigned exactly once, at
creation, and never touched again by `Step` or anything else, an
allocation's overlay address is stable for its entire lifetime by
construction, not by an explicit idempotency check.

Exhaustion (every CIDR fully assigned) is surfaced through the same
admission-refusal path `HandleDesiredRunnerCount` already uses for
`CatalogUnavailable`/`CatalogNotAdmissible`: the entire batch of newly
requested allocations is refused, the fleet's `Condition` is set to
`OverlayAddressPoolExhausted`, and the admission count `Reconcile` had
provisionally incremented is rolled back - no allocation from an exhausted
batch is partially created, and the next admission cycle (driven by the
existing 5-second `Tick`/scale-set demand cycle, not a bespoke retry loop)
naturally retries once capacity frees up, exactly like the two existing
conditions it mirrors.

Concurrency safety follows directly from how this codebase already
serializes every other mutation `HandleDesiredRunnerCount` makes: a
Kubernetes Lease ensures at most one leader `Operator` calls it at a time,
an in-process mutex (`o.mu`) serializes it against this same `Operator`'s
own `Tick`, and the fleet ConfigMap itself is read-then-written under
`resourceVersion` compare-and-swap (`docs/architecture.md`'s "Recovery and
cleanup" section) - a concurrent writer's conflicting update fails
`saveFleet` outright, so no batch of overlay-address assignments is ever
partially committed. No separate locking was added for this feature; it
relies on the same guarantee every other field `HandleDesiredRunnerCount`
sets on a new allocation already relies on.

A `NetworkMapping`'s CIDRs changing after allocations already exist against
it is not handled specially: an already-checkpointed
`WireGuardOverlayAddress` is never re-validated against a possibly-changed
CIDR, so an operator who shrinks or replaces a `NetworkMapping`'s `cidrs`
while allocations are active can end up with a currently-active allocation
whose checkpointed address falls outside the new CIDR. This mirrors how
this codebase already treats every other `NetworkMapping` field
(`subnetID`, `networkID`) - already-created cloud resources are never
retroactively reconciled against a changed `NetworkProfile` - and is left
as a real, undecided limitation rather than silently glossed over.

## Why

| Requirement | How the recommendation satisfies it |
| --- | --- |
| No second key-custody system (per networking-control-plane.md) | The controller stays the only place a private key or the authoritative peer list exists; the poll endpoint only serves state the controller already computes, it does not register or authenticate peers on its own authority. |
| "Credentials generated in controller memory, never in CRDs/logs/examples" | Per-allocation private keys never become a Secret object; they are generated and consumed in the same code path the JIT token already uses (`command.go:64-91`). |
| CQ-05 (no cross-namespace Secret/ConfigMap reference) | `EnrollmentRef` is validated through the exact same `secret()` helper (`compile.go:48-53`) every other `SecretKeyReference` in this codebase already uses. |
| CQ-07 (no fabricated replacement identity after an uncertain outcome) | The allocation's WireGuard public key and overlay address are checkpointed on `lifecycle.Allocation` the same way `ResourceID`/`Resources` already are, so a controller restart recovers rather than re-mints. |
| CQ-11 (finalizer removal gated on observed effect, not intent) | Deliberately **not** reused for peer revocation: revocation is triggered eagerly at the phase transition, decoupled from drain observation, because the two guard different failure modes (stale network trust vs. duplicate cloud delete) and coupling them risks a stuck-forever cleanup if peer delivery fails. |
| Qualification lane: "no host-network mode... only the minimum container capabilities/devices required" | No test topology here ever requests `NET_ADMIN`/`/dev/net/tun`/`--privileged`/host networking — the netstack (gVisor) TUN backend never touches a kernel TUN device regardless of whether the two `Tunnel` instances run in one process or two containers; see §4. |

## Rejected alternatives

**Per-VM cloud identity (IAM instance profile / managed identity / service
account) decrypting an encrypted cloud-init payload.** Investigated directly
against this codebase: GCP's create call forces an explicitly empty service
account list (`ServiceAccounts: []*compute.ServiceAccount{}`,
`internal/provider/gcp_sdk.go:207`), AWS's `RunInstancesInput` has no
`IamInstanceProfile` field at all (`internal/provider/aws.go:59-65`), and
Azure's ARM VM `properties` map has no `identity` block
(`internal/provider/azure.go:62-67`). No runner VM in this codebase holds any
cloud-native identity today, by what all three providers agree is a
deliberate omission, not an oversight. Adding one is a separate,
security-relevant scope expansion (a compromised job now inheriting a live
cloud credential) that this document declines to fold into a networking
design.

**A dynamic/handshake-based trust model** (TOFU, gossip, a peer
self-registering with a join code): rejected for the same reason
networking-control-plane.md rejected Headscale — it reintroduces a second
key-custody authority the controller does not already have, for no gain over
"the controller already knows every allocation it created."

**Gating cloud-resource finalizer removal on confirmed peer-revocation
delivery**, mirroring CQ-11's drain gate exactly: rejected because it
couples two independently-failing systems (peer-list delivery, cloud
resource deletion) with no shared failure mode, and a stuck peer-poll channel
would then also block cleanup of a resource this codebase's own cost posture
wants removed promptly.

**Giving each VM a Kubernetes ServiceAccount token to poll the API server
directly** instead of a narrow controller-hosted endpoint: rejected as a
larger blast radius than a single-purpose, allocation-scoped bearer token —
it would hand an untrusted, internet-adjacent VM a credential valid against
the whole Kubernetes API surface rather than one read-only peer-list value.

**A push-only model that re-renders cloud-init after boot**: not available
on any of the three providers for a running instance without recreating it,
which would defeat the entire point of updating a live peer list.

## What this document does not decide

The concrete wire schema of the poll endpoint and its response format, the
polling interval and backoff policy, PSK rotation policy when
`EnrollmentRef` is set, rate limiting or abuse protection on the new
controller-hosted endpoint, observability for peer-convergence lag, and the
VM-side systemd unit or other boot-time integration that would actually
call `internal/wireguard/tunnel.BringUp` on a running instance are all real
implementation decisions this document does not make. (Overlay IP address
allocation and exhaustion handling - previously listed here as
undecided - is now decided; see "5. Overlay address allocation" above,
including the one limitation it does not resolve: a `NetworkMapping`'s
`cidrs` changing after allocations already exist against it.)

How a VM-side peer learns another peer's outer transport endpoint (the real
dialable network address:port for the underlying UDP socket a
`device.Device` sends encrypted packets to — distinct from the inner overlay
address `CloudInitPayload.Peer` already carries, which only supplies an
AllowedIPs entry) is now resolved *within a single `NetworkMapping`*:
`lifecycle.Allocation.WireGuardEndpoint`/`wireguard.Peer.Endpoint` capture a
VM's already-cloud-assigned private IP at creation time for all three
providers: AWS (`internal/provider/aws.go`) and Azure
(`internal/provider/azure_inventory.go`) capture it with zero new API calls,
from a response their create paths already fetch for other purposes. GCP's
create path never receives a `compute.Instance` response of its own (only
`compute.Operation` ones, which carry no NetworkInterfaces), so it captures
this via one genuinely new post-create `Instances.Get` call
(`internal/provider/gcp_sdk.go`'s `gcpCaptureWireGuardEndpoint`) — made only
when `a.NetworkProfile != ""`, so it is inert (zero added calls) for every
allocation this codebase's configuration path can produce today, and
best-effort (never fails the creation) once that gate opens. Reachability
*across* two different `NetworkMapping`s (different providers or regions
referenced by one `NetworkProfile`) also remains fully undecided; see
`Allocation.WireGuardEndpoint`'s own doc comment
(`internal/lifecycle/lifecycle.go`) for why. See
[networking-peer-model.background.md](networking-peer-model.background.md)
for the investigation and reasoning behind each recommendation above.
