# Full end-to-end qualification: AWS - background

## Why this exists now, and why AWS-only

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
too - Azure and GCP are separate, later, not-yet-built pieces of the same
issue.

## Why a local runbook, not a GitHub Actions workflow

This was an explicit, required decision (issue #80's own text calls it "an
open implementation choice, not yet decided"), weighed as follows:

**What a GitHub Actions workflow would have bought**: consistency with
`qualify-aws.yml`'s established shape, an `if: always()` teardown guarantee
enforced by the platform rather than a script's own `trap`, and a job-level
`timeout-minutes:` ceiling the workflow YAML itself enforces independently
of any script logic.

**Why it was not chosen anyway**: this harness's failure surface is
qualitatively larger than any of the three provider-adapter qualifications.
`qualify-aws.yml` drives one Go test function directly against one cloud
SDK. This harness needs k3d running inside the runner (Docker-in-Docker,
its own networking/storage/loadbalancer stack - never exercised by any
existing workflow in this repository, unlike `kindest/node` via `kind`,
which `docs/operations.md`'s local Kubernetes qualification already uses
successfully), a Helm install, CRD reconciliation, a live scale-set
listener session, real VM boot/cloud-init/runner-registration timing that
is inherently variable and slow (unlike a Go test polling one EC2 API
directly), and a real dispatched job's own queueing/pickup latency on top
of all of that. Stacking this much genuinely new-to-this-repository CI
machinery (k3d-in-Actions) onto the same commit as the harness's own first
real end-to-end exercise would have made a first failure ambiguous: cluster
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
repository owns end-to-end (like `qualify-aws.yml` does for the AWS
adapter). It fits less well for a harness whose entire subject is an
*external* test repository's own Actions surface. A local runbook makes
that boundary explicit rather than blurring it.

The safety bar is identical either way, and is enforced identically:
`tools/e2e/lib.sh`'s `e2e_require_confirmation`/`e2e_require_ceiling` are
the runbook's equivalent of `qualify-aws.yml`'s first workflow step and its
hard-coded `RUNNERSCOUT_QUALIFY_MAX_RUNTIME_CEILING_MINUTES`, and
`tools/e2e/run.sh`'s `trap ... EXIT` calling `tools/e2e/teardown.sh` is the
runbook's equivalent of `qualify-aws.yml`'s `if: always()` final step. A
local runbook does not mean a weaker safety net - it means the same net,
enforced by the script author instead of the platform, chosen because the
platform's own moving parts here (k3d-in-Actions) are the least proven part
of this whole harness, not the safety net itself.

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
it is actually needed.

Splitting registration from `tools/e2e/bring-up.sh` (rather than folding
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
session endpoint), unlike the EC2 Spot Instance/EBS volume/ENI trio this
harness's teardown treats as urgent to reclaim. Deleting and recreating the
same scale set name on every run would add GitHub-side churn (a new scale
set ID each time, invalidating anything an operator wired to the old one)
for no safety benefit - the actual safety-relevant cleanup obligation is the
cloud-side VM, which `tools/e2e/teardown.sh` and `tools/e2e/verify.sh` do
treat as the hard gate. `tools/e2e/register-scale-set.sh --delete` exists
for an operator who is done with the harness entirely and wants to remove
the registration too, but it is a deliberate, separate, opt-in action, not
something teardown does automatically on every run.

## Why `dispatch-and-wait.sh` re-renders the CapacityCatalog immediately before dispatch

`internal/placement.MaxPriceAge` is five minutes, and CRD-driven mode has
no equivalent of `operator.Config.AWSPriceRefresh` - that flag is read only
by `cmd/runnerscout`'s mounted-JSON path
(`internal/configapi/runtime.go:112` checks `resolved.Config.AWSPriceRefresh`,
but nothing in `internal/configapi/compile.go` or `api/v1alpha1/types.go`
ever sets it from any CRD field). This was verified by reading both files
directly while designing this harness, not assumed by analogy with AWS's
mounted-config path. A CRD-driven `CapacityCatalog` is therefore always a
static snapshot: whatever `observedAt` timestamp was in the manifest at
`kubectl apply` time is what it stays until something re-applies it. If
`tools/e2e/bring-up.sh` rendered the catalog once and an operator then took
several minutes bringing up the rest of the graph, waiting for `Ready`, and
only then ran `tools/e2e/dispatch-and-wait.sh`, the price could easily have
gone stale before the controller ever had a chance to admit against it -
not a hypothetical, but the literal common case for a multi-stage runbook
an operator drives by hand rather than a single unattended script.
Re-rendering and re-applying just the `CapacityCatalog` at the start of
`dispatch-and-wait.sh`, immediately before the real `workflow_dispatch`,
keeps the freshness window tied to the moment it actually matters instead of
to whatever bring-up happened to take.

## Why the independent verification found a real bug during this harness's own smoke testing

While mechanically smoke-testing `tools/e2e/teardown.sh` locally (k3d
cluster bring-up with placeholder credentials, no real AWS account
configured at all in the sandbox - see this harness's own commit history/PR
description for the exact reproduction), `teardown.sh`'s final verification
fallback re-query used `aws ec2 describe-instances ... 2>/dev/null || true`.
When the `aws` CLI failed with a `NoCredentials` error (not "zero
instances," an outright API call failure), that pattern silently converted
the failure into an empty string, which the surrounding logic then read as
"confirmed clean" - the same shape of bug this harness's own design
philosophy (see [qualification-real-cloud.md](qualification-real-cloud.md)'s
"independent cleanup inventory" requirement) exists specifically to catch
in the *controller's* Observe()/Delete() path, found instead in this
harness's own verification script. It was fixed by replacing every
`2>/dev/null || true`-swallowed AWS query in `tools/e2e/verify.sh` and
`tools/e2e/teardown.sh`'s final gate with an explicit
`if ! var=$(aws ...); then <fail loudly, distinctly from "found leftover">`
pattern, so "the query itself failed" and "the query succeeded and found
zero" can never be confused with each other again. The three best-effort
discovery queries earlier in `teardown.sh` (used only to decide what to
force-terminate, not to certify success) were deliberately left tolerant of
failure, since the authoritative gate is the final re-verification, which
now fails loudly on exactly this class of error. This bug and fix were both
found and applied before this harness was ever run against a real AWS
account - the smoke test's entire value was surfacing this class of mistake
somewhere safe.

A related, narrower pitfall fixed during the same testing pass: several
early drafts of `tools/e2e/dispatch-and-wait.sh` and `tools/e2e/verify.sh`
used bare `[ condition ] && do_something` as a standalone statement under
`set -e`. When `condition` is false and there is no following `||`
fallback, the statement's own exit status is the failed test's non-zero
status, which `set -e` treats as the whole script failing - an entirely
unrelated, silent early-abort bug that has nothing to do with the AWS query
issue above but was caught the same way, by actually running the scripts
rather than only reading them. Every such site was rewritten as an explicit
`if`.

## Why `E2E_MAX_RUNTIME_MINUTES`'s ceiling (60) is higher than `qualify-aws.yml`'s (20)

`qualify-aws.yml` only has to wait for one EC2 Spot Instance to reach
"running" - typically well under a minute once the API call returns. This
harness's equivalent wait chains several genuinely slower steps: cluster
bring-up and Helm install (roughly a minute in this harness's own local
smoke testing), the controller's own admission/placement cycle, real VM
boot and cloud-init execution before a runner process even starts, GitHub
Actions runner registration, queueing/pickup latency for the dispatched
job, and only then the job's own (deliberately trivial) run time. 60 minutes
is a hard ceiling, not an expectation that a real run takes anywhere close
to it - keeping the dispatched workflow trivial (an `echo` step, per this
doc's operator-prerequisites section) is what actually bounds real spend;
the ceiling exists to guarantee termination, not to describe typical
duration.

## Why the dispatched workflow's triviality is documented, not enforced

`qualify-aws.yml`/`qualify-gcp.yml` can enforce their own real workload
directly, because the "workload" is one Go test function this repository
itself owns and reviews. This harness's dispatched workflow lives in an
operator-chosen external test repository this codebase has no access to or
opinion about beyond its trigger name and ref - there is no read-only
pre-flight check equivalent to `qualify-aws.yml`'s subnet/VPC
cross-validation that could inspect an arbitrary external repository's
workflow file and confirm it is "trivial" without a much more invasive
(and still gameable) content scan. The `E2E_MAX_RUNTIME_MINUTES` ceiling is
the actual backstop against a long-running or runaway dispatched job - if
an operator points this at a workflow that runs for 45 minutes, the
harness's poll loop still terminates at the ceiling and teardown still
runs, at the cost of a failed (timed-out) run rather than a hung one. The
same trust boundary already exists at `tools/e2e/dispatch-and-wait.sh`'s
own use of the operator's own `gh` session against `E2E_TARGET_REPO`, not
this repository's `gh-tsouza` wrapper - see AGENTS.md and that script's own
header comment.

## Why `verify.sh`/`teardown.sh` never trust the controller's own reported state alone

Same reasoning as `aws_realcloud_test.go`'s `newIndependentEC2Client` and
`qualify-aws.yml`'s own "Independent post-run cleanup safety net" step (see
[qualification-real-cloud.background.md](qualification-real-cloud.background.md)'s
"Why a second, independent client, and what independence means here"): a
bug that made the controller's own `Observe()`/`Delete()` wrongly report
success would not also make a separately-invoked `aws` CLI call agree,
because the two never share a code path. `verify.sh` and `teardown.sh` use
the operator's own ambient AWS CLI session (whatever credentials are
configured in the invoking shell - `~/.aws/config`, environment variables,
an SSO session, anything the `aws` CLI itself resolves), which is a
different credential *resolution path* from the `AWS_SHARED_CREDENTIALS_FILE`
mounted into the controller's pod from the `aws-credentials` Secret, even
when both ultimately reach the same AWS account. This is independence of
code path, identical in spirit to (and for the same reason as) how
`aws_realcloud_test.go` reasons about its own second EC2 client - not
independence of underlying identity, since realistically both credential
paths belong to the same operator's account either way.

## Provenance

Written 2026-09-14 for issue #80's AWS-only first pass, immediately
following this session's `qualify-aws.yml`/`qualify-gcp.yml` work (issues
#3/#16/#17). Built, reviewed via local k3d mechanics smoke testing (cluster
creation, CRD/chart install, and - deliberately - teardown's own
failure-mode handling, which is where the AWS-query-masking bug above was
found and fixed), and committed without ever registering a real GitHub
scale set, configuring real AWS credentials, or dispatching a real workflow
- exactly like `qualify-aws.yml`/`qualify-gcp.yml` were.
