# Full end-to-end qualification: AWS

Part of [issue #80](https://github.com/tsouza/runnerscout/issues/80), the
piece [issue #3](https://github.com/tsouza/runnerscout/issues/3)'s own
real-cloud qualification (`qualify-aws.yml`/`qualify-azure.yml`/
`qualify-gcp.yml`, see
[qualification-real-cloud.md](qualification-real-cloud.md)) deliberately
stopped short of: those three each use a synthetic, non-functional
`Bootstrap` placeholder instead of a real GitHub JIT registration token, so
none of them proves a real runner actually registers and executes a real
workflow job. This harness closes that gap for AWS. Azure and GCP are a
separate, later, not-yet-built piece of the same issue - see issue #80's own
scoping recommendation.

One run of this harness:

1. Registers (idempotent-safe) one real GitHub Actions runner scale set
   against a real, operator-chosen test repository or organization.
2. Brings up a local k3d cluster running the actual `runnerscout` controller
   image, with a real `ProviderConfig`/`RunnerClass`/`RunnerScaleSet`/
   `CapacityCatalog`/`NetworkProfile` resource graph pointed at the same
   pinned AWS qualification network [qualification-real-cloud.md](qualification-real-cloud.md)'s
   AWS section documents.
3. Refreshes the `CapacityCatalog` with a real, freshly observed Spot price,
   then triggers a real `workflow_dispatch` job against the test repository.
4. Waits for the controller to admit the job, place a real billed EC2 Spot
   Instance, for that VM's cloud-init to register a real ephemeral runner via
   a real JIT token (`internal/operator/operator.go`'s `Bootstrap` closure -
   already the production path, no placeholder involved here), and for the
   dispatched job to complete on it.
5. Independently confirms, via `kubectl` (the fleet ConfigMap's own allocation
   phase) and a second AWS CLI invocation (never the controller's own
   session), that the allocation reached `deleted` and zero billable EC2
   resource survived.
6. Tears down the cluster and force-cleans any leftover AWS resource on every
   path, including failure and timeout.

This is a **local runbook**, not a GitHub Actions workflow - see
[e2e-qualification.background.md](e2e-qualification.background.md) for why.

## What an operator must provision before running this

1. A real, dedicated test repository (or organization) - never a production
   repository - containing one trivial `workflow_dispatch` workflow (e.g. a
   single `echo` step) that requests this scale set's runner label. Keeping
   that workflow trivial bounds real runtime/spend once dispatched.
2. A GitHub App (with scale-set permissions) or a PAT with scale-set and
   `workflow_dispatch` permissions on that repository.
3. The same pinned, isolated AWS VPC/subnet/security group/AMI documented in
   [qualification-real-cloud.md](qualification-real-cloud.md)'s AWS section -
   reuse it, do not provision a second one.
4. An AWS credentials file (shared-credentials format, `[runnerscout]`
   profile) with the same minimum EC2 permissions
   [qualification-real-cloud.md](qualification-real-cloud.md)'s AWS section
   lists, plus `ec2:DescribeSpotPriceHistory` (already listed there) for the
   catalog refresh.
5. `k3d`, `kubectl`, `helm`, `docker`, `aws`, `gh`, `jq`, `envsubst` and `go`
   installed locally.
6. A k3d node image satisfying `charts/runnerscout/Chart.yaml`'s
   `kubeVersion: ">=1.37.0-0 <1.38.0-0"` constraint (k3d's own default image
   may trail this - see `tools/e2e/bring-up.sh`'s `E2E_K3D_IMAGE`).

## Running it

```sh
cp tools/e2e/env.example tools/e2e/.env   # gitignored; fill in real values
source tools/e2e/.env

tools/e2e/register-scale-set.sh           # prints the scale set's numeric ID
export E2E_REGISTERED_SCALE_SET_ID=<id>   # from the line above

tools/e2e/run.sh                          # bring-up, dispatch, wait, teardown
```

`tools/e2e/run.sh` always attempts teardown on exit, success or failure. To
run the stages individually instead (e.g. to inspect cluster state between
bring-up and dispatch), source `tools/e2e/.env`, run
`tools/e2e/bring-up.sh`, then `tools/e2e/dispatch-and-wait.sh`, then
`tools/e2e/verify.sh` and/or `tools/e2e/teardown.sh` yourself.

To remove the registered scale set once done with the harness entirely
(optional - see [e2e-qualification.background.md](e2e-qualification.background.md)
for why teardown does not do this automatically):

```sh
tools/e2e/register-scale-set.sh --delete
```

## Safety bounds

- **Explicit spend confirmation.** Every script that can reach a billable or
  GitHub-mutating call refuses to run unless `E2E_CONFIRM_REAL_SPEND` is
  exactly `I-UNDERSTAND-THIS-COSTS-REAL-MONEY` - the same literal phrase
  `qualify-aws.yml`/`qualify-gcp.yml` require for `confirm_real_spend`.
- **Hard numeric runtime ceiling.** `E2E_MAX_RUNTIME_MINUTES` must be a plain
  integer, 1-60 (`tools/e2e/lib.sh`'s hard-coded ceiling - higher than
  `qualify-aws.yml`'s 20, since this harness does strictly more: cluster
  bring-up, scale-set registration, a real dispatched job, and real runner
  boot/registration/job pickup on top of VM creation). Every stage
  re-derives its own deadline from this value rather than trusting an
  earlier stage's already-validated copy.
- **Pinned network identifiers only.** Every AWS identifier is a required
  operator input, cross-validated read-only against the real AWS API before
  any create call, exactly like `qualify-aws.yml`'s own pre-flight.
- **Idempotent-safe scale-set registration.** `tools/e2e/register-scale-set.sh`
  always looks up the named scale set before creating or deleting one -
  running it twice never creates a duplicate, and `--delete` against an
  already-absent name is a no-op, not an error.
- **Trivial dispatched job.** The harness never authors the target
  workflow's content - the operator prerequisite above asks for a trivial
  one - but nothing here removes an operator's ability to point this at a
  larger job; keeping it trivial is a documented expectation, not an
  enforced one (see [e2e-qualification.background.md](e2e-qualification.background.md)).
- **Guaranteed teardown on every path.** `tools/e2e/run.sh` installs its
  teardown call under `trap ... EXIT` before doing anything else billable,
  so it runs whether bring-up, dispatch, or the wait loop succeeds, fails,
  or hits the runtime ceiling. `tools/e2e/teardown.sh` itself never uses
  `set -e`: every step (RunnerScaleSet deletion, Helm uninstall, AWS-side
  force-cleanup, cluster deletion) is attempted regardless of whether an
  earlier one failed, and it exits non-zero - loudly - only if independent
  AWS-side verification still finds a leftover billable resource after
  force-cleanup, exactly like `qualify-aws.yml`'s own safety-net step.
- **This is never run automatically.** Nothing in this harness is wired into
  CI, a cron, or any other automatic trigger - every stage requires an
  operator to source real credentials and invoke a script by hand.

## Environment variables

See `tools/e2e/env.example` for the complete, commented list with defaults.
Each `tools/e2e/*.sh` script's own header comment lists exactly the
variables that script reads.

## What this qualifies - and what it honestly does not

Qualified, for real, by a passing run:

- A real GitHub Actions runner scale set registration.
- The real CRD-driven controller resource graph (`ProviderConfig`/
  `RunnerClass`/`RunnerScaleSet`/`CapacityCatalog`/`NetworkProfile`) admitting
  a real job against real AWS Spot capacity.
- Real JIT token issuance, real cloud-init runner registration, and real job
  execution on a real EC2 Spot Instance.
- Independently re-verified Kubernetes-side allocation cleanup and AWS-side
  resource cleanup.

Named gaps, not covered by this piece:

- **Azure and GCP.** A separate, later piece of the same issue, per its own
  first-pass scoping recommendation.
- **Spot interruption during a real job.** Like `qualify-aws.yml`, this
  harness cannot force a real Spot interruption on demand - it only exercises
  the ordinary, uninterrupted completion path.
- **Retry/rerun composition.** The resource graph's `RunnerClass.retry` stays
  disabled, matching every other qualification piece in this repository -
  `internal/recovery`'s retry composition has its own separate qualification
  surface, not exercised here.
- **WireGuard networking.** The resource graph uses `separate` mode - see
  [operations.md](operations.md)'s "Known limitations": `wireguard` mode is
  not yet functionally usable end-to-end.

See [e2e-qualification.background.md](e2e-qualification.background.md) for
the reasoning behind the design choices above, including the local-runbook
decision.
