# Optional private networking: WireGuard control-plane assessment

> **Status: PROPOSED, not yet implemented.** This document names the
> control-plane approach a future implementation of issue #16's `wireguard`
> `NetworkProfile` mode should use. No code in this repository implements
> any of it yet; `internal/configapi/compile.go` still rejects
> `NetworkProfileSpec.Mode: "wireguard"` unconditionally.

## Recommendation

Use **userspace WireGuard via `golang.zx2c4.com/wireguard`** (the same
library `wireguard-go` and Tailscale's client are built on), with the
**netstack (gVisor) TUN backend**, not a real kernel WireGuard interface.

## Why

| Requirement (from [networking-plan.md](../work/private-history/source-cleanup-20260912/outputs/networking-plan.md), gitignored) | How userspace WireGuard satisfies it |
| --- | --- |
| "Credentials must not appear in CRDs, logs or examples" | Keypairs are generated in controller memory and stored only in a Kubernetes Secret scoped to the allocation; only the *public* key and endpoint are ever shipped to a peer. |
| "Track ephemeral peer identity and cleanup with the allocation" | Key generation and storage live entirely in the controller process, next to the allocation lifecycle this codebase already manages - no second key-custody system to reconcile against. |
| Local qualification lane: "no host-network mode... only the minimum container capabilities/devices required" | The netstack backend never touches a real kernel TUN device, so the qualification containers need no `NET_ADMIN`, no `/dev/net/tun`, and no `--privileged` - packets never leave userspace. |

## Rejected alternatives

**Shelling out to `wg`/`ip` CLI tools**, configured via the cloud-init
script `Bootstrap()` already generates (`internal/provider/command.go:54-58`,
called from `CreateWithResources` at `command.go:64-91`): the WireGuard
kernel module and tooling are more mature than any Go library, but this
codebase would gain no prior art for the actual failure mode the plan
worries about - key generation on the VM means the controller only learns
the public key back through a side channel (metadata/tag write-back),
adding a race between "VM boots" and "controller observes its key," or
else the controller pre-generates the private key and injects it into
cloud-init user-data, which is exactly the "credentials in CRDs, logs or
examples" pattern the plan explicitly rules out. The qualification lane
would also need real `NET_ADMIN` + `/dev/net/tun` per container.

**A Kubernetes-native mesh (Headscale/Tailscale, Cilium's WireGuard
transparent encryption)**: Cilium's WireGuard mode is documented as
pod-to-pod only - it has no hook for a non-Kubernetes peer, and runner VMs
are bare cloud instances, not pods, so it is architecturally inapplicable
regardless of maturity. Headscale is topologically plausible (built for
heterogeneous nodes joining via pre-auth keys) but means operating a
second control plane and a second key-custody system (Headscale's own
node registry) alongside the CRD controller's allocation lifecycle -
directly working against "peer identity tied to allocation lifecycle"
being a single source of truth. Headscale's own project also does not
describe itself as enterprise-hardened (no HA/active-active mode; a single
instance is a SPOF for new-peer enrollment).

## What this document does not decide

Peer trust model, revocation semantics on interruption/timeout, the exact
Secret shape for stored keys, and the local Docker qualification lane's
concrete topology are all real implementation decisions this document
does not make. Per the plan, none of that can be responsibly stubbed
separately from real network I/O - this document only removes one
prerequisite ambiguity (which library/approach) so that work can start
from a settled choice instead of re-litigating it. Those four questions are
now answered in
[networking-peer-model.md](networking-peer-model.md); what remains open
after that document is its own "What this document does not decide"
section, not a restatement of these four.
