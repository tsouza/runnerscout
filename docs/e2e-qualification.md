# Full end-to-end qualification: AWS and Azure

Part of [issue #80](https://github.com/tsouza/runnerscout/issues/80), the
piece [issue #3](https://github.com/tsouza/runnerscout/issues/3)'s own
real-cloud qualification (`qualify-aws.yml`/`qualify-azure.yml`/
`qualify-gcp.yml`, see
[qualification-real-cloud.md](qualification-real-cloud.md)) deliberately
stopped short of: those three each use a synthetic, non-functional
`Bootstrap` placeholder instead of a real GitHub JIT registration token, so
none of them proves a real runner actually registers and executes a real
workflow job. This harness closes that gap for AWS and Azure. GCP is a
separate, later, not-yet-built piece of the same issue - see issue #80's own
scoping recommendation.

One run of this harness, for a given cloud:

1. Registers (idempotent-safe) one real GitHub Actions runner scale set
   against a real, operator-chosen test repository or organization - the
   same step and the same program (`cmd/e2e-register-scale-set`) regardless
   of which cloud is being qualified, since scale-set registration is a
   GitHub-side concept, not a cloud-side one.
2. Brings up a local k3d cluster running the actual `runnerscout` controller
   image, with a real `ProviderConfig`/`RunnerClass`/`RunnerScaleSet`/
   `CapacityCatalog`/`NetworkProfile` resource graph pointed at the same
   pinned qualification network
   [qualification-real-cloud.md](qualification-real-cloud.md)'s AWS or Azure
   section documents.
3. Refreshes the `CapacityCatalog` with a real, freshly observed Spot price,
   then triggers a real `workflow_dispatch` job against the test repository.
4. Waits for the controller to admit the job, place a real billed cloud Spot
   VM, for that VM's cloud-init to register a real ephemeral runner via a
   real JIT token (`internal/operator/operator.go`'s `Bootstrap` closure -
   already the production path, no placeholder involved here), and for the
   dispatched job to complete on it.
5. Independently confirms, via `kubectl` (the fleet ConfigMap's own
   allocation phase) and a second, independent cloud CLI invocation (never
   the controller's own session), that the allocation reached `deleted` and
   zero billable cloud resource survived.
6. Tears down the cluster and force-cleans any leftover cloud resource on
   every path, including failure and timeout.

This is a **local runbook**, not a GitHub Actions workflow - see
[e2e-qualification.background.md](e2e-qualification.background.md) for why.

The AWS and Azure pieces share the same GitHub-side registration script
(`tools/e2e/register-scale-set.sh`) and the same safety-gate library
(`tools/e2e/lib.sh`), but every other stage is a separate script per cloud -
`tools/e2e/*.sh` (no suffix) for AWS, `tools/e2e/*-azure.sh` for Azure - and
a separate manifest set per cloud under `tools/e2e/manifests/` (AWS
templates have no suffix, Azure templates end in `-azure.yaml.tmpl`). Run
exactly one cloud's scripts per invocation of this harness; the two never
run against the same k3d cluster/namespace at once in this design (see
[e2e-qualification.background.md](e2e-qualification.background.md) for why
each cloud got its own scripts rather than one parameterized set).

## What an operator must provision before running this

1. A real, dedicated test repository (or organization) - never a production
   repository - containing one trivial `workflow_dispatch` workflow (e.g. a
   single `echo` step) that requests this scale set's runner label. Keeping
   that workflow trivial bounds real runtime/spend once dispatched.
2. A GitHub App (with scale-set permissions) or a PAT with scale-set and
   `workflow_dispatch` permissions on that repository.
3. **AWS**: the same pinned, isolated VPC/subnet/security group/AMI
   documented in [qualification-real-cloud.md](qualification-real-cloud.md)'s
   AWS section - reuse it, do not provision a second one. An AWS
   credentials file (shared-credentials format, `[runnerscout]` profile)
   with the same minimum EC2 permissions that section lists, plus
   `ec2:DescribeSpotPriceHistory` (already listed there) for the catalog
   refresh.
   **Azure**: the same pinned, isolated resource group/VNet/subnet/network
   security group/managed image documented in
   [qualification-real-cloud.md](qualification-real-cloud.md)'s Azure
   section - reuse it, do not provision a second one. An Azure AD app
   registration with a federated credential (Workload Identity Federation -
   the only supported path here, see that section for why there is no
   client-secret fallback) granted the minimum RBAC permissions that
   section lists, and a locally obtained federated OIDC token file this
   harness can present as `AZURE_FEDERATED_TOKEN_FILE` (how an operator
   obtains one locally - e.g. from their own CI identity, or any other
   token acceptable to that same federated-credential trust relationship -
   is outside this harness's scope, exactly like how an operator obtains
   `E2E_AWS_CREDENTIALS_FILE`'s contents is outside the AWS piece's scope).
   An authenticated `az` CLI session (`az login` or equivalent) must also
   already be active in the shell invoking `bring-up-azure.sh`/
   `dispatch-and-wait-azure.sh`/`teardown-azure.sh` - none of them run
   `az login` themselves.
4. `k3d`, `kubectl`, `helm`, `docker`, `gh`, `jq`, `envsubst` and `go`
   installed locally, plus the `aws` CLI for the AWS piece or the `az` CLI
   for the Azure piece.
5. A k3d node image satisfying `charts/runnerscout/Chart.yaml`'s
   `kubeVersion: ">=1.37.0-0 <1.38.0-0"` constraint (k3d's own default image
   may trail this - see `tools/e2e/bring-up.sh`/`bring-up-azure.sh`'s
   `E2E_K3D_IMAGE`).

## Running it

```sh
cp tools/e2e/env.example tools/e2e/.env   # gitignored; fill in real values
source tools/e2e/.env

tools/e2e/register-scale-set.sh           # prints the scale set's numeric ID
export E2E_REGISTERED_SCALE_SET_ID=<id>   # from the line above

tools/e2e/run.sh                          # AWS: bring-up, dispatch, wait, teardown
tools/e2e/run-azure.sh                    # Azure: same, run instead of (not in addition to) run.sh
```

`tools/e2e/run.sh`/`run-azure.sh` always attempt teardown on exit, success
or failure. To run the stages individually instead (e.g. to inspect cluster
state between bring-up and dispatch), source `tools/e2e/.env`, then run the
per-cloud bring-up script, then that cloud's dispatch-and-wait script, then
its verify and/or teardown script yourself:

```sh
tools/e2e/bring-up.sh           tools/e2e/bring-up-azure.sh
tools/e2e/dispatch-and-wait.sh  tools/e2e/dispatch-and-wait-azure.sh
tools/e2e/verify.sh             tools/e2e/verify-azure.sh
tools/e2e/teardown.sh           tools/e2e/teardown-azure.sh
```

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
  `qualify-aws.yml`/`qualify-azure.yml`/`qualify-gcp.yml` require for
  `confirm_real_spend`.
- **Hard numeric runtime ceiling.** `E2E_MAX_RUNTIME_MINUTES` must be a plain
  integer, 1-60 (`tools/e2e/lib.sh`'s hard-coded ceiling - higher than
  `qualify-aws.yml`'s/`qualify-azure.yml`'s 20, since this harness does
  strictly more: cluster bring-up, scale-set registration, a real
  dispatched job, and real runner boot/registration/job pickup on top of VM
  creation). Every stage re-derives its own deadline from this value rather
  than trusting an earlier stage's already-validated copy.
- **Pinned network identifiers only.** Every cloud identifier is a required
  operator input, cross-validated read-only against the real cloud API
  before any create call, exactly like `qualify-aws.yml`'s/
  `qualify-azure.yml`'s own pre-flight.
- **Idempotent-safe scale-set registration.** `tools/e2e/register-scale-set.sh`
  always looks up the named scale set before creating or deleting one -
  running it twice never creates a duplicate, and `--delete` against an
  already-absent name is a no-op, not an error.
- **Trivial dispatched job.** The harness never authors the target
  workflow's content - the operator prerequisite above asks for a trivial
  one - but nothing here removes an operator's ability to point this at a
  larger job; keeping it trivial is a documented expectation, not an
  enforced one (see [e2e-qualification.background.md](e2e-qualification.background.md)).
- **Guaranteed teardown on every path.** `tools/e2e/run.sh`/`run-azure.sh`
  install their teardown call under `trap ... EXIT` before doing anything
  else billable, so it runs whether bring-up, dispatch, or the wait loop
  succeeds, fails, or hits the runtime ceiling. `tools/e2e/teardown.sh`/
  `teardown-azure.sh` themselves never use `set -e`: every step (RunnerScaleSet
  deletion, Helm uninstall, cloud-side force-cleanup, cluster deletion) is
  attempted regardless of whether an earlier one failed, and each exits
  non-zero - loudly - only if independent cloud-side verification still
  finds a leftover billable resource after force-cleanup, exactly like
  `qualify-aws.yml`'s/`qualify-azure.yml`'s own safety-net step.
- **This is never run automatically.** Nothing in this harness is wired into
  CI, a cron, or any other automatic trigger - every stage requires an
  operator to source real credentials and invoke a script by hand.

## Environment variables

See `tools/e2e/env.example` for the complete, commented list with defaults,
covering both clouds. Each `tools/e2e/*.sh` script's own header comment
lists exactly the variables that script reads.

## What this qualifies - and what it honestly does not

Qualified, for real, by a passing run of either cloud's scripts:

- A real GitHub Actions runner scale set registration.
- The real CRD-driven controller resource graph (`ProviderConfig`/
  `RunnerClass`/`RunnerScaleSet`/`CapacityCatalog`/`NetworkProfile`) admitting
  a real job against real AWS Spot capacity, or real Azure Spot capacity.
- Real JIT token issuance, real cloud-init runner registration, and real job
  execution on a real EC2 Spot Instance, or a real Azure Spot Virtual
  Machine.
- Independently re-verified Kubernetes-side allocation cleanup and
  cloud-side resource cleanup.

Named gaps, not covered by this piece:

- **GCP.** A separate, later piece of the same issue, per its own first-pass
  scoping recommendation.
- **Spot/eviction interruption during a real job.** Like `qualify-aws.yml`/
  `qualify-azure.yml`, this harness cannot force a real Spot
  interruption/eviction on demand - it only exercises the ordinary,
  uninterrupted completion path.
- **Retry/rerun composition.** The resource graph's `RunnerClass.retry` stays
  disabled, matching every other qualification piece in this repository -
  `internal/recovery`'s retry composition has its own separate qualification
  surface, not exercised here.
- **WireGuard networking.** The resource graph uses `separate` mode - see
  [operations.md](operations.md)'s "Known limitations": `wireguard` mode is
  not yet functionally usable end-to-end.
- **Azure's `-1` "no price cap" sentinel.** `azure_realcloud_test.go` sets
  `Requirements.MaxPriceMicros = -1_000_000` directly on a hand-built
  Allocation to mean "no cap" (see
  [qualification-real-cloud.background.md](qualification-real-cloud.background.md)'s
  Azure section). A CRD-driven `RunnerClass.spec.placement.maxPriceMicros`
  cannot express that sentinel - it carries its own
  `+kubebuilder:validation:Minimum=1` - so the Azure piece here always uses
  a real, finite price ceiling instead, exactly like the AWS piece already
  does.

See [e2e-qualification.background.md](e2e-qualification.background.md) for
the reasoning behind the design choices above, including the local-runbook
decision and why AWS and Azure got separate scripts instead of one
parameterized set.
