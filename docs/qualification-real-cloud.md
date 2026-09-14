# Real-cloud qualification: AWS and GCP

Part of [issue #3](https://github.com/tsouza/runnerscout/issues/3)'s remaining
"Final real-cloud qualification" scope, plus [issue #2](https://github.com/tsouza/runnerscout/issues/2)'s
related "real provider pricing requires later qualification" ask. This covers
the AWS and GCP provider adapters. Azure is a separate, later, not-yet-built
piece of the same remaining scope — see each provider's "Known gaps" section
below.

Both workflows share one safety philosophy - `workflow_dispatch`-only,
an exact-phrase spend confirmation checked before any cloud credential is
configured, a hard-coded runtime ceiling no input can raise, pinned
operator-supplied network identifiers cross-validated before any create call,
and a two-layer teardown guarantee (the Go test's own `t.Cleanup` plus an
`if: always()` workflow step that independently re-derives everything and
re-queries the cloud API) - adapted to each cloud's real API shapes rather
than copied blindly. See
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)
for the reasoning behind each design choice, including where and why the two
providers' workflows genuinely differ.

## AWS

`.github/workflows/qualify-aws.yml` drives the production
`internal/provider` AWS adapter (`aws.go`, `aws_sdk.go`, `aws_inventory.go`,
`internal/prices/aws.go`) against real AWS infrastructure, through
`internal/provider/aws_realcloud_test.go` (built only with `-tags
realcloud`, never part of any other build or test path). One run:

1. Creates one real, billed EC2 Spot Instance via the adapter's own
   `CreateWithResources`.
2. Independently confirms it reaches EC2's real "running" state, using a
   second EC2 client built through the standard AWS SDK default credential
   chain — never the adapter's own session — so this confirmation never
   depends on the exact code path under qualification also certifying its
   own success.
3. Confirms the adapter's `Observe` reports the running instance as not
   interrupted.
4. Observes a real Spot price for the same pinned instance type/AZ via
   `internal/prices.AWSSpotClient` (through `Command.AWS.SpotPrices()`).
5. Deletes the instance via the adapter's own `Delete`, then independently
   re-queries the AWS API (the same second EC2 client) for leftover
   volumes/network interfaces owned by this run's allocation tags — never
   just trusting `Delete`'s or `Observe`'s own report of success.
6. Uploads everything observed (creation receipt, running-state
   confirmation, observation results, the price quote, the independent
   post-delete inventory query) as a workflow artifact, at
   `evidence/qualify-aws-<run>-<attempt>/manifest.json`.

### Trigger and required inputs

`workflow_dispatch` only — no schedule, no push, no pull_request. Every
input below is required, with no default:

| Input | Meaning |
| --- | --- |
| `confirm_real_spend` | Must equal exactly `I-UNDERSTAND-THIS-COSTS-REAL-MONEY`. |
| `aws_region` | Region to qualify against. |
| `vpc_id` | Pinned, pre-provisioned VPC. `subnet_id` and `security_group_id` must both belong to it (checked before any create call). |
| `subnet_id` | Pinned subnet to launch into. |
| `security_group_id` | Pinned security group to attach. |
| `ami_id` | Pinned AMI. Must be `available`, EBS-backed, and match `architecture`. |
| `instance_type` | Keep this cheap — it directly bounds real spend alongside `max_runtime_minutes`. |
| `architecture` | `amd64` or `arm64`; must match `ami_id`'s real architecture. |
| `max_runtime_minutes` | Plain integer, 1–20. Bounds the create/observe/price phase. |

Nothing is auto-discovered: the AMI and every network identifier are
operator-supplied, per issue #3's "pinned images/networks" requirement. The
instance's Availability Zone is not a separate input — it is read from
`subnet_id` itself (a subnet lives in exactly one AZ), so it cannot drift
from the pinned subnet.

### Hard bounds

- `max_runtime_minutes` is checked against a hard-coded workflow ceiling of
  **20** before anything else runs (before Go setup, before AWS credentials
  are even configured). No input can raise this ceiling.
- The job's own `timeout-minutes: 40` is a second, independent ceiling that
  does not derive from the input at all.
- Deletion runs on its own fixed 5-minute budget, independent of
  `max_runtime_minutes` — a run that spent its whole create/observe/price
  budget just confirming the VM works still gets a full, unhurried
  teardown attempt.
- `concurrency: { group: qualify-aws, cancel-in-progress: false }` — only
  one real-cloud run at a time; a second dispatch queues rather than races
  or cancels an in-flight one.

### Guaranteed cleanup

Two independent layers, either one alone sufficient for the common case:

1. The Go test's own `t.Cleanup`, registered immediately once a real
   instance exists, running on its own fresh 5-minute context regardless of
   how much of the create/observe/price budget was already spent.
2. The workflow's own final step, gated `if: always()` — the authoritative
   backstop, since it runs even if the job was killed by its own timeout or
   cancelled before the Go process's `t.Cleanup` could run. It re-derives
   everything it needs (region, allocation id, owner tag) directly from
   job inputs/env, never from a prior step's output, so it still works when
   every earlier step failed. It independently re-queries for any instance/
   volume/network-interface still tagged to this run and force-cleans
   anything found, then fails the job loudly (that condition is a real bug,
   never a normal outcome).

### Credentials

OIDC role assumption is preferred, matching this repo's existing GHCR/
cosign OIDC pattern (`release-build.yml`). A static-key fallback is also
supported:

- **OIDC** (preferred): set the `AWS_QUALIFICATION_ROLE_ARN` repository
  secret to an IAM role ARN (a role ARN is not sensitive on its own, but it
  is stored as a secret rather than a variable to match how it was actually
  provisioned in this repository). The workflow assumes it via
  `aws-actions/configure-aws-credentials`'s `role-to-assume`, using the
  job's own GitHub Actions OIDC identity (`id-token: write`, granted only
  to this job).
- **Static keys** (fallback): set the `AWS_QUALIFICATION_ACCESS_KEY_ID` and
  `AWS_QUALIFICATION_SECRET_ACCESS_KEY` repository secrets. Used only when
  `AWS_QUALIFICATION_ROLE_ARN` is unset.

Both paths converge on the same `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/
`AWS_SESSION_TOKEN` environment variables for the rest of the job — see
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)
for why this made supporting both trivial instead of a deferred follow-up.

`AWS_QUALIFICATION_ROLE_ARN` is the only one of these that exists in this
repository today (confirmed via `gh secret list`); the static-key secrets do
not. Referencing the static-key secrets by name is inert until an operator
deliberately provisions them — the OIDC path above is what a dispatch
actually uses today.

#### Minimum IAM permissions

The assumed role or IAM user needs, scoped to the pinned region/account:
`sts:GetCallerIdentity`; `ec2:DescribeSubnets`, `ec2:DescribeSecurityGroups`,
`ec2:DescribeImages`, `ec2:DescribeInstances`, `ec2:DescribeVolumes`,
`ec2:DescribeNetworkInterfaces`, `ec2:DescribeSpotPriceHistory`;
`ec2:RunInstances`, `ec2:TerminateInstances`, `ec2:DeleteVolume`,
`ec2:DeleteNetworkInterface`. No IAM, VPC-creation or billing permissions
are needed — the VPC/subnet/security group are pre-provisioned and only
ever read, never created or modified, by this workflow.

### Operator prerequisites

Before dispatching this workflow, an operator must have already:

1. Provisioned an isolated VPC, subnet and security group dedicated to
   qualification (never a production network).
2. Chosen a pinned, EBS-backed AMI and confirmed its real architecture.
3. Chosen a cheap instance type consistent with that architecture.
4. Set either `AWS_QUALIFICATION_ROLE_ARN` (preferred) or the static-key
   secret pair above, granting the minimum permissions listed.

### What this qualifies — and what it honestly does not

Qualified, for real, by a passing run:

- Real EC2 Spot creation, a real transition to "running", real Spot price
  observation, real deletion, and independently re-verified cleanup — all
  through the exact production adapter code path.

Named gaps, not covered by this piece:

- **Real GitHub Actions job execution** (issue #3's "ordinary runs-on,
  actual VM job execution"). The VM boots with a synthetic, non-functional
  placeholder in place of a real GitHub JIT registration token — the AMI's
  cloud-init runner-registration step is expected to fail harmlessly inside
  the guest. Confirming a real runner registers and executes a real
  workflow job needs a live scale set and a real ephemeral registration
  token — a separate, larger qualification piece this workflow does not
  attempt.
- **A real Spot interruption.** AWS provides no API to force one on demand.
  This workflow can only confirm the adapter correctly reports "not
  interrupted" against a real, healthy running instance; the
  `Server.SpotInstanceTermination` detection logic itself is covered
  separately, against synthesized state payloads, by the existing unit
  test suite.
- **Azure.** A separate, later piece of issue #3's same remaining scope.

## GCP

`.github/workflows/qualify-gcp.yml` drives the production
`internal/provider` GCP adapter (`gcp_sdk.go`, `gcp_credentials.go`) against
real GCP infrastructure, through `internal/provider/gcp_realcloud_test.go`
(built only with `-tags realcloud`, alongside — never replacing —
`aws_realcloud_test.go`). One run:

1. Creates one real, billed Compute Engine Spot instance (plus its boot
   disk) via the adapter's own `CreateWithResources`.
2. Independently confirms it reaches Compute Engine's real "RUNNING" status,
   using a second `compute.Service` client built through
   `google.golang.org/api/compute/v1`'s own default-credential resolution —
   never the adapter's own `newGCPSDK`/`gcpFileCredential` construction — so
   this confirmation never depends on the exact code path under
   qualification also certifying its own success.
3. Confirms the adapter's `Observe` reports the running instance as not
   interrupted.
4. Deletes the instance via the adapter's own `Delete`, then independently
   re-queries the GCP API (the same second client) for a leftover boot disk
   or any reserved address owned by this run's allocation labels — never
   just trusting `Delete`'s or `Observe`'s own report of success.
5. Uploads everything observed (creation receipt, running-state
   confirmation, observation results, the independent post-delete inventory
   query) as a workflow artifact, at
   `evidence/qualify-gcp-<run>-<attempt>/manifest.json`.

Real Spot **price** observation is not attempted — see "What this qualifies
— and what it honestly does not" below.

### Trigger and required inputs

`workflow_dispatch` only — no schedule, no push, no pull_request. Every
input below is required, with no default:

| Input | Meaning |
| --- | --- |
| `confirm_real_spend` | Must equal exactly `I-UNDERSTAND-THIS-COSTS-REAL-MONEY`. |
| `gcp_project` | Project to qualify against. Must equal `vars.GCP_QUALIFICATION_PROJECT_ID` (checked before any credential is configured). |
| `gcp_region` | Region to qualify against. |
| `gcp_zone` | Zone to launch into. Must belong to `gcp_region` (checked before any create call). |
| `gcp_network` | Pinned, pre-provisioned VPC network. `gcp_subnetwork` must belong to it (checked before any create call). |
| `gcp_subnetwork` | Pinned subnetwork to launch into. |
| `gcp_image` | Pinned boot image, as an exact image self-link — never a rolling `.../images/family/...` reference, which is not a pinned identifier. Resolved (existence-checked) before any create call. |
| `machine_type` | Keep this cheap — it directly bounds real spend alongside `max_runtime_minutes`. |
| `max_runtime_minutes` | Plain integer, 1–20. Bounds the create/observe phase. |

Nothing is auto-discovered: the image and every network identifier are
operator-supplied, per issue #3's "pinned images/networks" requirement.
Unlike AWS, GCP zones are not derivable from a subnetwork (a GCP subnetwork
is regional, not zonal, so any zone in the region is valid for it) — `gcp_zone`
is therefore its own required input, cross-checked against `gcp_region`
directly rather than read from another pinned identifier.

There is no `architecture` input: `internal/provider/gcp_sdk.go`'s
`createGCP` never reads `Offering.Architecture` at all (unlike AWS's
`createAWS`, which cross-validates the AMI's real architecture against the
input before creating) — adding an input the adapter does not consult would
document a safety property this piece does not actually have. See
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)
for why this is named as a real adapter-level gap rather than papered over.

### Hard bounds

- `max_runtime_minutes` is checked against a hard-coded workflow ceiling of
  **20** before anything else runs (before Go setup, before GCP credentials
  are even configured). No input can raise this ceiling.
- The job's own `timeout-minutes: 40` is a second, independent ceiling that
  does not derive from the input at all.
- Deletion runs on its own fixed 5-minute budget, independent of
  `max_runtime_minutes` — a run that spent its whole create/observe budget
  just confirming the VM works still gets a full, unhurried teardown
  attempt.
- `concurrency: { group: qualify-gcp, cancel-in-progress: false }` — only
  one real-cloud run at a time; a second dispatch queues rather than races
  or cancels an in-flight one.

### Guaranteed cleanup

Two independent layers, either one alone sufficient for the common case:

1. The Go test's own `t.Cleanup`, registered immediately once a real
   instance exists, running on its own fresh 5-minute context regardless of
   how much of the create/observe budget was already spent.
2. The workflow's own final step, gated `if: always()` — the authoritative
   backstop, since it runs even if the job was killed by its own timeout or
   cancelled before the Go process's `t.Cleanup` could run. It re-derives
   everything it needs (project, zone, region, allocation id, owner label)
   directly from job inputs/env, never from a prior step's output, so it
   still works when every earlier step failed. It independently re-queries
   for any instance/disk/reserved-address still labeled to this run and
   force-cleans anything found, then fails the job loudly (that condition is
   a real bug, never a normal outcome).

### Credentials

Workload Identity Federation only — deliberately no service account key
fallback (see
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)
for why this genuinely differs from AWS's OIDC-preferred/static-key-fallback
pair). Three repository **variables** (not secrets — none of these values
are sensitive on their own; GCP's WIF trust policy is what actually gates
access) already exist in this repository:

- `GCP_QUALIFICATION_PROJECT_ID` — the project the qualification identity is
  provisioned in.
- `GCP_QUALIFICATION_SERVICE_ACCOUNT` — the service account email the
  workflow impersonates.
- `GCP_QUALIFICATION_WORKLOAD_IDENTITY_PROVIDER` — the full WIF provider
  resource name.

`google-github-actions/auth` assumes this identity via
`workload_identity_provider`/`service_account`, using the job's own GitHub
Actions OIDC identity (`id-token: write`, granted only to this job), and
exports `GOOGLE_APPLICATION_CREDENTIALS`/
`CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE` pointing at the generated
credential file — exactly the environment shape
`internal/provider/gcp_credentials.go`'s `gcpCredential` already reads from
a nil environment map, so no branching is needed anywhere below that step.

#### Minimum IAM permissions

The impersonated service account needs, scoped to the pinned
project/region/zone: `compute.zones.get`, `compute.subnetworks.get`,
`compute.images.get`, `compute.instances.get`, `compute.instances.list`,
`compute.disks.get`, `compute.disks.list`, `compute.addresses.list`,
`compute.zoneOperations.get`, `compute.zoneOperations.list`;
`compute.instances.create`, `compute.instances.delete`,
`compute.disks.create`, `compute.disks.delete`,
`compute.instances.setLabels`, `compute.disks.setLabels`,
`compute.subnetworks.use`. No project-creation, IAM, VPC-creation or
billing permissions are needed — the network/subnetwork are pre-provisioned
and only ever read, never created or modified, by this workflow.

### Operator prerequisites

Before dispatching this workflow, an operator must have already:

1. Provisioned an isolated VPC network and subnetwork dedicated to
   qualification (never a production network).
2. Chosen a pinned boot image self-link.
3. Chosen a cheap machine type.
4. Provisioned the Workload Identity Federation pool/provider, the
   qualification service account, and set the three repository variables
   above, granting the minimum permissions listed.

### What this qualifies — and what it honestly does not

Qualified, for real, by a passing run:

- Real Compute Engine Spot creation, a real transition to "RUNNING", real
  deletion, and independently re-verified cleanup — all through the exact
  production adapter code path.

Named gaps, not covered by this piece:

- **Real GitHub Actions job execution** (issue #3's "ordinary runs-on,
  actual VM job execution"). The VM boots with a synthetic, non-functional
  placeholder in place of a real GitHub JIT registration token — the
  image's startup-script runner-registration step is expected to fail
  harmlessly inside the guest. Confirming a real runner registers and
  executes a real workflow job needs a live scale set and a real ephemeral
  registration token — a separate, larger qualification piece this workflow
  does not attempt.
- **Real GCP Spot price observation** (issue #2). No `internal/prices` GCP
  client exists: Compute Engine's public pricing surface splits Core/RAM
  SKUs with no structured machine-type field, no zone-level pricing and no
  request-side filter, so a GCP price client was assessed as not honestly
  buildable without guessing — see
  [../internal/prices/gcp.md](../internal/prices/gcp.md) and
  [../internal/prices/gcp.background.md](../internal/prices/gcp.background.md).
  This is a named, tracked gap, not a silently skipped step.
- **A real Spot preemption.** GCP provides no supported, on-demand API to
  force one. This workflow can only confirm the adapter correctly reports
  "not interrupted" against a real, healthy running instance; the
  `gcpConfirmedPreemption` detection logic itself is covered separately,
  against synthesized operation payloads, by the existing unit test suite.
- **Architecture/image compatibility.** `createGCP` never cross-validates
  `machine_type` against `gcp_image`'s real architecture the way AWS's
  adapter does for `instance_type`/`ami_id` — an operator error here fails
  at VM boot, not at this workflow's pre-flight.
- **Azure.** A separate, later piece of issue #3's same remaining scope.

See [qualification-real-cloud.background.md](qualification-real-cloud.background.md)
for the reasoning behind these design choices.
