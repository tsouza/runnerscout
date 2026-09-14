# Full end-to-end qualification: AWS, Azure and GCP - background

## Why this exists now, and why AWS then Azure then GCP

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
too; this piece's Azure and GCP passes each followed immediately after,
reading the AWS piece's exact committed shape first (see "Provenance"
below) rather than re-deriving the pattern independently.

## Why a local runbook, not a GitHub Actions workflow

This was an explicit, required decision (issue #80's own text calls it "an
open implementation choice, not yet decided"), weighed as follows, and this
reasoning applies identically across all three provider passes - none of
their own moving parts changed the answer:

**What a GitHub Actions workflow would have bought**: consistency with
`qualify-aws.yml`'s/`qualify-azure.yml`'s/`qualify-gcp.yml`'s established
shape, an `if: always()` teardown guarantee enforced by the platform rather
than a script's own `trap`, and a job-level `timeout-minutes:` ceiling the
workflow YAML itself enforces independently of any script logic.

**Why it was not chosen anyway**: this harness's failure surface is
qualitatively larger than any of the three provider-adapter qualifications.
`qualify-aws.yml`/`qualify-azure.yml`/`qualify-gcp.yml` each drive one Go
test function directly against one cloud SDK. This harness needs k3d
running inside the runner (Docker-in-Docker, its own
networking/storage/loadbalancer stack - never exercised by any existing
workflow in this repository, unlike `kindest/node` via `kind`, which
`docs/operations.md`'s local Kubernetes qualification already uses
successfully), a Helm install, CRD reconciliation, a live scale-set
listener session, real VM boot/cloud-init (AWS, Azure)/startup-script
(GCP)/runner-registration timing that is inherently variable and slow
(unlike a Go test polling one cloud API directly), and a real dispatched
job's own queueing/pickup latency on top of all of that. Stacking this much
genuinely new-to-this-repository CI machinery (k3d-in-Actions) onto the
same commit as the harness's own first real end-to-end exercise would have
made a first failure ambiguous: cluster bring-up flake,
k3d-in-Docker-in-Actions quirk, or a real bug in the resource graph /
controller reconciliation itself. A local runbook lets an operator iterate
on exactly that ambiguity interactively - re-run one stage, inspect
`kubectl get` output between stages, keep the cluster up with
`E2E_KEEP_CLUSTER` after a failure - none of which a single CI job attempt
offers.

This harness is also, by construction, not meant to run unattended or
frequently: it registers a real GitHub scale set and drives a real dispatch
against a specific pinned test repository chosen by the operator running it,
not this repository's own CI. A GitHub Actions workflow's implicit
framing - "something that runs in this repository's Actions tab, on this
repository's runners" - fits a workflow that qualifies infrastructure this
repository owns end-to-end (like `qualify-aws.yml`/`qualify-azure.yml`/
`qualify-gcp.yml` do for their respective adapters). It fits less well for a
harness whose entire subject is an *external* test repository's own Actions
surface. A local runbook makes that boundary explicit rather than blurring
it.

The safety bar is identical across all three providers and every piece, and
is enforced identically: `tools/e2e/lib.sh`'s `e2e_require_confirmation`/
`e2e_require_ceiling` are the runbook's equivalent of each `qualify-*.yml`'s
first workflow step and its hard-coded
`RUNNERSCOUT_QUALIFY_MAX_RUNTIME_CEILING_MINUTES`, and each
`tools/e2e/run*.sh`'s `trap ... EXIT` calling its own
`tools/e2e/teardown*.sh` is the runbook's equivalent of those workflows'
`if: always()` final step. A local runbook does not mean a weaker safety
net - it means the same net, enforced by the script author instead of the
platform, chosen because the platform's own moving parts here
(k3d-in-Actions) are the least proven part of this whole harness, not the
safety net itself.

## Why each provider got separate scripts, not one parameterized set

Before writing any Azure script, that piece read every AWS script in full
to check whether the non-cloud-specific ones (`dispatch-and-wait.sh` in
particular, per issue #80's own suggestion that it "likely has no
AWS-specific logic at all except its own catalog-refresh call") were
already cloud-agnostic enough to reuse directly, adding only a per-provider
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
  single generic `az resource list --tag` call and GCP's is a single
  generic `gcloud ... list --filter labels....` call, different enough
  shapes that forcing all three through one script would need a real
  abstraction layer, not just a substituted variable prefix.
- The one manifest that is *almost* cloud-agnostic,
  `manifests/runner-scaleset-{pat,app}.yaml.tmpl`, still hardcodes
  `runnerClassRef.name: e2e-aws` as a literal string, not a variable -
  reusing it verbatim for another provider would point that provider's
  `RunnerScaleSet` at a `RunnerClass` that does not exist in its own
  resource graph.

Given that, each provider piece added exactly what issue #80's own
hoped-for-if-true shortcut still holds regardless: a per-provider catalog
cross-validation function (`e2e_refresh_azure_catalog_inputs`,
`e2e_refresh_gcp_catalog_inputs`) added to the shared `tools/e2e/lib.sh`
(not a new file), alongside the other already-cloud-agnostic pieces of that
library (`e2e_require_confirmation`/`e2e_require_ceiling`/
`e2e_deadline_epoch`/`e2e_default_catalog_vars`/`e2e_render`, all reused
unchanged, or - for GCP - its own self-contained
`e2e_default_gcp_catalog_vars`, see "GCP-specific design notes" below for
why). Every other Azure- or GCP-specific stage got its own script
(`bring-up-azure.sh`/`bring-up-gcp.sh`,
`dispatch-and-wait-azure.sh`/`dispatch-and-wait-gcp.sh`,
`teardown-azure.sh`/`teardown-gcp.sh`, `verify-azure.sh`/`verify-gcp.sh`,
`run-azure.sh`/`run-gcp.sh`) and its own manifest set
(`manifests/*-azure*.yaml.tmpl`, `manifests/*-gcp.yaml.tmpl`), rather than
retrofitting the AWS scripts into a parameterized shared set. Two reasons,
not one:

1. **Correctness now**: the actual duplication every provider shares (the
   dispatch/poll loop against GitHub's own API, the fleet-ConfigMap
   watching, the RunnerScaleSet-delete-then-Helm-uninstall sequence) is
   real and was copied verbatim rather than re-derived, specifically to
   avoid introducing a new bug while translating working AWS logic to
   another provider. The genuinely cloud-specific parts (credential shape,
   resource graph field values, leftover-resource sweep shape) are
   different enough between providers that a shared script would need real
   per-cloud branching internally anyway - which is what separate scripts
   already are, just organized as separate files instead of separate
   `case` branches in one file.
2. **Merge sequencing now**: both the Azure and GCP pieces were built
   entirely on worktrees branched from `origin/main`, before AWS's own PR
   (#82) had merged - see "Provenance" below. Editing
   `tools/e2e/bring-up.sh`/`dispatch-and-wait.sh`/`teardown.sh`/`verify.sh`
   in place to add per-provider branches would have guaranteed a merge
   conflict against every line PR #82 itself touches in those same files.
   Adding only new, suffixed files (plus the necessarily shared additions
   to `tools/e2e/lib.sh`, `tools/e2e/env.example`, and this document) kept
   that conflict surface to the minimum each piece could not avoid sharing,
   letting the coordinating session sequence each rebase deliberately
   rather than resolving conflicts scattered across every AWS script
   neither piece ever actually needed to touch.

A future consolidation into one parameterized script per stage remains
possible now that all three passes exist in the same tree and any real
duplication becomes easier to see and remove safely - deliberately left for
whoever does that unification with all three implementations in hand, not
attempted speculatively here.

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
unchanged by every provider pass rather than reimplemented per cloud (the
GCP pass reuses it, `tools/e2e/register-scale-set.sh` and all, completely
unmodified).

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
session endpoint), unlike the EC2 Spot Instance/EBS volume/ENI trio (AWS),
the Azure Spot VM/NIC/OS disk trio (Azure), or the GCE Spot instance/boot
disk pair (GCP) this harness's teardown treats as urgent to reclaim.
Deleting and recreating the same scale set name on every run would add
GitHub-side churn (a new scale set ID each time, invalidating anything an
operator wired to the old one) for no safety benefit - the actual
safety-relevant cleanup obligation is the cloud-side VM (and its
dependents), which each provider's own `tools/e2e/teardown*.sh` and
`tools/e2e/verify*.sh` do treat as the hard gate.
`tools/e2e/register-scale-set.sh --delete` exists for an operator who is
done with the harness entirely and wants to remove the registration too,
but it is a deliberate, separate, opt-in action, not something teardown
does automatically on every run.

## Why `dispatch-and-wait-<provider>.sh` re-renders the CapacityCatalog immediately before dispatch

`internal/placement.MaxPriceAge` is five minutes, and CRD-driven mode has
no equivalent of `operator.Config.AWSPriceRefresh` - that flag is read only
by `cmd/runnerscout`'s mounted-JSON path
(`internal/configapi/runtime.go:112` checks `resolved.Config.AWSPriceRefresh`,
but nothing in `internal/configapi/compile.go` or `api/v1alpha1/types.go`
ever sets it from any CRD field). This was verified by reading both files
directly while designing the AWS piece, not assumed by analogy with AWS's
mounted-config path, and applies identically to Azure and GCP - CRD-driven
mode has no per-cloud carve-out here at all. A CRD-driven `CapacityCatalog`
is therefore always a static snapshot: whatever `observedAt` timestamp was
in the manifest at `kubectl apply` time is what it stays until something
re-applies it. If a bring-up script rendered the catalog once and an
operator then took several minutes bringing up the rest of the graph,
waiting for `Ready`, and only then ran the dispatch-and-wait script, the
price could easily have gone stale before the controller ever had a chance
to admit against it - not a hypothetical, but the literal common case for a
multi-stage runbook an operator drives by hand rather than a single
unattended script. Re-rendering and re-applying just the `CapacityCatalog`
at the start of dispatch-and-wait, immediately before the real
`workflow_dispatch`, keeps the freshness window tied to the moment it
actually matters instead of to whatever bring-up happened to take. This
applies even to GCP, whose price itself is static (see "GCP-specific design
notes" below) - only `observedAt` needs refreshing there, for the same
`MaxPriceAge` reason.

## Why the independent AWS verification found a real bug during that pass's own smoke testing

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
zero" can never be confused with each other again. The Azure and GCP
scripts (`verify-azure.sh`/`teardown-azure.sh`,
`verify-gcp.sh`/`teardown-gcp.sh`) applied that exact pattern to every
`az`/`gcloud` call in their own final gate from the start, proactively
rather than needing the same bug found three times.

A related, narrower pitfall fixed during the AWS piece's same testing pass:
several early drafts of `tools/e2e/dispatch-and-wait.sh`/`verify.sh` used
bare `[ condition ] && do_something` as a standalone statement under
`set -e`. When `condition` is false and there is no following `||`
fallback, the statement's own exit status is the failed test's non-zero
status, which `set -e` treats as the whole script failing - an entirely
unrelated, silent early-abort bug that has nothing to do with the AWS query
issue above but was caught the same way, by actually running the scripts
rather than only reading them. Every such site was rewritten as an explicit
`if`; the Azure and GCP scripts follow the same explicit-`if` style
throughout for the same reason.

## Why `E2E_MAX_RUNTIME_MINUTES`'s ceiling (60) is higher than each `qualify-*.yml`'s (20)

Each of `qualify-aws.yml`, `qualify-azure.yml` and `qualify-gcp.yml` only
has to wait for one Spot VM to reach a running state - typically well under
a minute once the API call returns. This harness's equivalent wait chains
several genuinely slower steps, for any provider: cluster bring-up and Helm
install (roughly a minute in this harness's own local smoke testing), the
controller's own admission/placement cycle, real VM boot and
cloud-init/startup-script execution before a runner process even starts,
GitHub Actions runner registration, queueing/pickup latency for the
dispatched job, and only then the job's own (deliberately trivial) run
time. 60 minutes is a hard ceiling, not an expectation that a real run
takes anywhere close to it - keeping the dispatched workflow trivial (an
`echo` step, per this doc's operator-prerequisites section) is what
actually bounds real spend; the ceiling exists to guarantee termination,
not to describe typical duration.

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
run rather than a hung one. The same trust boundary already exists at each
`tools/e2e/dispatch-and-wait*.sh`'s own use of the operator's own `gh`
session against `E2E_TARGET_REPO`, not this repository's `gh-tsouza`
wrapper - see AGENTS.md and each script's own header comment.

## Why `verify-<provider>.sh`/`teardown-<provider>.sh` never trust the controller's own reported state alone

Same reasoning as `aws_realcloud_test.go`'s `newIndependentEC2Client`/
`azure_realcloud_test.go`'s `newIndependentAzureClients`/
`gcp_realcloud_test.go`'s `newIndependentGCPComputeService` and each
`qualify-*.yml`'s own "Independent post-run cleanup safety net" step (see
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)'s
"Why a second, independent client for verification, in every provider"): a
bug that made the controller's own `Observe()`/`Delete()` wrongly report
success would not also make a separately-invoked `aws`/`az`/`gcloud` CLI
call agree, because the two never share a code path. Each provider's
`verify*.sh`/`teardown*.sh` use the operator's own ambient CLI session for
that provider (whatever credentials are configured in the invoking shell -
a login session, environment variables, anything the CLI itself resolves),
a different credential *resolution path* from what is mounted into the
controller's own pod, even when both ultimately reach the same cloud
account. This is independence of code path, identical in spirit to (and
for the same reason as) how each `*_realcloud_test.go` reasons about its
own second cloud client - not independence of underlying identity, since
realistically both credential paths belong to the same operator's account
either way.

## Azure-specific adaptations

Everything below is a real difference the Azure piece had to reason about
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
CRD-driven graph can express, since no CRD-driven `RunnerClass` for any
provider can express an unconditional "no cap" today.

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
one is not already guaranteed absent. The GCP pass's own smoke testing
discovered the equivalent ambient-`gcloud`-session risk in advance from
this finding and deliberately avoided it (see "GCP-specific design notes"
below) rather than repeating it.

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

## GCP-specific design notes

### Why GCP's `CapacityCatalog` uses a static price, and what still had to be built anyway

`docs/prices-gcp.md`/`docs/prices-gcp.background.md` (issue #2, closed)
already settled this for the production adapter: no `internal/prices` GCP
client exists, or ever will, because Compute Engine's public pricing
surface splits Core/RAM SKUs with no structured machine-type field, no
zone-level pricing and no request-side filter - building one would mean
pattern-matching free-text SKU descriptions, exactly the "usually works"
guessing this codebase's provider adapters otherwise refuse to ship. This
E2E harness inherits that decision rather than relitigating it: there was
never a candidate design where `tools/e2e/lib.sh` grew a
`e2e_refresh_gcp_price` function that AWS's `e2e_refresh_aws_catalog_inputs`
has and GCP's does not - doing so would have built, inside a qualification
harness, exactly the price observer the production codebase deliberately
does not ship, for the same reasons.

What the GCP pass still had to build, despite the static price, is the
*cross-validation* half of `e2e_refresh_aws_catalog_inputs`'s job:
`e2e_refresh_gcp_catalog_inputs` performs the same three read-only
`gcloud ... describe` checks `qualify-gcp.yml`'s own pre-flight step
performs (zone belongs to region, subnetwork belongs to network, image
resolves) before any create call, and refreshes `observedAt` for the
`MaxPriceAge` reason explained above. `E2E_GCP_PRICE_MICROS` itself is a
plain, hard-coded-with-an-operator-override constant
(`e2e_default_gcp_catalog_vars`, default `10000`) - never derived from any
API call, and documented as such directly in
[e2e-qualification.md](e2e-qualification.md)'s GCP section and in
`manifests/capacity-catalog-gcp.yaml.tmpl`'s own header comment, so a reader
of the rendered manifest never mistakes it for a real quote.

### Why GCP's scripts are separate files from AWS's, not one parameterized set

AWS's own scripts (`tools/e2e/bring-up.sh`, `dispatch-and-wait.sh`,
`teardown.sh`, `verify.sh`, `run.sh`) and manifests
(`manifests/provider-config.yaml.tmpl` and siblings) predate the GCP pass
and use unsuffixed names - there was only one provider when they were
written. Introducing GCP without renaming any AWS file (out of scope for
this pass; AWS's own pass was already built and under review) meant every
new GCP file needed its own name to avoid colliding with AWS's - hence the
`-gcp` suffix on both scripts (`bring-up-gcp.sh` and siblings) and manifests
(`provider-config-gcp.yaml.tmpl` and siblings). A single parameterized
script (e.g. `bring-up.sh --provider gcp`) was considered and declined:
AWS's `bring-up.sh` already branches on `E2E_GITHUB_AUTH_MODE` for its
two-mode GitHub secret handling, and adding a second provider dimension
on top would have doubled that branching inside one file for a harness
that is, by construction, run by a human operator choosing one provider at
a time - not a library consumed programmatically where parameterization
earns its complexity. Per-provider files keep each script's own
cross-validation and cleanup logic (real AWS tag-based sweep vs. real GCP
label-based sweep, in particular) linear and readable on its own, at the
cost of the header-comment/structural duplication visible by diffing
`bring-up.sh` against `bring-up-gcp.sh` side by side - an accepted,
deliberate tradeoff, not an oversight. `tools/e2e/lib.sh` (the safety gates,
the shared `e2e_render` allowlist) and `tools/e2e/register-scale-set.sh`
(cloud-agnostic GitHub-side registration) remain the one place shared logic
lives, precisely because that logic is identical across providers rather
than merely similar.

### Why GCP credentials are an operator-generated WIF file, not `google-github-actions/auth`

`qualify-gcp.yml`'s own WIF-only choice (see
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)'s
GCP section) is enforced by `google-github-actions/auth`, a GitHub
Actions-native step that exchanges the job's own Actions OIDC token for a
short-lived GCP credential - a mechanism that only exists inside a GitHub
Actions runner, not in an operator's local shell where this harness runs.
This harness cannot reuse that step; it can only hold itself to the same
*outcome* (a short-lived, non-persisted credential; never a long-lived
service account key committed or stored anywhere) via a different
mechanism: `E2E_GCP_CREDENTIALS_FILE` is documented as a file the operator
generates themselves before running the harness, either via `gcloud iam
workload-identity-pools create-cred-config` (producing an `external_account`
credential configuration - the same *credential type*
`google-github-actions/auth` itself produces, just generated by the
operator's own `gcloud` invocation instead of a GitHub Actions step) or via
`gcloud auth application-default login --impersonate-service-account=<SA>`
(producing a short-lived `impersonated_service_account` ADC file).
`internal/provider/gcp_credentials.go`'s `gcpCredential`/`parseGCPCredential`
already accept both shapes (alongside `ServiceAccount`/`AuthorizedUser`) -
the adapter itself does not enforce WIF-only, exactly as
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)'s
GCP section notes for `qualify-gcp.yml`'s own choice: this is this
harness's own documented policy, not an adapter-level restriction, and it
is carried into this piece of the qualification surface as an active
choice, not an accidental gap.

### Why `teardown-gcp.sh`/`verify-gcp.sh` filter only on `labels.runnerscout-owner`, not also `labels.runnerscout-operation`

`internal/provider/gcp_sdk.go`'s `gcpLabels` sets two labels on every VM and
boot disk it creates: `runnerscout-owner` (the `RunnerScaleSet`'s own name,
per `internal/operator.Config.Validate`'s "provider owner must match
controller name" invariant) and `runnerscout-operation` (the allocation's
own ID). `qualify-gcp.yml`'s own safety-net step filters on both, because
its Go test creates exactly one allocation with a well-known, fixed ID
(`RUNNERSCOUT_QUALIFY_ALLOCATION_ID`) known before the run even starts. This
harness has no such fixed ID: the controller assigns allocation IDs
dynamically as real placement happens, and an operator re-running stages
individually (bring-up, then dispatch, then teardown, as separate
invocations) has no reliable way to thread a single allocation ID across
all of them the way one Go test function can hold it in a local variable.
Filtering on `labels.runnerscout-owner` alone is both sufficient and
correct here: the owner label is the `RunnerScaleSet`'s own name, which is
already `E2E_SCALE_SET_NAME` - unique to this harness's own run by operator
convention (see `tools/e2e/env-gcp.example`'s default,
`e2e-gcp-qualify`) - so every GCP resource this harness's own controller
instance could possibly have created carries that owner label, and nothing
else reasonably would. AWS's `tools/e2e/teardown.sh`/`verify.sh` already
make the identical choice (`tag:runnerscout-owner=$owner`, no operation-level
tag filter) for the same reason; GCP's pass follows the established
pattern rather than introducing a narrower one that would have needed
either a fixed allocation ID (not available here) or a second sweep pass
per observed allocation (unnecessary complexity for what owner-only
filtering already answers correctly).

### Why the GCP pass's own local smoke testing deliberately avoided a real `gcloud` session

Having read the Azure section's own discovery above (a smoke test
inadvertently reaching a real, ambient `az` session) before writing its own
smoke tests, the GCP pass explicitly checked for and confirmed a real,
already-authenticated `gcloud` session was also active in its own sandbox -
and avoided exercising `teardown-gcp.sh`/`verify-gcp.sh` against it at all,
testing their failure- and success-mode logic instead against a stubbed
`gcloud` binary. This is the direct, applied lesson from the Azure finding
rather than a coincidence: once one provider pass demonstrated that this
environment's agent sandboxes can inherit ambient cloud CLI sessions, every
later pass treated that as a known risk to check for explicitly before
smoke-testing any teardown/verify script, rather than rediscovering it
independently.

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
own Provenance section). Because `tools/e2e/` did not exist on `main` yet at
the time, this piece necessarily recreated `tools/e2e/lib.sh`,
`tools/e2e/env.example`, and this document's own AWS-authored content in
full (as a superset with Azure's own additions folded in) rather than being
able to "extend" a file already on `main` - `docs/e2e-qualification.md`/
`.background.md` and `tools/e2e/lib.sh`/`env.example` were therefore
expected merge conflicts against PR #82, resolved by the coordinating
session by diffing each incoming version against the merged AWS content to
confirm it was a clean superset (or, where it was not, splicing the
genuinely new content in directly) rather than either side's work being
discarded (see "Why each provider got separate scripts, not one
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

The GCP piece was written 2026-09-14, the same day, by a third concurrent
session building the GCP equivalent while both the AWS and Azure pieces sat
merge-pending (AWS as PR #82, Azure not yet opened), also on a worktree
branched from `origin/main` before either had merged, for the same
independent-sequencing reason. It mirrored the AWS pass's structure and
safety bounds and adapted the pieces genuinely specific to GCP: pricing
(static, not observed - see "GCP-specific design notes" above), network
cross-validation (`gcloud` shapes, region/zone/network/subnetwork/image
rather than VPC/subnet/security-group/AMI), credential handling
(operator-generated WIF file rather than a shared-credentials profile), and
cleanup (label-based GCP inventory sweep rather than tag-based AWS
inventory sweep). Learning directly from the Azure piece's own disclosed
ambient-credential finding, this piece explicitly checked for and confirmed
a real `gcloud` session in its own sandbox before smoke-testing
`teardown-gcp.sh`/`verify-gcp.sh`, and deliberately tested their logic
against a stubbed `gcloud` binary instead of the real ambient session - the
one place this piece's own process genuinely improved on the pattern rather
than merely repeating it. Like both earlier pieces, it was built, reviewed,
and committed without ever registering a real GitHub scale set, configuring
real GCP credentials, or dispatching a real workflow.
