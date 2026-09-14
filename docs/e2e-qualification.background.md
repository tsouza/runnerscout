# Full end-to-end qualification: AWS and Azure - background

## Why this exists now, and why AWS then Azure

Issue #3's own three real-cloud qualification workflows
(`qualify-aws.yml`/`qualify-azure.yml`/`qualify-gcp.yml`, see
[qualification-real-cloud.md](qualification-real-cloud.md)) each named the
same gap explicitly in their own "Known gaps" sections: real GitHub Actions
job execution needs a live scale set and a real ephemeral JIT registration
token, neither of which a provider-adapter-only qualification can supply
without pulling in cluster bring-up, Helm, and scale-set wiring - exactly
the multi-layer failure surface
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)'s
"Why the provider adapter, not the whole controller" section describes
avoiding for that piece of work. Issue #80 is that deferred piece, arriving
after all three provider adapters were qualified independently. Its own
text recommends AWS first, mirroring how AWS was issue #3's first provider
too; this Azure piece follows immediately, reading the AWS piece's exact
committed shape first (see "Provenance" below) rather than re-deriving the
pattern independently. GCP remains a separate, later, not-yet-built piece
of the same issue.

## Why a local runbook, not a GitHub Actions workflow

This was an explicit, required decision (issue #80's own text calls it "an
open implementation choice, not yet decided"), weighed as follows:

**What a GitHub Actions workflow would have bought**: consistency with
`qualify-aws.yml`'s/`qualify-azure.yml`'s established shape, an
`if: always()` teardown guarantee enforced by the platform rather than a
script's own `trap`, and a job-level `timeout-minutes:` ceiling the
workflow YAML itself enforces independently of any script logic.

**Why it was not chosen anyway**: this harness's failure surface is
qualitatively larger than any of the three provider-adapter qualifications.
`qualify-aws.yml`/`qualify-azure.yml` each drive one Go test function
directly against one cloud SDK. This harness needs k3d running inside the
runner (Docker-in-Docker, its own networking/storage/loadbalancer stack -
never exercised by any existing workflow in this repository, unlike
`kindest/node` via `kind`, which `docs/operations.md`'s local Kubernetes
qualification already uses successfully), a Helm install, CRD
reconciliation, a live scale-set listener session, real VM
boot/cloud-init/runner-registration timing that is inherently variable and
slow (unlike a Go test polling one cloud API directly), and a real
dispatched job's own queueing/pickup latency on top of all of that.
Stacking this much genuinely new-to-this-repository CI machinery
(k3d-in-Actions) onto the same commit as the harness's own first real
end-to-end exercise would have made a first failure ambiguous: cluster
bring-up flake, k3d-in-Docker-in-Actions quirk, or a real bug in the
resource graph / controller reconciliation itself. A local runbook lets an
operator iterate on exactly that ambiguity interactively - re-run one stage,
inspect `kubectl get` output between stages, keep the cluster up with
`E2E_KEEP_CLUSTER` after a failure - none of which a single CI job attempt
offers.

This harness is also, by construction, not meant to run unattended or
frequently: it registers a real GitHub scale set and drives a real dispatch
against a specific pinned test repository chosen by the operator running it,
not this repository's own CI. A GitHub Actions workflow's implicit
framing - "something that runs in this repository's Actions tab, on this
repository's runners" - fits a workflow that qualifies infrastructure this
repository owns end-to-end (like `qualify-aws.yml`/`qualify-azure.yml` do
for their respective adapters). It fits less well for a harness whose
entire subject is an *external* test repository's own Actions surface. A
local runbook makes that boundary explicit rather than blurring it.

The safety bar is identical across both clouds and both pieces, and is
enforced identically: `tools/e2e/lib.sh`'s `e2e_require_confirmation`/
`e2e_require_ceiling` are the runbook's equivalent of
`qualify-aws.yml`'s/`qualify-azure.yml`'s first workflow step and its
hard-coded `RUNNERSCOUT_QUALIFY_MAX_RUNTIME_CEILING_MINUTES`, and
`tools/e2e/run.sh`'s/`run-azure.sh`'s `trap ... EXIT` calling
`tools/e2e/teardown.sh`/`teardown-azure.sh` is the runbook's equivalent of
those workflows' `if: always()` final step. A local runbook does not mean a
weaker safety net - it means the same net, enforced by the script author
instead of the platform, chosen because the platform's own moving parts
here (k3d-in-Actions) are the least proven part of this whole harness, not
the safety net itself.

## Why AWS and Azure got separate scripts, not one parameterized set

Before writing any Azure script, this piece read every AWS script in full
to check whether the non-cloud-specific ones (`dispatch-and-wait.sh` in
particular, per issue #80's own suggestion that it "likely has no
AWS-specific logic at all except its own catalog-refresh call") were
already cloud-agnostic enough to reuse directly, adding only an Azure
catalog-refresh helper to `tools/e2e/lib.sh`. They were not, on inspection:

- `tools/e2e/dispatch-and-wait.sh` hardcodes the rendered manifest filename
  (`capacity-catalog.yaml.tmpl`), calls `e2e_refresh_aws_catalog_inputs`
  directly by name, and its own `e2e_require_var` loop lists only
  `E2E_AWS_*` variable names - none of that is parameterized on a cloud
  argument or an indirection variable.
- `tools/e2e/bring-up.sh` similarly hardcodes AWS Secret names
  (`aws-credentials`), AWS-specific required variables, and the AWS
  manifest filenames it renders and applies.
- `tools/e2e/teardown.sh`/`verify.sh` query AWS's EC2 API directly (three
  separately-typed `describe-instances`/`describe-volumes`/
  `describe-network-interfaces` calls) with no cloud-agnostic abstraction
  over "list leftover resources for this owner" - Azure's equivalent is a
  single generic `az resource list --tag` call, a different enough shape
  that forcing both through one script would need a real abstraction layer,
  not just a substituted variable prefix.
- The one manifest that is *almost* cloud-agnostic,
  `manifests/runner-scaleset-{pat,app}.yaml.tmpl`, still hardcodes
  `runnerClassRef.name: e2e-aws` as a literal string, not a variable - reusing
  it verbatim for Azure would point Azure's `RunnerScaleSet` at a
  `RunnerClass` that does not exist in Azure's own resource graph.

Given that, this piece added exactly what issue #80's own hoped-for-if-true
shortcut still holds regardless: `e2e_refresh_azure_catalog_inputs`, added
to the shared `tools/e2e/lib.sh` (not a new file), alongside the other
already-cloud-agnostic pieces of that library
(`e2e_require_confirmation`/`e2e_require_ceiling`/`e2e_deadline_epoch`/
`e2e_default_catalog_vars`/`e2e_render`, all reused unchanged). Every other
Azure-specific stage got its own script (`bring-up-azure.sh`,
`dispatch-and-wait-azure.sh`, `teardown-azure.sh`, `verify-azure.sh`,
`run-azure.sh`) and its own manifest set
(`manifests/*-azure*.yaml.tmpl`), rather than retrofitting the AWS scripts
into a parameterized shared set. Two reasons, not one:

1. **Correctness now**: the actual duplication the two clouds share (the
   dispatch/poll loop against GitHub's own API, the fleet-ConfigMap
   watching, the RunnerScaleSet-delete-then-Helm-uninstall sequence) is
   real and was copied verbatim rather than re-derived, specifically to
   avoid introducing a new bug while translating working AWS logic to
   Azure. The genuinely cloud-specific parts (credential shape, resource
   graph field values, leftover-resource sweep shape) are different enough
   between AWS and Azure that a shared script would need real per-cloud
   branching internally anyway - which is what separate scripts already
   are, just organized as separate files instead of separate `case`
   branches in one file.
2. **Merge sequencing now**: this piece was built entirely on a worktree
   branched from `origin/main`, before AWS's own PR (#82) had merged - see
   "Provenance" below. Editing `tools/e2e/bring-up.sh`/`dispatch-and-wait.sh`/
   `teardown.sh`/`verify.sh` in place to add Azure branches would have
   guaranteed a merge conflict against every line PR #82 itself touches in
   those same files. Adding only new, Azure-suffixed files (plus the one
   necessarily shared addition to `tools/e2e/lib.sh`, `tools/e2e/env.example`,
   and this document) keeps that conflict surface to the minimum the two
   pieces cannot avoid sharing, letting the coordinating session sequence
   the rebase deliberately rather than resolving conflicts scattered across
   every AWS script this piece never actually needed to touch.

A future consolidation into one parameterized script per stage remains
possible once both pieces exist in the same tree and any real duplication
becomes easier to see and remove safely - deliberately left for whoever
does that unification with both implementations in hand, not attempted
speculatively here.

## Why registration is a separate script/binary from bring-up and from cmd/runnerscout

`cmd/runnerscout` (the production controller) only ever *consumes* an
existing `ScaleSetID` - nothing in `internal/operator` creates or deletes
one, by design (a running controller should never have the authority to
mutate GitHub-side scale-set objects, only cloud-side VMs and
Kubernetes-side state). Adding scale-set creation as a flag on
`cmd/runnerscout` would have given the production binary a code path that
creates/deletes billable, durable, cross-account GitHub objects purely to
serve a qualification harness - a capability every real deployment of that
binary would then carry, unused, forever. `cmd/e2e-register-scale-set` is a
separate, small program specifically so that capability lives only where
it is actually needed - and, being a GitHub-side concept only, is shared
unchanged by both the AWS and Azure pieces rather than reimplemented per
cloud.

Splitting registration from the bring-up scripts (rather than folding
registration into bring-up's own first step) keeps bring-up idempotent-safe
to re-run in the ordinary sense Kubernetes tooling means it (`helm upgrade
--install`, `kubectl apply` are all convergent) without also having to
reason about whether re-running it could ever accidentally re-trigger a
GitHub-side mutation. `cmd/e2e-register-scale-set` is itself idempotent-safe
too (`GetRunnerScaleSet` before `CreateRunnerScaleSet`), but keeping the two
concerns in separate scripts means a bring-up retry after, say, a Helm
install failure never even needs to reason about the GitHub side at all.

## Why teardown does not delete the registered scale set

A GitHub Actions runner scale set with no active runners is not a billable
resource - it is metadata (a name, a runner group binding, a listener
session endpoint), unlike the EC2 Spot Instance/EBS volume/ENI trio (AWS)
or the Azure Spot VM/NIC/OS disk trio (Azure) this harness's teardown
treats as urgent to reclaim. Deleting and recreating the same scale set
name on every run would add GitHub-side churn (a new scale set ID each
time, invalidating anything an operator wired to the old one) for no safety
benefit - the actual safety-relevant cleanup obligation is the cloud-side
VM (and its dependents), which `tools/e2e/teardown.sh`/`teardown-azure.sh`
and `tools/e2e/verify.sh`/`verify-azure.sh` do treat as the hard gate.
`tools/e2e/register-scale-set.sh --delete` exists for an operator who is
done with the harness entirely and wants to remove the registration too,
but it is a deliberate, separate, opt-in action, not something teardown
does automatically on every run.

## Why `dispatch-and-wait.sh`/`dispatch-and-wait-azure.sh` re-render the CapacityCatalog immediately before dispatch

`internal/placement.MaxPriceAge` is five minutes, and CRD-driven mode has
no equivalent of `operator.Config.AWSPriceRefresh` - that flag is read only
by `cmd/runnerscout`'s mounted-JSON path
(`internal/configapi/runtime.go:112` checks `resolved.Config.AWSPriceRefresh`,
but nothing in `internal/configapi/compile.go` or `api/v1alpha1/types.go`
ever sets it from any CRD field). This was verified by reading both files
directly while designing the AWS piece, not assumed by analogy with AWS's
mounted-config path, and applies identically to Azure - CRD-driven mode has
no per-cloud carve-out here at all. A CRD-driven `CapacityCatalog` is
therefore always a static snapshot: whatever `observedAt` timestamp was in
the manifest at `kubectl apply` time is what it stays until something
re-applies it. If a bring-up script rendered the catalog once and an
operator then took several minutes bringing up the rest of the graph,
waiting for `Ready`, and only then ran the dispatch-and-wait script, the
price could easily have gone stale before the controller ever had a chance
to admit against it - not a hypothetical, but the literal common case for a
multi-stage runbook an operator drives by hand rather than a single
unattended script. Re-rendering and re-applying just the `CapacityCatalog`
at the start of dispatch-and-wait, immediately before the real
`workflow_dispatch`, keeps the freshness window tied to the moment it
actually matters instead of to whatever bring-up happened to take.

## Why the independent verification found a real bug during the AWS piece's own smoke testing

While mechanically smoke-testing `tools/e2e/teardown.sh` locally (k3d
cluster bring-up with placeholder credentials, no real AWS account
configured at all in the sandbox), `teardown.sh`'s final verification
fallback re-query used `aws ec2 describe-instances ... 2>/dev/null || true`.
When the `aws` CLI failed with a `NoCredentials` error (not "zero
instances," an outright API call failure), that pattern silently converted
the failure into an empty string, which the surrounding logic then read as
"confirmed clean" - the same shape of bug this harness's own design
philosophy (see [qualification-real-cloud.md](qualification-real-cloud.md)'s
"independent cleanup inventory" requirement) exists specifically to catch
in the *controller's* Observe()/Delete() path, found instead in this
harness's own verification script. It was fixed by replacing every
`2>/dev/null || true`-swallowed AWS query in `tools/e2e/verify.sh`/
`teardown.sh`'s final gate with an explicit
`if ! var=$(aws ...); then <fail loudly, distinctly from "found leftover">`
pattern, so "the query itself failed" and "the query succeeded and found
zero" can never be confused with each other again. This piece's Azure
scripts (`verify-azure.sh`/`teardown-azure.sh`) applied that exact pattern
to every `az` call in their own final gate from the start, proactively
rather than needing the same bug found twice.

A related, narrower pitfall fixed during the AWS piece's same testing pass:
several early drafts of `tools/e2e/dispatch-and-wait.sh`/`verify.sh` used
bare `[ condition ] && do_something` as a standalone statement under
`set -e`. When `condition` is false and there is no following `||`
fallback, the statement's own exit status is the failed test's non-zero
status, which `set -e` treats as the whole script failing - an entirely
unrelated, silent early-abort bug that has nothing to do with the AWS query
issue above but was caught the same way, by actually running the scripts
rather than only reading them. Every such site was rewritten as an explicit
`if`; this piece's Azure scripts follow the same explicit-`if` style
throughout for the same reason.

## Why `E2E_MAX_RUNTIME_MINUTES`'s ceiling (60) is higher than `qualify-aws.yml`'s/`qualify-azure.yml`'s (20)

Both `qualify-aws.yml` and `qualify-azure.yml` only have to wait for one
Spot VM to reach a running state - typically well under a minute once the
API call returns. This harness's equivalent wait chains several genuinely
slower steps, for either cloud: cluster bring-up and Helm install (roughly
a minute in this harness's own local smoke testing), the controller's own
admission/placement cycle, real VM boot and cloud-init execution before a
runner process even starts, GitHub Actions runner registration,
queueing/pickup latency for the dispatched job, and only then the job's own
(deliberately trivial) run time. 60 minutes is a hard ceiling, not an
expectation that a real run takes anywhere close to it - keeping the
dispatched workflow trivial (an `echo` step, per this doc's
operator-prerequisites section) is what actually bounds real spend; the
ceiling exists to guarantee termination, not to describe typical duration.

## Why the dispatched workflow's triviality is documented, not enforced

`qualify-aws.yml`/`qualify-azure.yml`/`qualify-gcp.yml` can enforce their
own real workload directly, because the "workload" is one Go test function
this repository itself owns and reviews. This harness's dispatched workflow
lives in an operator-chosen external test repository this codebase has no
access to or opinion about beyond its trigger name and ref - there is no
read-only pre-flight check equivalent to those workflows' own
subnet/VPC/resource-group cross-validation that could inspect an arbitrary
external repository's workflow file and confirm it is "trivial" without a
much more invasive (and still gameable) content scan. The
`E2E_MAX_RUNTIME_MINUTES` ceiling is the actual backstop against a
long-running or runaway dispatched job - if an operator points this at a
workflow that runs for 45 minutes, the harness's poll loop still terminates
at the ceiling and teardown still runs, at the cost of a failed (timed-out)
run rather than a hung one. The same trust boundary already exists at
`tools/e2e/dispatch-and-wait.sh`'s/`dispatch-and-wait-azure.sh`'s own use of
the operator's own `gh` session against `E2E_TARGET_REPO`, not this
repository's `gh-tsouza` wrapper - see AGENTS.md and each script's own
header comment.

## Why `verify.sh`/`teardown.sh`/`verify-azure.sh`/`teardown-azure.sh` never trust the controller's own reported state alone

Same reasoning as `aws_realcloud_test.go`'s `newIndependentEC2Client`/
`azure_realcloud_test.go`'s `newIndependentAzureClients` and
`qualify-aws.yml`'s/`qualify-azure.yml`'s own "Independent post-run cleanup
safety net" step (see
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)'s
"Why a second, independent client, and what independence means here"): a
bug that made the controller's own `Observe()`/`Delete()` wrongly report
success would not also make a separately-invoked `aws`/`az` CLI call agree,
because the two never share a code path. `verify.sh`/`teardown.sh` use the
operator's own ambient AWS CLI session; `verify-azure.sh`/`teardown-azure.sh`
use the operator's own ambient `az` CLI session (whatever credentials are
configured in the invoking shell - a login session, environment variables,
anything the CLI itself resolves), each a different credential *resolution
path* from what is mounted into the controller's own pod, even when both
ultimately reach the same cloud account. This is independence of code
path, identical in spirit to (and for the same reason as) how
`aws_realcloud_test.go`/`azure_realcloud_test.go` each reason about their
own second cloud client - not independence of underlying identity, since
realistically both credential paths belong to the same operator's account
either way.

## Azure-specific adaptations

Everything below is a real difference this piece had to reason about
adapting from the AWS piece, following
[qualification-real-cloud.md](qualification-real-cloud.md)'s/
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)'s
Azure sections' already-reviewed patterns rather than inventing new ones -
see those documents for the underlying adapter-level reasoning; this
section only covers what is specific to the E2E harness itself.

### A fresh, ephemeral SSH key instead of an operator-provided one

`internal/provider.Command.Validate` requires `Config.SSHPublicKey` to
start with `ssh-` for every Azure deployment (`azure.go`'s
`linuxConfiguration.ssh.publicKeys[].keyData`, with password authentication
disabled) - Azure's ARM API will not provision the VM at all without a
real-shaped key, and AWS has no equivalent requirement, so
`provider-config.yaml.tmpl` (AWS) has no such field at all. Rather than
asking an operator to provision, store and rotate a real SSH key pair for a
qualification VM nothing is ever meant to log into (this harness's
`Bootstrap` is the same synthetic-only-until-a-real-JIT-token-issues path
as everywhere else - see `azure_realcloud_test.go`'s own header comment for
the equivalent reasoning in the provider-adapter qualification),
`bring-up-azure.sh` generates a throwaway ed25519 key pair itself at the
start of every run (`ssh-keygen -t ed25519 -N ''`) and discards the private
half immediately after encoding the public half into
`E2E_AZURE_SSH_PUBLIC_KEY`. This is strictly less operational surface than
a managed secret would be, for a value nothing downstream ever needs to
verify against - the exact same choice `azure_realcloud_test.go` already
made for the provider-adapter qualification, applied here for the same
reason.

### Why the Spot price observation needs no Azure credential, but the rest of pre-flight does

`internal/prices/azure.go`'s `AzureSpotClient` talks to Azure's public,
unauthenticated Retail Prices API - unlike AWS's
`describe-spot-price-history`, which needs a signed, credentialed request.
`e2e_refresh_azure_catalog_inputs` (`tools/e2e/lib.sh`) reflects this
directly: its price-observation half is a plain `curl`, no `az` CLI
involved, while its network/identity cross-validation half (subscription
identity, resource-group ownership of the VNet/subnet/NSG/image) still
needs an authenticated `az` CLI session, mirroring `qualify-azure.yml`'s own
pre-flight almost verbatim (`az account show`/`az resource show`, the same
ARM-ID-nesting structural check for `subnet_id` under `vnet_id`). An
operator running the Azure piece must therefore have `az login` (or
equivalent) already active before running `bring-up-azure.sh`/
`dispatch-and-wait-azure.sh` even though the price refresh itself would
technically work without it.

### Why the Availability Zone is a required input, never derived

AWS's `E2E_AWS_ZONE` is derived from `E2E_AWS_SUBNET_ID` via
`describe-subnets`' own `AvailabilityZone` field - a subnet unambiguously
implies its AZ. Azure has no equivalent derivation: a subnet resource
carries no Availability Zone property at all (zones apply to the VM/disk
resources themselves, not the network), and
[qualification-real-cloud.md](qualification-real-cloud.md)'s Azure section
already established this exact reasoning for `qualify-azure.yml`'s own
`availability_zone` input. `E2E_AZURE_ZONE` is therefore a required,
operator-supplied input here too (`"1"`, `"2"` or `"3"`), never computed.

### Why the maxPriceMicros ceiling is a normal finite value, not Azure's `-1` sentinel

`azure.go`'s Spot `billingProfile.maxPrice` is populated directly from
`Allocation.Requirements.MaxPriceMicros` with no special-case handling, and
Azure documents `-1` as its own sentinel meaning "no price cap" (see
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)'s
Azure section for the full reasoning, and why
`azure_realcloud_test.go` sets that sentinel directly on a hand-built
`Allocation`). This harness's `RunnerClass.spec.placement.maxPriceMicros`
cannot express `-1`: `api/v1alpha1/types.go`'s `PlacementPolicy.MaxPriceMicros`
carries `+kubebuilder:validation:Minimum=1`, enforced by the API server on
every `kubectl apply`, independent of anything this harness's own scripts
do. `manifests/runner-class-azure.yaml.tmpl` therefore uses
`E2E_AZURE_MAX_PRICE_MICROS` as a real, finite ceiling (defaulted to
200000 micros by `e2e_default_catalog_vars`, the same default value the AWS
piece already uses for its own equivalent), exactly like the AWS piece's
own `maxPriceMicros` already does - this is not a gap relative to what a
CRD-driven graph can express, since no CRD-driven `RunnerClass` for either
cloud can express an unconditional "no cap" today.

### Why local smoke testing of `teardown-azure.sh` cannot fully neutralize a real `az` session the way the AWS piece's placeholder credentials file does

The AWS piece's own local mechanics smoke test can guarantee zero real-AWS
reachability: pointing `AWS_SHARED_CREDENTIALS_FILE` at a syntactically
well-formed but fake profile makes every `aws` CLI call fail closed with an
authentication error, since AWS credential resolution for that CLI is
scoped entirely to that one environment variable. Azure's `az` CLI has no
equivalent scoping - its credential resolution is a single, ambient,
session-wide login (`az login`), not something `E2E_AZURE_CLIENT_ID`/
`E2E_AZURE_TENANT_ID`/`E2E_AZURE_FEDERATED_TOKEN_FILE` (which only feed the
*controller's own* Workload Identity Federation credential, mounted into
its pod) can neutralize or redirect. `teardown-azure.sh`'s/`verify-azure.sh`'s
own Azure-side checks deliberately reuse whatever `az` session is already
active in the invoking shell, exactly mirroring `qualify-azure.yml`'s own
reviewed "reuse the az CLI session already established" pattern - this is
correct and intentional for a real run, but it means a local mechanics
smoke test of these two scripts specifically will reach a real Azure
account if the machine running it already has one logged in via `az`,
regardless of `E2E_SKIP_IMAGE_BUILD` (which only governs `bring-up-azure.sh`'s
own catalog-refresh pre-flight, not teardown/verify - teardown must always
attempt real cleanup regardless of how bring-up ran, which is the entire
point of a guaranteed-teardown design).

This was discovered directly, not hypothesized: this piece's own local
smoke test of `teardown-azure.sh` ran in a sandbox that turned out to
already have a live `az login` session (a real personal Azure subscription,
confirmed via `az account show` immediately after). The only two calls this
made against it were both read-only (`az account show`, then
`az resource list --resource-group <placeholder> --tag ...`, twice) against
a placeholder resource group name (`runnerscout-e2e-qualify`) that does not
exist in that account - both failed with `ResourceGroupNotFound`, which
`verify-azure.sh`'s explicit-failure-check code path correctly reported as
"could not independently re-verify cleanup," distinct from "confirmed
clean," exactly as designed. No resource was created, modified, listed
successfully, or deleted; the practical exposure was nil, but the *general*
risk - reaching a real, live Azure account merely by testing these scripts'
mechanics - is real and cannot be closed from inside this harness the way
the AWS piece's placeholder-credentials-file trick closes it. An operator
or agent smoke-testing `teardown-azure.sh`/`verify-azure.sh` locally should
first confirm with `az account show` whether a real session is active, and
prefer a sandbox/container with no Azure credentials configured at all if
one is not already guaranteed absent.

### Why the independent leftover sweep is one generic query, not three typed ones

`tools/e2e/verify.sh`/`teardown.sh` (AWS) issue three separately-typed EC2
describe calls (instances, volumes, network interfaces) because that is
what `aws ec2 describe-*` requires - there is no single "list everything
tagged X" call in the EC2 API surface this harness uses.
`verify-azure.sh`/`teardown-azure.sh` instead issue one generic
`az resource list --resource-group <rg> --tag runnerscout-owner=<owner>`
call, which already returns every resource type in the resource group
(VM, NIC, disk, or anything else a future template might add) - the exact
tag-based, type-agnostic sweep
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)'s
Azure section documents `azure_realcloud_test.go`'s own
`azureIndependentLeftovers` already using, and `qualify-azure.yml`'s own
safety-net step already using, for the same "strictly stronger, would also
catch an unexpected resource kind" reason. `teardown-azure.sh`'s
force-cleanup pass still deletes VMs before re-listing and deleting
whatever remains, matching Azure's `deleteAzure` one-resource-per-call
priority order (VM, then NIC, then disk) and `qualify-azure.yml`'s own
final-step structure almost verbatim.

## Provenance

AWS piece written 2026-09-14 for issue #80's AWS-only first pass,
immediately following that session's `qualify-aws.yml`/`qualify-gcp.yml`
work (issues #3/#16/#17). Built, reviewed via local k3d mechanics smoke
testing (cluster creation, CRD/chart install, and - deliberately -
teardown's own failure-mode handling, which is where the AWS-query-masking
bug above was found and fixed), and committed without ever registering a
real GitHub scale set, configuring real AWS credentials, or dispatching a
real workflow - exactly like `qualify-aws.yml`/`qualify-gcp.yml` were.

Azure piece written 2026-09-14 by a concurrent session building the Azure
equivalent while the AWS piece sat merge-pending as PR #82, on a worktree
branched from `origin/main` (i.e. without PR #82's own commits present) per
the coordinating session's explicit instruction to keep the two pieces as
independent, separately-sequenced patches rather than stacking one on the
other before either was reviewed. This piece read PR #82's exact committed
shape directly (`git show origin/feat/e2e-aws-qualification -- ...`) before
writing anything, to mirror its pattern faithfully rather than re-deriving
it and risking silent drift - the same discipline the GCP provider-adapter
qualification piece already used relative to the AWS provider-adapter
piece (see
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)'s
own Provenance section). Because `tools/e2e/` does not exist on `main` yet,
this piece necessarily recreates `tools/e2e/lib.sh`, `tools/e2e/env.example`,
and this document's own AWS-authored content in full (as a superset with
Azure's own additions folded in) rather than being able to "extend" a file
already on `main` - `docs/e2e-qualification.md`/`.background.md` and
`tools/e2e/lib.sh`/`env.example` are therefore expected merge conflicts
against PR #82, flagged explicitly for the coordinating session to
sequence (see "Why AWS and Azure got separate scripts, not one
parameterized set" above for why every other file was kept additive-only
instead). This piece was built, reviewed via local k3d mechanics smoke
testing (mirroring the AWS piece's own scope: cluster creation, CRD/chart
install against `bring-up-azure.sh`'s `E2E_SKIP_IMAGE_BUILD` mode, plus
`teardown-azure.sh`'s own failure-mode handling), and committed without
ever registering a real GitHub scale set, dispatching a real workflow, or
configuring real Azure credentials for `bring-up-azure.sh` itself. It was
NOT built without ever reaching a real, already-authenticated `az` CLI
session, though - see "Why local smoke testing of `teardown-azure.sh`
cannot fully neutralize a real `az` session the way the AWS piece's
placeholder credentials file does" above for exactly what that session was
and the two read-only, no-mutation calls it received, discovered and
disclosed rather than hidden.
