# Real-cloud qualification: AWS

Part of [issue #3](https://github.com/tsouza/runnerscout/issues/3)'s remaining
"Final real-cloud qualification" scope, plus [issue #2](https://github.com/tsouza/runnerscout/issues/2)'s
related "real provider pricing requires later qualification" ask. This
covers the AWS provider adapter only. Azure and GCP are separate, later,
not-yet-built pieces of the same remaining scope — see "Known gaps" below.

## What it does

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

## Trigger and required inputs

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

## Hard bounds

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

## Guaranteed cleanup

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

## Credentials

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

None of these secrets/variables exist in this repository today. Referencing
them by name is inert until an operator deliberately provisions them —
dispatching this workflow before then fails cleanly with a "secret/variable
not found"-style error.

### Minimum IAM permissions

The assumed role or IAM user needs, scoped to the pinned region/account:
`sts:GetCallerIdentity`; `ec2:DescribeSubnets`, `ec2:DescribeSecurityGroups`,
`ec2:DescribeImages`, `ec2:DescribeInstances`, `ec2:DescribeVolumes`,
`ec2:DescribeNetworkInterfaces`, `ec2:DescribeSpotPriceHistory`;
`ec2:RunInstances`, `ec2:TerminateInstances`, `ec2:DeleteVolume`,
`ec2:DeleteNetworkInterface`. No IAM, VPC-creation or billing permissions
are needed — the VPC/subnet/security group are pre-provisioned and only
ever read, never created or modified, by this workflow.

## Operator prerequisites

Before dispatching this workflow, an operator must have already:

1. Provisioned an isolated VPC, subnet and security group dedicated to
   qualification (never a production network).
2. Chosen a pinned, EBS-backed AMI and confirmed its real architecture.
3. Chosen a cheap instance type consistent with that architecture.
4. Set either `AWS_QUALIFICATION_ROLE_ARN` (preferred) or the static-key
   secret pair above, granting the minimum permissions listed.

## What this qualifies — and what it honestly does not

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
- **Azure, GCP.** Separate, later pieces of issue #3's same remaining
  scope.

See [qualification-real-cloud.background.md](qualification-real-cloud.background.md)
for the reasoning behind these design choices.
