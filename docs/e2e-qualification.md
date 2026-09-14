# Full end-to-end qualification: AWS, Azure and GCP

Part of [issue #80](https://github.com/tsouza/runnerscout/issues/80), the
piece [issue #3](https://github.com/tsouza/runnerscout/issues/3)'s own
real-cloud qualification (`qualify-aws.yml`/`qualify-azure.yml`/
`qualify-gcp.yml`, see
[qualification-real-cloud.md](qualification-real-cloud.md)) deliberately
stopped short of: those three each use a synthetic, non-functional
`Bootstrap` placeholder instead of a real GitHub JIT registration token, so
none of them proves a real runner actually registers and executes a real
workflow job. This harness closes that gap for all three providers - see
each provider's own section below for what an operator must provision and
what remains out of scope even after a passing run.

One run of this harness, for a given provider:

1. Registers (idempotent-safe, via `tools/e2e/register-scale-set.sh` -
   cloud-agnostic, shared by every provider) one real GitHub Actions runner
   scale set against a real, operator-chosen test repository or
   organization.
2. Brings up a local k3d cluster running the actual `runnerscout` controller
   image, with a real `ProviderConfig`/`RunnerClass`/`RunnerScaleSet`/
   `CapacityCatalog`/`NetworkProfile` resource graph pointed at the same
   pinned qualification network
   [qualification-real-cloud.md](qualification-real-cloud.md)'s own section
   for that provider documents.
3. Refreshes the `CapacityCatalog` (a real, freshly observed Spot price for
   AWS and Azure; a fixed static price and a refreshed timestamp for GCP -
   see the GCP section below), then triggers a real `workflow_dispatch` job
   against the test repository.
4. Waits for the controller to admit the job, place a real billed Spot VM,
   for that VM's cloud-init/startup-script to register a real ephemeral
   runner via a real JIT token (`internal/operator/operator.go`'s
   `Bootstrap` closure - already the production path, no placeholder
   involved here), and for the dispatched job to complete on it.
5. Independently confirms, via `kubectl` (the fleet ConfigMap's own
   allocation phase) and a second, separately authenticated cloud CLI
   invocation (never the controller's own session), that the allocation
   reached `deleted` and zero billable cloud resource survived.
6. Tears down the cluster and force-cleans any leftover cloud resource on
   every path, including failure and timeout.

This is a **local runbook**, not a GitHub Actions workflow - see
[e2e-qualification.background.md](e2e-qualification.background.md) for why.

## Shared safety bounds

Every provider's scripts source `tools/e2e/lib.sh`, which enforces the same
two speed bumps regardless of which cloud is being qualified:

- **Explicit spend confirmation.** Every script that can reach a billable or
  GitHub-mutating call refuses to run unless `E2E_CONFIRM_REAL_SPEND` is
  exactly `I-UNDERSTAND-THIS-COSTS-REAL-MONEY` - the same literal phrase
  `qualify-aws.yml`/`qualify-azure.yml`/`qualify-gcp.yml` require for
  `confirm_real_spend`.
- **Hard numeric runtime ceiling.** `E2E_MAX_RUNTIME_MINUTES` must be a plain
  integer, 1-60 (`tools/e2e/lib.sh`'s hard-coded ceiling - higher than
  `qualify-aws.yml`'s/`qualify-azure.yml`'s/`qualify-gcp.yml`'s 20, since
  this harness does strictly more: cluster bring-up, scale-set registration,
  a real dispatched job, and real runner boot/registration/job pickup on top
  of VM creation). Every stage re-derives its own deadline from this value
  rather than trusting an earlier stage's already-validated copy.
- **Pinned network identifiers only.** Every cloud identifier is a required
  operator input, cross-validated read-only against the real cloud API
  before any create call, exactly like each provider's own
  `qualify-*.yml`'s pre-flight.
- **Idempotent-safe scale-set registration.** `tools/e2e/register-scale-set.sh`
  always looks up the named scale set before creating or deleting one -
  running it twice never creates a duplicate, and `--delete` against an
  already-absent name is a no-op, not an error. This script and the program
  it wraps (`cmd/e2e-register-scale-set`) are cloud-agnostic and shared
  unmodified by every provider.
- **Trivial dispatched job.** The harness never authors the target
  workflow's content - the operator prerequisite below asks for a trivial
  one - but nothing here removes an operator's ability to point this at a
  larger job; keeping it trivial is a documented expectation, not an
  enforced one (see [e2e-qualification.background.md](e2e-qualification.background.md)).
- **Guaranteed teardown on every path.** Each provider's `run-<provider>.sh`
  (or `run.sh` for AWS) installs its teardown call under `trap ... EXIT`
  before doing anything else billable, so it runs whether bring-up,
  dispatch, or the wait loop succeeds, fails, or hits the runtime ceiling.
  Each provider's own `teardown-<provider>.sh`/`teardown.sh` never uses
  `set -e`: every step is attempted regardless of whether an earlier one
  failed, and it exits non-zero - loudly - only if independent cloud-side
  verification still finds a leftover billable resource after
  force-cleanup, exactly like each provider's own `qualify-*.yml` safety-net
  step.
- **This is never run automatically.** Nothing in this harness is wired into
  CI, a cron, or any other automatic trigger - every stage requires an
  operator to source real credentials and invoke a script by hand.

## AWS

`tools/e2e/bring-up.sh`, `tools/e2e/dispatch-and-wait.sh`,
`tools/e2e/verify.sh` and `tools/e2e/teardown.sh` wire the resource graph to
a real AWS EC2 Spot instance, orchestrated end to end by `tools/e2e/run.sh`.

### What an operator must provision before running the AWS harness

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

### Running the AWS harness

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

### AWS environment variables

See `tools/e2e/env.example` for the complete, commented list with defaults.
Each `tools/e2e/*.sh` script's own header comment lists exactly the
variables that script reads.

### What the AWS pass qualifies - and what it honestly does not

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

## Azure

`tools/e2e/bring-up-azure.sh`, `tools/e2e/dispatch-and-wait-azure.sh`,
`tools/e2e/verify-azure.sh` and `tools/e2e/teardown-azure.sh` wire the
resource graph to a real Azure Spot Virtual Machine, orchestrated end to end
by `tools/e2e/run-azure.sh`. These mirror the AWS scripts' exact structure
and safety bounds, adapted to Azure's real API/CLI shapes (`az`, ARM
resource IDs, tag-based ownership) rather than reusing AWS's own unsuffixed
script names - see
[e2e-qualification.background.md](e2e-qualification.background.md) for why
per-provider scripts, not one parameterized set.

### What an operator must provision before running the Azure harness

1. A real, dedicated test repository (or organization) - never a production
   repository - containing one trivial `workflow_dispatch` workflow (e.g. a
   single `echo` step) that requests this scale set's runner label. Keeping
   that workflow trivial bounds real runtime/spend once dispatched.
2. A GitHub App (with scale-set permissions) or a PAT with scale-set and
   `workflow_dispatch` permissions on that repository.
3. The same pinned, isolated resource group/VNet/subnet/network security
   group/managed image documented in
   [qualification-real-cloud.md](qualification-real-cloud.md)'s Azure
   section - reuse it, do not provision a second one.
4. An Azure AD app registration with a federated credential (Workload
   Identity Federation - the only supported path here, see that section for
   why there is no client-secret fallback) granted the minimum RBAC
   permissions that section lists, and a locally obtained federated OIDC
   token file this harness can present as `E2E_AZURE_FEDERATED_TOKEN_FILE`
   (how an operator obtains one locally - e.g. from their own CI identity, or
   any other token acceptable to that same federated-credential trust
   relationship - is outside this harness's scope, exactly like how an
   operator obtains `E2E_AWS_CREDENTIALS_FILE`'s contents is outside the AWS
   piece's scope). An authenticated `az` CLI session (`az login` or
   equivalent) must also already be active in the shell invoking
   `bring-up-azure.sh`/`dispatch-and-wait-azure.sh`/`teardown-azure.sh` -
   none of them run `az login` themselves.
5. `k3d`, `kubectl`, `helm`, `docker`, `az`, `gh`, `jq`, `envsubst` and `go`
   installed locally.
6. A k3d node image satisfying `charts/runnerscout/Chart.yaml`'s
   `kubeVersion: ">=1.37.0-0 <1.38.0-0"` constraint (k3d's own default image
   may trail this - see `tools/e2e/bring-up-azure.sh`'s `E2E_K3D_IMAGE`).

### Running the Azure harness

```sh
cp tools/e2e/env.example tools/e2e/.env   # gitignored; fill in real values (Azure section)
source tools/e2e/.env

tools/e2e/register-scale-set.sh           # prints the scale set's numeric ID
export E2E_REGISTERED_SCALE_SET_ID=<id>   # from the line above

tools/e2e/run-azure.sh                    # bring-up, dispatch, wait, teardown
```

`tools/e2e/run-azure.sh` always attempts teardown on exit, success or
failure. To run the stages individually instead (e.g. to inspect cluster
state between bring-up and dispatch), source `tools/e2e/.env`, run
`tools/e2e/bring-up-azure.sh`, then `tools/e2e/dispatch-and-wait-azure.sh`,
then `tools/e2e/verify-azure.sh` and/or `tools/e2e/teardown-azure.sh`
yourself.

To remove the registered scale set once done with the harness entirely
(optional - see [e2e-qualification.background.md](e2e-qualification.background.md)
for why teardown does not do this automatically):

```sh
tools/e2e/register-scale-set.sh --delete
```

### Azure environment variables

See `tools/e2e/env.example` for the complete, commented list with defaults
(covering both the AWS and Azure sections - fill in only the section for the
provider you are actually qualifying). Each `tools/e2e/*-azure.sh` script's
own header comment lists exactly the variables that script reads.

### What the Azure pass qualifies - and what it honestly does not

Qualified, for real, by a passing run:

- A real GitHub Actions runner scale set registration.
- The real CRD-driven controller resource graph (`ProviderConfig`/
  `RunnerClass`/`RunnerScaleSet`/`CapacityCatalog`/`NetworkProfile`) admitting
  a real job against real Azure Spot capacity.
- Real JIT token issuance, real cloud-init runner registration, and real job
  execution on a real Azure Spot Virtual Machine.
- Independently re-verified Kubernetes-side allocation cleanup and
  Azure-side resource cleanup.

Named gaps, not covered by this piece:

- **Spot eviction during a real job.** Like `qualify-azure.yml`, this harness
  cannot force a real Spot eviction on demand (only the separate,
  unintegrated Azure Chaos Studio service can) - it only exercises the
  ordinary, uninterrupted completion path.
- **Retry/rerun composition.** The resource graph's `RunnerClass.retry` stays
  disabled, matching every other qualification piece in this repository.
- **WireGuard networking.** The resource graph uses `separate` mode - see
  [operations.md](operations.md)'s "Known limitations": `wireguard` mode is
  not yet functionally usable end-to-end.
- **Azure's `-1` "no price cap" sentinel.** `azure_realcloud_test.go` sets
  `Requirements.MaxPriceMicros = -1_000_000` directly on a hand-built
  Allocation to mean "no cap" (see
  [qualification-real-cloud.background.md](qualification-real-cloud.background.md)'s
  Azure section). A CRD-driven `RunnerClass.spec.placement.maxPriceMicros`
  cannot express that sentinel - it carries its own
  `+kubebuilder:validation:Minimum=1` - so this harness always uses a real,
  finite price ceiling instead, exactly like the AWS piece already does.

## GCP

`tools/e2e/bring-up-gcp.sh`, `tools/e2e/dispatch-and-wait-gcp.sh`,
`tools/e2e/verify-gcp.sh` and `tools/e2e/teardown-gcp.sh` wire the resource
graph to a real GCP Compute Engine Spot instance, orchestrated end to end by
`tools/e2e/run-gcp.sh`. These mirror the AWS scripts' exact structure and
safety bounds, adapted to GCP's real API/CLI shapes (`gcloud`, GCP resource
paths, label-based ownership) rather than reusing AWS's own unsuffixed
script names - see
[e2e-qualification.background.md](e2e-qualification.background.md) for why
per-provider scripts, not one parameterized set, and for the one place GCP's
own harness genuinely differs from AWS's: pricing.

### What an operator must provision before running the GCP harness

1. A real, dedicated test repository (or organization) - never a production
   repository - containing one trivial `workflow_dispatch` workflow (e.g. a
   single `echo` step) that requests this scale set's runner label. Keeping
   that workflow trivial bounds real runtime/spend once dispatched.
2. A GitHub App (with scale-set permissions) or a PAT with scale-set and
   `workflow_dispatch` permissions on that repository.
3. The same pinned, isolated GCP VPC network/subnetwork/boot image documented
   in [qualification-real-cloud.md](qualification-real-cloud.md)'s GCP
   section - reuse it, do not provision a second one. The boot image must be
   an exact image self-link, never a rolling `.../images/family/...`
   reference - see that section's `gcp_image` entry.
4. A `GOOGLE_APPLICATION_CREDENTIALS`-format credential file generated via
   Workload Identity Federation - e.g. `gcloud iam workload-identity-pools
   create-cred-config` (an `external_account` config), or `gcloud auth
   application-default login --impersonate-service-account=<SA>` for a
   short-lived local ADC file (`impersonated_service_account`) - **never a
   static service account key JSON**. See
   [e2e-qualification.background.md](e2e-qualification.background.md)'s GCP
   section for why this harness holds itself to `qualify-gcp.yml`'s WIF-only
   philosophy even though it runs from an operator's own shell, not GitHub
   Actions. The identity needs the same minimum IAM permissions
   [qualification-real-cloud.md](qualification-real-cloud.md)'s GCP section
   lists, scoped to the pinned project/region/zone.
5. `k3d`, `kubectl`, `helm`, `docker`, `gcloud`, `gh`, `jq`, `envsubst` and
   `go` installed locally.
6. A k3d node image satisfying `charts/runnerscout/Chart.yaml`'s
   `kubeVersion: ">=1.37.0-0 <1.38.0-0"` constraint (k3d's own default image
   may trail this - see `tools/e2e/bring-up-gcp.sh`'s `E2E_K3D_IMAGE`).

### Running the GCP harness

```sh
cp tools/e2e/env-gcp.example tools/e2e/.env   # gitignored; fill in real values
source tools/e2e/.env

tools/e2e/register-scale-set.sh               # prints the scale set's numeric ID
export E2E_REGISTERED_SCALE_SET_ID=<id>       # from the line above

tools/e2e/run-gcp.sh                          # bring-up, dispatch, wait, teardown
```

`tools/e2e/run-gcp.sh` always attempts teardown on exit, success or failure.
To run the stages individually instead (e.g. to inspect cluster state
between bring-up and dispatch), source `tools/e2e/.env`, run
`tools/e2e/bring-up-gcp.sh`, then `tools/e2e/dispatch-and-wait-gcp.sh`, then
`tools/e2e/verify-gcp.sh` and/or `tools/e2e/teardown-gcp.sh` yourself.

To remove the registered scale set once done with the harness entirely
(optional - see [e2e-qualification.background.md](e2e-qualification.background.md)
for why teardown does not do this automatically):

```sh
tools/e2e/register-scale-set.sh --delete
```

### GCP environment variables

See `tools/e2e/env-gcp.example` for the complete, commented list with
defaults. Each `tools/e2e/*-gcp.sh` script's own header comment lists
exactly the variables that script reads.

### What the GCP pass qualifies - and what it honestly does not

Qualified, for real, by a passing run:

- A real GitHub Actions runner scale set registration.
- The real CRD-driven controller resource graph (`ProviderConfig`/
  `RunnerClass`/`RunnerScaleSet`/`CapacityCatalog`/`NetworkProfile`) admitting
  a real job against real GCP Compute Engine Spot capacity.
- Real JIT token issuance, real startup-script runner registration, and real
  job execution on a real GCE Spot instance.
- Independently re-verified Kubernetes-side allocation cleanup and GCP-side
  resource cleanup (instances, disks, reserved addresses).

Named gaps, not covered by this piece:

- **Real GCP Spot price observation.** No `internal/prices` GCP client
  exists, permanently, by design - see [prices-gcp.md](prices-gcp.md) and
  [prices-gcp.background.md](prices-gcp.background.md) (issue #2, closed).
  This harness's `CapacityCatalog` uses a fixed, operator-overridable static
  price (`E2E_GCP_PRICE_MICROS`, default `10000`), never a live quote - the
  same honest gap `qualify-gcp.yml` already names in
  [qualification-real-cloud.md](qualification-real-cloud.md)'s GCP section,
  carried through here rather than silently reintroduced as a "harness-only"
  price observer.
- **Spot interruption during a real job.** GCP provides no supported,
  on-demand API to force one - like `qualify-gcp.yml`, this harness only
  exercises the ordinary, uninterrupted completion path.
- **Architecture/image compatibility.** `internal/provider/gcp_sdk.go`'s
  `createGCP` never cross-validates `E2E_GCP_MACHINE_TYPE` against
  `E2E_GCP_IMAGE`'s real architecture the way AWS's adapter does for
  `E2E_AWS_INSTANCE_TYPE`/`E2E_AWS_AMI_ID` - a mismatch here fails at VM
  boot, not at this harness's pre-flight. See
  [qualification-real-cloud.md](qualification-real-cloud.md)'s GCP section,
  which names the same adapter-level gap for `qualify-gcp.yml`.
- **Retry/rerun composition.** The resource graph's `RunnerClass.retry` stays
  disabled, matching every other qualification piece in this repository.
- **WireGuard networking.** The resource graph uses `separate` mode - see
  [operations.md](operations.md)'s "Known limitations": `wireguard` mode is
  not yet functionally usable end-to-end.

See [e2e-qualification.background.md](e2e-qualification.background.md) for
the reasoning behind the design choices above, including the local-runbook
decision and why each provider got its own scripts instead of one
parameterized set.
