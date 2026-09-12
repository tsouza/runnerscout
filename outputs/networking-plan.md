# Optional uniform private networking

Status: planned under AUTH-OPTIONAL-NETWORK-001. This is an opt-in feature;
separate AWS, Azure and GCP networks remain the default. No paid networking or
live-cloud deployment is authorized by this plan.

## Product behavior

Present one logical private-network configuration while retaining each cloud's
native VPC or VNet, subnet, firewall and account boundaries. Provider mappings
must be explicit, including regions, subnet identities and DNS requirements.
Reject overlapping CIDRs and incomplete mappings before provisioning. Never
silently attach a runner to a different account or a public network when its
requested private connectivity cannot be established.

Model the option in the implemented CRD API and provide examples for all three
providers. Final API names and fields must be derived with the CRD/controller
implementation, rather than publishing unsupported manifests. Initially integrate
with operator-managed connectivity; automatic provisioning of paid gateways is
outside this option. Assess a WireGuard overlay for the first real local seam.
Any external enrollment service must use short-lived, narrowly scoped credentials
referenced through Secrets; credentials must not appear in CRDs, logs or examples.

Each runner class declares required private services and permitted traffic.
Runner-to-runner access defaults to denied. Shared connectivity must not expose
the Kubernetes control plane or unrelated internal services. Preserve ordinary
GitHub access and provider cleanup paths. Define the distinction between network
readiness and runner registration explicitly: a runner requiring private access
must not accept a job before its network policy is ready.

Track ephemeral peer identity and cleanup with the allocation. Termination,
interruption, bootstrap timeout and uncertain create outcomes retain peer
revocation obligations. Reconciliation must not revoke another allocation's peer.
Network failures do not cancel GitHub workflows or reset provisioning deadlines.

## Dependency-ordered implementation

1. Finish the dependency/cache/PR sweep and preserve current verified behavior.
2. Define the CRD network configuration alongside provider and runner-class APIs;
   implement default-off validation, provider mapping and Secret references.
3. Implement the selected overlay bootstrap/readiness and idempotent peer cleanup
   integration, preserving allocation identities and existing deadlines.
4. Build a bounded local network qualification lane and runnable worked examples.
5. Qualify the selected cloud topology only after a separate numeric spending and
   runtime allocation exists. Do not equate local network success with cloud success.

## Local qualification without cloud accounts

Create three isolated Docker network domains representing AWS, Azure and GCP.
Connect them using actual WireGuard peers/gateways rather than an HTTP mock.
Use task-owned containers and networks, no host-network mode, no host Docker
socket mounts, and only the minimum container capabilities/devices required by
the chosen implementation. Keep test keys ephemeral and out of exported evidence.
Bound the campaign duration and resource usage; retain failure diagnostics and
always check removal of peers, containers and networks.

Required positive and negative controls:

- A permitted runner reaches a private test service across network domains and
  resolves its private DNS name; the same path fails when connectivity is disabled.
- Unpermitted services, other runners and control-plane endpoints remain unreachable.
- Missing routes, overlapping address ranges, wrong mappings and failed DNS cannot
  produce a ready runner; no fallback silently weakens a hard network requirement.
- Exercise representative packet sizes and an MTU black-hole condition; test
  gateway loss/restart, key expiry/rotation and interrupted bootstrap.
- Duplicate reconciliation neither enrolls duplicate peers nor expands access.
  Runner deletion, timeout and interruption revoke the owned peer; restart recovers
  unfinished cleanup without removing a different allocation's peer.
- Default-off examples provision no overlay and require no enrollment Secret.

Artifacts must identify topology, configuration revision, software/image digests,
checks and cleanup verdicts. Local network domains represent cloud boundaries;
they do not implement the cloud providers' routing or billing behavior.

## Later live-cloud qualification and cost

Use isolated AWS VPC, Azure VNet and GCP VPC configurations with non-overlapping
address ranges. Verify actual route tables, security groups/firewalls, DNS,
NAT/egress, MTU and return paths; repeat allowed/denied connectivity and independent
cleanup inventory checks. Retain provider-specific evidence and failure cases.

WireGuard/Headscale software can be free, but gateway/server compute and outbound
cross-cloud transfer may cost money. Managed VPN gateways add hourly charges;
dedicated private links have separate recurring and transfer costs. Select a
bounded topology and record maximum spend, runtime, transfer allowance and cleanup
owner before any paid execution. Local emulator/overlay testing incurs no cloud
charges. Paid gateway creation must never be a hidden consequence of enabling a
runner class.

## Acceptance boundary

The option is complete only when its implemented configuration, bootstrap,
isolation/readiness, recovery and cleanup behavior match the documented contracts
and their required checks pass. Report local-overlay and live-cloud support
separately. Until real cloud qualification exists, examples must label that gap;
neither passing YAML validation nor a single successful ping qualifies the feature.
