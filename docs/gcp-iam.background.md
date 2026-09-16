# GCP `ProviderConfig` IAM permissions — background

Filed as [issue #160](https://github.com/tsouza/runnerscout/issues/160).

## How this list was derived

Nothing in `operations.md`, `prices-gcp.md`/`.background.md` or
`architecture.md` previously stated the full IAM permission set a `gcp`
`ProviderConfig`'s service account needs. Starting from a role with only the
permissions that look sufficient by reading `createGCP`'s own request shape -
`compute.instances.create`, `compute.instances.{get,delete,list}`,
`compute.disks.{get,delete}`, `compute.zoneOperations.{get,list}`,
`compute.subnetworks.use`, `compute.networks.get`,
`compute.images.useReadOnly` - every `gcp-spot` allocation in production
failed, 3 attempts each, `LocalProvisioningTimeout`, with zero visible error
from runnerscout itself.

Each missing permission in the final list (`gcp-iam.md`) was found only by
reading GCP's own Cloud Audit Log for the exact `v1.compute.instances.insert`
error, one at a time:

```
gcloud logging read 'protoPayload.methodName:"compute.instances"'
```

1. `compute.disks.create` - `createGCP`'s `Instances.Insert` attaches a
   **new** boot disk inline via `initializeParams`, not a pre-existing one;
   GCP's IAM check treats that as also creating a disk, independent of
   `compute.instances.create`.
2. `compute.disks.setLabels` and `compute.instances.setLabels` - the
   instance and its inline boot disk are both labeled at create time
   (`p.gcpLabels(a)`, used later for ownership checks in
   `gcpInventory`/`ownsGCP`). A labeled create needs `*.setLabels` on top of
   `*.create`.
3. `compute.instances.setMetadata` - the startup script is embedded as
   instance metadata (`compute.Metadata.Items`) in the same
   `Instances.Insert` call, which needs its own permission too.

Each fix required a separate failed production attempt to surface the next
missing permission, since GCP's audit log only records the error for the
permission actually checked before that request was rejected - it does not
list every permission a request would need up front.

## Why the failure mode is silent

`createGCP` treats a missing permission the same as any other create failure
that never reaches a definitive answer: 3 attempts, then
`LocalProvisioningTimeout`. This is deliberate and correct as a *classification*
matter - the codebase's own discipline elsewhere (see
`docs/prices-gcp.background.md`'s "Why this fails this codebase's
classification discipline") is to never infer a specific cause from an
ambiguous transport or provider response, and a 403 buried inside a wrapped
SDK error is exactly that kind of response, not something safe to pattern-match
on and report as "IAM permission missing" with confidence. The cost is that a
missing permission looks identical to any other transient provisioning
failure from inside runnerscout, and the only way to actually tell them apart
is GCP's own audit log - which is why this document exists: to remove the
need to rediscover that once per deployment.

## Why the minimal list, not a broad predefined role

A predefined role like `roles/compute.instanceAdmin.v1` would avoid this
entire investigation, but was not used here: this project's existing
operations posture (see `docs/operations.md`'s credential-scoping guidance
throughout - AWS profile scoping, Azure federated workload identity,
per-provider credential isolation) consistently favors the minimum permission
set actually exercised by the code path, not the broadest role that happens
to cover it. The list in `gcp-iam.md` is exactly the permissions
`createGCP`'s `Instances.Insert` plus this codebase's own inventory/deletion
paths (`gcpInventory`, `ownsGCP`, deletion) use - nothing that only a
predefined role would additionally grant.

## Relationship to the orphaned-runner-registration bug

Every failed `gcp-spot` attempt observed in production also left a
permanently-orphaned GitHub Actions runner registration: the scale-set
listener claims a job and registers a runner name for it before the VM ever
exists, and nothing deregistered that claimed name when the create attempt
failed and the allocation reached `TimedOut`. Under real PR-storm load this
made the missing-permission problem far more disruptive to diagnose than a
one-time setup error - each failed attempt both wasted a full
`provisioningSeconds` timeout and permanently occupied one of `maxRunners`'
slots at the GitHub level. That failure mode is tracked and fixed separately
(issue #162); it is noted here only because it is what made this particular
misconfiguration so costly to leave undocumented.

## Reference Terraform

The working role was first assembled as Terraform, not this documented list -
see <https://github.com/allodops/cirunners/blob/main/tofu/gcp.tf#L218-L262>.
That repository is not part of runnerscout; it is referenced here only as a
concrete example of granting this exact permission set.
