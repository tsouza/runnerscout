# WireGuard peer model — background

## Why trust is derived from cloud-init instead of a handshake

The controller already delivers exactly one high-value secret to every VM it
creates today: the GitHub Actions JIT registration token, base64-encoded
directly into the cloud-init script `provider.Bootstrap()` returns
(`internal/provider/command.go:54-58`). That comment is explicit about the
threat model already accepted: *"cloud-init never prints the JIT token; the
image already contains runner binaries"* — the token is written to a
`0700`/`umask 077` file, consumed once, and deleted by a `trap ... EXIT`
before the VM shuts itself down. Nothing about a WireGuard private key is
different in kind: it is another secret whose entire lifetime equals one
allocation's lifetime, delivered once at boot, never needed back by the
controller. Reusing the identical channel and identical assumptions means
this design adds no new trust primitive to reason about — it is the same one
already load-bearing for every VM this codebase creates.

The alternative — some dynamic handshake where a booting VM proves its
identity to the controller or to other peers before being trusted — was
considered and rejected quickly: it requires *something* to arbitrate the
handshake (a shared secret distributed how? a CA issuing certificates
managed by whom?), which is exactly the second key-custody system
[networking-control-plane.md](networking-control-plane.md) already rejected
Headscale and Tailscale for. "The controller booted this VM" is not a weaker
trust statement than a handshake would produce — it is stronger, because it
is already true and already load-bearing for the JIT token, the SSH
authorized key (`internal/provider/azure.go:65`), and the VM's entire
software identity. A handshake would only add a second way for the same fact
to be re-derived, with new failure modes of its own.

## Why a per-VM cloud identity was investigated and rejected

The task that produced this document explicitly asked whether an IAM
instance profile, Azure managed identity, or GCP service account already
exists per VM, since that would let the VM decrypt an encrypted cloud-init
payload without the plaintext key ever touching cloud-init logs. Checking
each provider's actual VM-creation call in this codebase found the opposite
of a hint — a consistent, cross-provider absence:

- **GCP**: `internal/provider/gcp_sdk.go:207` sets
  `ServiceAccounts: []*compute.ServiceAccount{}` with
  `ForceSendFields: []string{"ServiceAccounts"}` — this does not merely omit
  a service account, it explicitly forces the field to serialize as an empty
  list, overriding whatever project-level default service account GCP would
  otherwise attach. This is affirmative, not accidental.
- **AWS**: `internal/provider/aws.go:59-65`'s `ec2.RunInstancesInput` has no
  `IamInstanceProfile` field set anywhere in `createAWS`. The same input does
  set `MetadataOptions: &types.InstanceMetadataOptionsRequest{HttpTokens:
  "required", HttpPutResponseHopLimit: 1, InstanceMetadataTags: "disabled"}`
  — IMDSv2 enforced, a hop limit of 1 (blocks anything not running directly
  on the instance's own network namespace from reaching IMDS through it), and
  instance tags excluded from IMDS. This is a provider that has already
  thought carefully about instance metadata exposure and chosen to hold the
  line at "no IAM role, harden IMDS itself" rather than "grant identity, rely
  on scoped permissions."
- **Azure**: the ARM template's VM `properties` map
  (`internal/provider/azure.go:62-67`) has no `identity` key at all —
  Azure VMs without an explicit `identity` block have no managed identity,
  system- or user-assigned.

Three independent cloud adapters agreeing on "no identity for the runner VM"
is a stronger signal than any one of them alone: it reads as a deliberate
security posture (a compromised job running on the runner must not be able
to call the cloud provider's own control plane with a live credential), not
an oversight three separate code paths happened to share. Building the
WireGuard secret-delivery design around "the VM already has a cloud identity
it can use to decrypt its own boot payload" would have been building on a
capability that does not exist and — going by the pattern above — was
likely withheld on purpose. Granting one now, solely to support encrypted
cloud-init for one optional network mode, is a security-posture change with
its own blast radius (a live cloud credential reachable from arbitrary job
code) that deserves its own dedicated review, not a side effect of this
document.

Given that, the encrypted-payload mitigation the task asked to investigate
reduces to: deliver the WireGuard key exactly as the JIT token is already
delivered, plaintext-in-cloud-init, accepting the same exposure window this
codebase already accepts for a credential of comparable sensitivity (a
runner registration token is itself a single-use, short-lived credential
capable of registering as a GitHub Actions runner — not nothing). The
mitigating factors are identical: IMDSv2 enforced with hop limit 1
(no lateral SSRF path to the metadata service from a container or process
that isn't the instance's own kernel), a single-tenant ephemeral VM whose
only workload is the one job it was created for, and prompt in-VM deletion
of the decoded material after use.

## Why revocation is push-then-poll, not push-only or fully event-driven

Cloud-init/user-data is a boot-time-only delivery channel on all three
providers: none of them support redelivering updated user-data to an
already-running instance without recreating it, which would defeat the
entire purpose of updating a live peer list mid-lifetime. That leaves pull
as the only mechanism capable of converging a *running* VM's peer list after
boot.

A pull design still needs an endpoint to pull from. The two candidates
considered:

1. **A dedicated, narrow, allocation-scoped endpoint on the controller's
   already-existing HTTP server** (the same process already serving
   `/healthz` and `/readyz`, per `docs/runtime-image.md`). This adds one
   more route to a component that already exists and already serves HTTP;
   it does not add a new system, a new datastore, or a new identity
   authority — the controller remains the sole place the peer list and any
   key material exist. This is the recommended design.
2. **The VM polls the Kubernetes API server directly**, using a
   ServiceAccount token or kubeconfig handed to it at boot. Rejected: this
   hands an internet-adjacent, cloud-vendor-controlled, potentially
   compromised VM a credential valid against the entire Kubernetes API
   surface the controller itself runs on, to solve a problem that only needs
   one read-only value (the current peer list for one `NetworkProfile`). The
   blast radius mismatch is the whole objection — a leaked poll token for
   option 1 discloses one `NetworkProfile`'s public keys and endpoints; a
   leaked ServiceAccount token for option 2 is a foothold into the cluster
   itself.

Because this is a pull design, the "how fast must revocation propagate"
question in the task resolves to "one polling interval," not an instant
push. This document does not pick the interval's exact value (see "What this
document does not decide" in the main document) but the design guarantees
that whatever interval is chosen, it bounds the maximum staleness window for
every surviving peer uniformly — including the case where the revoked VM
itself is already gone (a spot interruption) and cannot be asked to
cooperate in its own removal at all. Its own poll token stops working the
moment the controller marks the allocation `Deleted`/`TimedOut`, which
matters only in the (attacker-controlled) case where the interrupted
instance somehow survives long enough to keep asking for a peer list it is
no longer part of.

## Why peer revocation is not gated on CQ-11's observed-drain precondition

CQ-11 says a finalizer-protected object must not lose its finalizer before
independently observed drain/cleanup completes — the invariant
`TestRuntimeDeletionRequiresObservedDrainAndRetainsOtherFinalizers` exercises
today. It was tempting to reuse the exact same gate for peer revocation
("don't consider a peer revoked until the VM is confirmed gone"), but the
two gates protect against different failure modes:

- CQ-11's drain gate exists so that removing a finalizer — which stops this
  codebase from ever looking at the resource again — cannot happen while
  real cleanup work (draining admissions, releasing a slot) is still
  outstanding. Acting too early there means *losing track of* something that
  still needs handling.
- Peer revocation exists so that a departed or compromised VM's key stops
  being trusted by everyone else as soon as possible. Acting too *late*
  there — e.g. waiting for observed absence, which for a spot interruption
  may never arrive cleanly or may take an unbounded number of retries — is
  the failure mode, not acting too early.

Coupling them would also introduce a real deadlock risk this codebase's
existing design goes out of its way to avoid elsewhere: if peer-list
delivery to survivors were ever a precondition for finishing cloud-resource
cleanup, an unreachable poll endpoint (misconfigured network, a
`NetworkProfile` a survivor no longer resolves) could hold a fully-drained,
otherwise-deletable, still-billing cloud resource open indefinitely. This
codebase already treats "retain cleanup obligations across restart, but
never let an unrelated subsystem's failure block cleanup that is otherwise
provable" as an existing principle (`docs/architecture.md`'s "Unknown
outcomes retain cleanup obligations across restart" line refers to the same
spirit, applied to a different subsystem). Keeping the two effects
independent and idempotent — each retried on its own schedule, from the same
originating phase transition — preserves that principle for both.

## Why `EnrollmentRef` became the optional pre-shared-key reference

`NetworkProfileSpec.EnrollmentRef *SecretKeyReference` already existed in
`api/v1alpha1/types.go` before this document, and `compile.go:163` already
refuses it (along with `AllowedServices`) for `separate` mode with the
message "separate networking cannot enroll peers or imply overlay access
rules" — i.e. whoever added the field had already scoped it to whatever
`wireguard` mode would become, without specifying what it would hold. This
document had to give it a concrete meaning to be implementation-ready.

The candidate that was rejected first: treating `EnrollmentRef` as a
Headscale-style pre-auth join secret that authorizes a peer's own
self-registration. That would reopen exactly the dynamic-trust-establishment
question §1 above closes — a peer proving itself via a shared secret is
still a second way to derive the same trust "the controller booted this VM"
already provides, and it would make the Secret itself a credential capable
of enrolling an arbitrary peer, which is a larger blast radius than anything
else in this design touches.

The meaning this document assigns instead — an optional WireGuard
pre-shared key (PSK), added on top of the Noise protocol's own public-key
handshake — matches WireGuard's own documented purpose for a PSK: defense
in depth against a future weakness in the Curve25519 handshake itself, not
a trust root. It is deliberately optional and defaults to unset, because
trust in this design never depended on it in the first place; an operator
who wants the extra layer can supply one without changing anything else
about how peers are authorized.

## Key recovery after a controller restart mid-creation

CQ-02 and CQ-07 exist because a create call's outcome can be left
ambiguous by a restart, and this codebase's answer is always "re-observe,
never blindly re-mint" (`TestCreationRecoveryRequiresCheckpointAndNeverCreatesReplacement`).
The same question applies to a WireGuard private key generated in controller
memory: if the controller crashes after generating a key and handing it to
`Bootstrap()`, but before checkpointing anything about it, and the VM has
already received that cloud-init payload, the controller must not mint a
*second* key for the same allocation — that would leave two keys claiming
one overlay identity, one of them silently orphaned on a VM the controller
no longer knows about.

The honest answer this document gives is conditional rather than asserting
an unverified cloud API guarantee: if the provider's existing observation
path (`ReconcileCreation`/`Observe`, already called during `Creating`
reconciliation) can recover the already-delivered cloud-init payload from
the provider's own record of the instance (some providers expose a
running instance's launch configuration back through their API; this
document does not claim all three do, and did not verify it against a real
account), the controller should prefer recovering the existing key material
over minting a replacement — the same preference CQ-07 already establishes
for cloud resource identity. Where that recovery is not possible, the
correct fallback is to treat that one allocation's WireGuard membership as
unrecoverable and exclude it from future peer-list convergence, without
touching the rest of its lifecycle — a narrower degradation than retrying
the VM's creation over again, and one that fails safe (the peer is simply
absent from the overlay) rather than fails open (a duplicate or ambiguous
identity).

## Local qualification lane: matching existing emulator conventions exactly

`tools/emulators.py` already establishes the pattern this design's
qualification lane follows: create an `--internal` docker network
(`docker network create --internal <network>`), start each fixture as a
named container attached to it with a docker network alias, wait for
readiness with a bounded retry loop against a raw socket or HTTP health
check, then run `go test -tags emulators` with `RUNNERSCOUT_*_ENDPOINT`
environment variables pointing at the containers' resolved IPs. No container
in that existing pattern runs with elevated capabilities; the same
`docker run` argument list style (`--network`, `--cpus`, `--memory`, no
`--cap-add`, no `--device`, no `--privileged`, no `--network host`) applies
unchanged to two WireGuard containers, because the netstack/gVisor backend
this codebase already committed to in
[networking-control-plane.md](networking-control-plane.md) never opens
`/dev/net/tun` or requires `CAP_NET_ADMIN` in the first place — the
qualification lane does not need to work around a missing capability, it
simply never needs one.

`internal/provider/azure_emulator_test.go`'s header comment is the template
for the new emulator test file's own header: it explains which real,
non-mocked package code the test drives, cites exactly how the gap it closes
was found, and is explicit that it exercises the actual production code
path against a close-to-real environment rather than a hand-rolled fake.
The WireGuard emulator test should make the same two claims explicitly: that
it drives the real userspace-WireGuard code this codebase will ship, not a
mock, and that its revocation assertion (remove a peer, observe the next
packet silently dropped) is an *observed effect*, in the same spirit CQ-09
already requires for interruption detection — not an assertion that the
removal code path merely ran.
