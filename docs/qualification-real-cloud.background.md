# Real-cloud qualification: AWS and GCP — background

## Why this exists now, and why AWS and GCP as separate pieces

Issue #3 named the exact remaining gap: "Final real-cloud qualification
still requires isolated provider and GitHub credential references, pinned
images/networks and a numeric spend/VM-time allocation... Local emulator
results do not discharge the real-VM obligations." `tools/emulators.py`'s
own `cloud-emulators` CI job already says as much in its own report
(`'scope': 'emulated cloud APIs, not real cloud VM execution'`) — these
workflows are the real-VM counterpart that claim always implied was still
owed. AWS was built first, as one complete, independently reviewable unit;
GCP followed as a second, separately reviewed unit built by a concurrent
session, deliberately mirroring AWS's settled pattern rather than
reinventing it; Azure is structurally similar but not attempted in either
change — each provider's real-cloud workflow gets its own focused review
rather than one large, harder-to-verify change covering three cloud SDKs'
worth of real infrastructure at once.

## Why the provider adapter, not the whole controller

`kubernetes-integration` and `cloud-emulators` already qualify the
Kubernetes-integration surface and the emulated-cloud-API surface
respectively. What neither can qualify is whether the cloud SDK calls
themselves, against real infrastructure, behave the way `aws.go`/
`aws_inventory.go` (AWS) or `gcp_sdk.go` (GCP) assume. Standing up a full
Kubernetes cluster, Helm install and Operator reconciliation loop just to
exercise one adapter's real-cloud calls would multiply each workflow's
failure surface (cluster bring-up, Helm, scale-set wiring) without adding
real-cloud coverage — every one of those extra layers is already qualified
against fakes elsewhere. Calling the adapter directly, the same way
`emulator_test.go` already does against Moto/ministack, isolates exactly the
one thing that cannot be qualified any other way: real cloud API behavior.

This also fixes the boundary of "actual VM job execution" from issue #3's
wording, for both providers identically. A real GitHub Actions job actually
running on the VM needs a live scale set and a real ephemeral JIT
registration token — that is Operator-and-GitHub-integration surface, not
adapter surface, and pulling it into either change would have re-introduced
exactly the multi-layer failure surface the previous paragraph avoids. It
remains a named, tracked gap in each provider's "Known gaps" section, not a
silently dropped requirement.

## Why OIDC/WIF and AWS's static-key fallback were both built, but GCP's was not

The AWS task considered deferring the static-key fallback as a named
follow-up if supporting both cleanly proved too complex for a first pass. It
did not: `internal/provider/credentials.go`'s `NewCommand`, given a `nil`
environment map, already reads `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/
`AWS_SESSION_TOKEN` directly from the process environment — and
`aws-actions/configure-aws-credentials` exports exactly those three
variables identically regardless of which of its own input modes
(`role-to-assume` via OIDC, or `aws-access-key-id`/`aws-secret-access-key`
static) obtained them. So the only difference between the two AWS
credential modes lives entirely inside one `if`/`else` pair of
`configure-aws-credentials` steps in `qualify-aws.yml` (gated on whether the
`AWS_QUALIFICATION_ROLE_ARN` repository secret is set); the Go test, the
adapter, and every other step downstream of credential configuration have
no branch at all. Supporting both was not "if clean, else defer" — it
turned out to be strictly simpler than picking one and documenting the
other as unsupported.

GCP's `gcp_credentials.go` has the exact same shape at the code level:
`gcpCredential` reads `GOOGLE_APPLICATION_CREDENTIALS`/
`CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE` as a path to a credential *file*,
and dispatches on that file's own `type` field (`service_account`,
`external_account`, ...) — so a static service-account-key JSON file would,
mechanically, work through exactly the same path a WIF-generated
`external_account` config file does. This task's own instructions were
explicit, though, that GCP's credential path should be WIF-only, no service
account key JSON — and this repository's actual provisioned qualification
identity (`GCP_QUALIFICATION_WORKLOAD_IDENTITY_PROVIDER`/
`GCP_QUALIFICATION_SERVICE_ACCOUNT`/`GCP_QUALIFICATION_PROJECT_ID`,
confirmed via `gh variable list` at the time this was written) was
provisioned as WIF-only, with no matching key secret. So unlike AWS's static
fallback (cheap to support *and* already provisioned as a real fallback
path), a GCP static-key fallback here would be speculative machinery for a
credential shape nothing provisions and the task explicitly asked not to
build — the same "build it inert" pattern that applies to *authorization*
(the repository variables) does not extend to inventing an unrequested,
unprovisioned second *credential mode*. This is a genuine, reasoned
divergence from AWS's shape, not an oversight.

## Why a second confirmation phrase beyond workflow_dispatch

`workflow_dispatch` alone already requires a human to explicitly trigger a
run. `confirm_real_spend`'s exact-phrase requirement is a deliberate second
speed bump specifically against a *scripted* dispatch — `gh workflow run
qualify-aws.yml -f aws_region=...` (or `qualify-gcp.yml -f gcp_project=...`)
composed once and reused, or a templated automation, could otherwise
re-trigger real spend with no human actually reading a confirmation at the
time of that particular run. Requiring an exact, unusual literal string (not
a boolean, not "yes") means the phrase has to be deliberately retyped or
copy-pasted with intent each time, not defaulted or scripted away casually.
Both workflows use the identical literal phrase and check it independently
in both the workflow's own first step and the Go test's own
`requireRealCloudConfirmation`/`requireGCPRealCloudConfirmation` — two
distinctly-named functions, deliberately not shared, so
`gcp_realcloud_test.go` and `aws_realcloud_test.go` can live in the same
package without either one's identifiers colliding with the other's.

## Why `vpc_id`/`gcp_network` are inputs at all, never passed to `Config`

`internal/provider.Config` has no VPC field for AWS and no network field for
GCP — `createAWS`/`awsInventory` trust `Config.Subnet`/`Config.SecurityGroup`
directly, and `createGCP` trusts `Config.Subnet` (a subnetwork) directly,
neither ever cross-checking which parent network either belongs to. That is
a reasonable adapter-level design in both cases (the adapter's job is to use
the subnet/subnetwork it is given, not to validate an operator's own network
topology) but it means neither adapter provides any protection against a
pasted-wrong-subnet mistake. Each workflow's own pre-flight step exists
precisely to supply that missing cross-check, entirely outside the adapter:
`vpc_id`/`gcp_network` are never passed to the Go program at all (there is
nowhere in `Config` for either to go) — they exist solely so a read-only
describe call can confirm, before any billable call, that the two pinned
identifiers actually belong together.

## Why GCP needs a `gcp_zone` input AWS has no equivalent for

`qualify-aws.yml` derives the instance's Availability Zone from `subnet_id`
itself, via a read-only `describe-subnets` call, rather than taking it as a
separate input — an AWS subnet lives in exactly one AZ, so the AZ cannot
drift from the pinned subnet, and asking for it separately would just invite
a mismatch between two inputs describing the same fact. GCP's subnetworks
are **regional**, not zonal: a given subnetwork is valid for launch into any
zone within its region, so there is no equivalent single source of truth to
derive a zone from. `gcp_zone` is therefore its own required, pinned input,
cross-validated directly against `gcp_region` (via `gcloud compute zones
describe`) rather than inferred from the subnetwork the way AWS's AZ is.

## Why there is no `architecture` input for GCP

`aws.go`'s `createAWS` actively cross-validates the pinned AMI's real
architecture against `Offering.Architecture` before ever calling
`RunInstances` — a genuine safety check, not just descriptive metadata — so
`qualify-aws.yml`'s `architecture` input feeds a real guard. `gcp_sdk.go`'s
`createGCP` never reads `Offering.Architecture` at all; nothing in the GCP
adapter cross-validates `machine_type` against `gcp_image`'s real
architecture. Adding an `architecture` input to `qualify-gcp.yml` anyway
would have looked like parity with AWS while actually being decorative —
worse than omitting it, because a reader would reasonably assume it does
what AWS's does. This is named explicitly in
[qualification-real-cloud.md](qualification-real-cloud.md)'s GCP "Known
gaps" section instead: a real, adapter-level gap this qualification
workflow surfaces but does not fix (fixing it would mean changing
`gcp_sdk.go` itself, out of scope for a qualification-workflow task).

## Why GCP's qualification identity is stored as repository *variables*, not secrets

AWS's `AWS_QUALIFICATION_ROLE_ARN` is stored as a secret even though a role
ARN is not sensitive on its own, to match how it was actually provisioned in
that repository. GCP's `GCP_QUALIFICATION_PROJECT_ID`/
`GCP_QUALIFICATION_SERVICE_ACCOUNT`/
`GCP_QUALIFICATION_WORKLOAD_IDENTITY_PROVIDER` were, independently, already
provisioned as repository **variables** (confirmed via `gh variable list`;
`gh secret list` at the same time showed only the AWS role ARN). The
instruction behind this piece of work was explicit: use whatever naming and
secret/variable convention was actually set up, not a convention invented to
match AWS. All three GCP values are, like the AWS role ARN, non-sensitive on
their own — the actual trust boundary in both cases is enforced server-side
(AWS's IAM role trust policy; GCP's WIF pool/provider trust condition and
attribute mapping), not by the confidentiality of these identifiers — so
storing them as variables is a legitimate, independent choice, not an
inconsistency to reconcile.

## Why a second, independent GCP client for verification, and what independence means here

Issue #3 asks specifically for "independent cleanup inventory," not "the
adapter's own opinion that cleanup succeeded." `aws_realcloud_test.go`
builds its verification client (`newIndependentEC2Client`) through the
plain `aws-sdk-go-v2/config` default credential chain rather than reusing
`Command.AWS`'s internal `awsCredentialScope`/`session()`.
`gcp_realcloud_test.go` does the same thing structurally
(`newIndependentGCPComputeService`, via `compute.NewService(ctx)` with no
explicit credential option, letting `google.golang.org/api/compute/v1`
resolve Application Default Credentials itself) rather than reusing
`Command.GCP`'s internal `newGCPSDK`/`gcpFileCredential`/`gcpAuthTransport`
construction. In both workflows' cases this is not independence of
*identity* — there is only one set of credentials available to the job
either way — it is independence of *code path*: the running/RUNNING-state
wait, the post-delete existence re-query, and the post-delete leftover-
resource re-query all go through code the adapter itself never executes, so
a bug that made the adapter's own `Observe`/`Delete` wrongly report success
would not also make this verification agree.

One genuine difference: `google.golang.org/api/compute/v1`'s generated REST
client ships no built-in operation waiter equivalent to
`aws-sdk-go-v2/service/ec2`'s `NewInstanceRunningWaiter`/
`NewInstanceTerminatedWaiter`. `gcp_realcloud_test.go` therefore implements
its own small poll loops (`waitForGCPInstanceStatus`/
`waitForGCPInstanceAbsent`) rather than pulling in a second, heavier GCP
client library solely to obtain a waiter — a deliberately simple,
in-file solution sized to the one thing it needs to do.

## Why GCP's real-cloud test does not observe a real Spot price

`aws_realcloud_test.go` observes a real EC2 Spot price because
`internal/prices.AWSSpotClient` genuinely exists and is wired into
`Command.AWS.SpotPrices()`. No GCP equivalent exists anywhere in this
codebase: this session's own investigation for issue #2 (see
[../internal/prices/gcp.md](../internal/prices/gcp.md) and
[../internal/prices/gcp.background.md](../internal/prices/gcp.background.md))
concluded that a GCP spot price client is not honestly buildable without
guessing — Compute Engine's public Cloud Billing Catalog SKUs split
Core/RAM pricing with no structured machine-type field, no zone-level
pricing, and no request-side filter capable of resolving "the price of this
exact machine type in this exact zone right now" the way AWS's
`DescribeSpotPriceHistory` or Azure's retail prices API can. Building a
qualification test that pretended to observe a GCP price by fabricating or
approximating one would qualify a lie, not a capability. Instead,
`gcp_realcloud_test.go` records this omission explicitly, in its own
evidence manifest and in its header comment, exactly as instructed: name
the gap, do not silently skip it. This is not a "for now" deferral without a
plan — issue #2 already tracks the pricing gap itself; this piece simply
does not manufacture a qualification result issue #2 hasn't earned yet.

## Why GCP's real-Spot-preemption limit reasoning differs from AWS's

AWS provides no API to force a Spot interruption on demand — a simple,
unambiguous "does not exist" the AWS piece could state directly. GCP's
answer required more care before writing it down. GCP does document
`gcloud compute instances simulate-maintenance-event`, which can terminate
a Spot instance (Spot VMs cannot live-migrate, so a simulated host
maintenance event on one results in termination per Google's own
documentation on that command's behavior). It would have been easy to reach
for that as GCP's answer to AWS's "no on-demand API." It was deliberately
not used here: `gcp_sdk.go`'s `gcpConfirmedPreemption` looks specifically
for a `compute.instances.preempted` zone-operation record, and
`simulate-maintenance-event` is documented as a host-maintenance/
live-migration testing tool, not a preemption-specific one — nothing
confirms it produces that exact operation type rather than some other
maintenance-triggered termination record. Using it here without that
confirmation would risk a false qualification result: a "passing" test that
actually validated the wrong code path, or worse, a "failing" one that
looked like a real regression in `gcpConfirmedPreemption` when the real
cause was just a different (correct) termination reason never intended to
match it. Given that ambiguity, and this task's explicit instruction to
check first rather than assume parity with AWS, the honest conclusion is
that GCP provides no *confirmed-equivalent, on-demand, safe-to-rely-on* way
to force the specific event this test would need — functionally the same
outcome as AWS's flat "no API," reached by verifying rather than assuming.

## Why deletion has its own fixed, separate time budget

`max_runtime_minutes` bounds how long the qualification is willing to keep a
real VM running while *confirming it works* (create, wait for
running/RUNNING, observe, and — for AWS only — price) — that is the actual
"VM-time allocation" issue #3 asks to be numerically bounded. Deletion is a
different kind of obligation: it must always be attempted in full, never
truncated because an earlier phase used up the budget. Deriving the
teardown deadline from whatever was left of `max_runtime_minutes` would
create exactly the wrong incentive under time pressure (a slow "confirm it
works" phase would leave less time to guarantee cleanup, the one step that
must never be shortchanged). A fixed, hard-coded 5-minute budget, independent
of the input entirely, avoids that coupling — identical in both workflows.

## Why the workflow-level `if: always()` step is the *authoritative*
## backstop, not the Go test's `t.Cleanup`

`t.Cleanup` only runs if the Go test process is still alive to run it. A
job-level `timeout-minutes` kill or a manual cancellation terminates the
process outright, skipping any registered `t.Cleanup` entirely — this is
exactly the scenario each doc's "Guaranteed cleanup" section describes. Each
workflow's final step is written to survive that: it depends on nothing
(its `if: always()`) and computes everything it uses (allocation id, region/
project/zone) from job-level `env:`/`inputs` directly rather than any prior
step's output, so it still runs correctly even when every earlier step,
including the Go test itself, never got that far. GCP's version additionally
guards on `gcloud` actually being installed (`setup-gcloud` never ran if
credential configuration itself failed) before attempting any sweep — a
guard AWS's equivalent step does not need, since the AWS CLI ships
preinstalled on `ubuntu-24.04` runners and GCP's `gcloud` CLI, as of this
writing, does not.

## Provenance

AWS piece written 2026-09-13/14 for the AWS piece of issue #3's remaining
real-cloud scope and issue #2's related real-pricing scope, following this
session's WireGuard `NetworkProfile` and GitHub Release work (issues
#16/#17). No AWS secrets or variables existed in the repository at the time
that piece was written; it was built, reviewed and committed entirely
without ever running it or provisioning any real AWS resource. A follow-up
commit on the same branch (`fix(qualification): read AWS role ARN from
secrets, not vars`) corrected a bug found once `AWS_QUALIFICATION_ROLE_ARN`
was actually provisioned as a secret: the workflow had read it as
`vars.AWS_QUALIFICATION_ROLE_ARN`, always empty, silently forcing the
static-key path every time.

GCP piece written 2026-09-14 by a concurrent session building the GCP
equivalent while the AWS piece sat merge-pending as PR #75 (it merged, as
#75, partway through this GCP piece's own work — this branch was rebased
onto that merge rather than left to drift against a moving `main`),
deliberately reading that PR's exact committed shape first (not just this
document) to mirror it faithfully rather than re-deriving the pattern
independently and risking silent drift between the two. Unlike the AWS piece, this one found
real GCP credentials already provisioned
(`GCP_QUALIFICATION_PROJECT_ID`/`GCP_QUALIFICATION_SERVICE_ACCOUNT`/
`GCP_QUALIFICATION_WORKLOAD_IDENTITY_PROVIDER` repository variables,
confirmed via `gh variable list`) — meaning `qualify-gcp.yml`, unlike
`qualify-aws.yml` at the time it was written, is not inert if dispatched
today. It was, like the AWS piece, built, reviewed and committed without
ever being dispatched.
