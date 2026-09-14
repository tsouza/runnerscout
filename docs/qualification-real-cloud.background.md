# Real-cloud qualification: AWS — background

## Why this exists now, and why AWS only

Issue #3 named the exact remaining gap: "Final real-cloud qualification
still requires isolated provider and GitHub credential references, pinned
images/networks and a numeric spend/VM-time allocation... Local emulator
results do not discharge the real-VM obligations." `tools/emulators.py`'s
own `cloud-emulators` CI job already says as much in its own report
(`'scope': 'emulated cloud APIs, not real cloud VM execution'`) — this
workflow is the real-VM counterpart that claim always implied was still
owed. AWS was built first, as one complete, independently reviewable unit;
Azure and GCP are structurally similar but deliberately not attempted in
the same change, so each provider's real-cloud workflow gets its own
focused review rather than one large, harder-to-verify change covering
three cloud SDKs' worth of real infrastructure at once.

## Why the provider adapter, not the whole controller

`kubernetes-integration` and `cloud-emulators` already qualify the
Kubernetes-integration surface and the emulated-cloud-API surface
respectively. What neither can qualify is whether the AWS SDK calls
themselves, against real EC2, behave the way `aws.go`/`aws_inventory.go`
assume. Standing up a full Kubernetes cluster, Helm install and Operator
reconciliation loop just to exercise that one adapter's real-cloud calls
would multiply this workflow's failure surface (cluster bring-up, Helm,
scale-set wiring) without adding real-AWS coverage — every one of those
extra layers is already qualified against fakes elsewhere. Calling the
adapter directly, the same way `emulator_test.go` already does against
Moto/ministack, isolates exactly the one thing that cannot be qualified any
other way: real AWS API behavior.

This also fixes the boundary of "actual VM job execution" from issue #3's
wording. A real GitHub Actions job actually running on the VM needs a live
scale set and a real ephemeral JIT registration token — that is Operator-
and-GitHub-integration surface, not adapter surface, and pulling it into
this change would have re-introduced exactly the multi-layer failure
surface the previous paragraph avoids. It remains a named, tracked gap
(`docs/qualification-real-cloud.md`'s "Known gaps"), not a silently dropped
requirement.

## Why OIDC and the static-key fallback were both built, not deferred

The task considered deferring the static-key fallback as a named follow-up
if supporting both cleanly proved too complex for a first pass. It did not:
`internal/provider/credentials.go`'s `NewCommand`, given a `nil`
environment map, already reads `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/
`AWS_SESSION_TOKEN` directly from the process environment — and
`aws-actions/configure-aws-credentials` exports exactly those three
variables identically regardless of which of its own input modes
(`role-to-assume` via OIDC, or `aws-access-key-id`/`aws-secret-access-key`
static) obtained them. So the only difference between the two credential
modes lives entirely inside one `if`/`else` pair of
`configure-aws-credentials` steps in the workflow (gated on whether
`vars.AWS_QUALIFICATION_ROLE_ARN` is set); the Go test, the adapter, and
every other step downstream of credential configuration have no branch at
all. Supporting both was not "if clean, else defer" — it turned out to be
strictly simpler than picking one and documenting the other as unsupported.

## Why a second confirmation phrase beyond workflow_dispatch

`workflow_dispatch` alone already requires a human to explicitly trigger a
run. `confirm_real_spend`'s exact-phrase requirement is a deliberate second
speed bump specifically against a *scripted* dispatch — `gh workflow run
qualify-aws.yml -f aws_region=... ` composed once and reused, or a
templated automation, could otherwise re-trigger real spend with no
human actually reading a confirmation at the time of that particular run.
Requiring an exact, unusual literal string (not a boolean, not "yes") means
the phrase has to be deliberately retyped or copy-pasted with intent each
time, not defaulted or scripted away casually.

## Why `vpc_id` is an input at all

`internal/provider.Config` has no VPC field — `createAWS`/`awsInventory`
trust `Config.Subnet`/`Config.SecurityGroup` directly and never cross-check
which VPC either belongs to. That is a reasonable adapter-level design
(the adapter's job is to use the subnet/security group it is given, not to
validate an operator's own network topology) but it means the adapter
itself provides zero protection against a pasted-wrong-subnet mistake. The
workflow's own pre-flight step exists precisely to supply that missing
cross-check, entirely outside the adapter: `vpc_id` is never passed to the
Go program at all (there is nowhere in `Config` for it to go) — it exists
solely so `describe-subnets`/`describe-security-groups` can confirm, before
any billable call, that the two pinned identifiers actually belong together.

## Why a second, independent EC2 client for verification

Issue #3 asks specifically for "independent cleanup inventory," not "the
adapter's own opinion that cleanup succeeded." `aws_realcloud_test.go`
builds its verification client (`newIndependentEC2Client`) through the
plain `aws-sdk-go-v2/config` default credential chain rather than reusing
`Command.AWS`'s internal `awsCredentialScope`/`session()`. In this
workflow's case both ultimately resolve the same underlying credentials
(there is only one set of AWS credentials available to the job), so this is
not independence of *identity* — it is independence of *code path*: the
running-state wait, the post-delete instance-terminated wait, and the
post-delete volume/network-interface re-query all go through code the
adapter itself never executes, so a bug that made the adapter's own
`Observe`/`Delete` wrongly report success would not also make this
verification agree.

## Why deletion has its own fixed, separate time budget

`max_runtime_minutes` bounds how long the qualification is willing to keep
a real VM running while *confirming it works* (create, wait for running,
observe, price) — that is the actual "VM-time allocation" issue #3 asks to
be numerically bounded. Deletion is a different kind of obligation: it must
always be attempted in full, never truncated because an earlier phase used
up the budget. Deriving the teardown deadline from whatever was left of
`max_runtime_minutes` would create exactly the wrong incentive under time
pressure (a slow "confirm it works" phase would leave less time to
guarantee cleanup, the one step that must never be shortchanged). A fixed,
hard-coded 5-minute budget, independent of the input entirely, avoids that
coupling.

## Why the workflow-level `if: always()` step is the *authoritative*
## backstop, not the Go test's `t.Cleanup`

`t.Cleanup` only runs if the Go test process is still alive to run it. A
job-level `timeout-minutes` kill or a manual cancellation terminates the
process outright, skipping any registered `t.Cleanup` entirely — this is
exactly the scenario `docs/qualification-real-cloud.md`'s two-layer
cleanup section describes. The workflow's final step is written to survive
that: it depends on nothing (its `if: always()`) and computes everything it
uses (`RUNNERSCOUT_QUALIFY_ALLOCATION_ID`, `RUNNERSCOUT_QUALIFY_REGION`)
from job-level `env:`/`inputs` directly rather than any prior step's
output, so it still runs correctly even when every earlier step, including
the Go test itself, never got that far.

## Provenance

Written 2026-09-13/14 for the AWS piece of issue #3's remaining real-cloud
scope and issue #2's related real-pricing scope, following this session's
WireGuard `NetworkProfile` and GitHub Release work (issues #16/#17). No AWS
secrets or variables existed in the repository at the time this was
written (confirmed via `gh secret list`/`gh variable list`); this workflow
was built, reviewed and committed entirely without ever running it or
provisioning any real AWS resource.
