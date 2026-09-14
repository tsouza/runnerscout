# Real-cloud qualification: AWS, Azure and GCP — background

> **Note on file references below:** this narrative was written while AWS,
> Azure and GCP each had their own separate workflow file
> (`qualify-aws.yml`/`qualify-azure.yml`/`qualify-gcp.yml`). Those three were
> later consolidated into a single matrixed `.github/workflows/qualify.yml`
> (a `provider` input selects `all` or one of `aws`/`azure`/`gcp`) — see
> [qualification-real-cloud.md](qualification-real-cloud.md) for the current
> file layout. The reasoning below is unaffected: only where each check
> lives changed, not why it exists. A later change (issue #89, see "Network
> provisioning" below) removed `vpc_id`/`vnet_id`/`gcp_network` and their
> sibling subnet/security-group/NSG inputs entirely — the "Why `vpc_id`/
> `vnet_id`/`gcp_network` are inputs at all" section below describes a
> cross-validation design that no longer exists; it is kept for why
> operator-pinned network identifiers were once cross-checked at all, which
> still explains why the network is provisioned per-run today rather than
> left entirely unvalidated.

## Why this exists now, and why AWS first

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
reinventing it; Azure came third, following the same reviewed pattern,
adapted to Azure's real API shapes rather than copied blindly — see
"Azure" below for exactly where and why it differs. Each provider's
real-cloud workflow gets its own focused review rather than one large,
harder-to-verify change covering three cloud SDKs' worth of real
infrastructure at once.

## Why the provider adapter, not the whole controller

`kubernetes-integration` and `cloud-emulators` already qualify the
Kubernetes-integration surface and the emulated-cloud-API surface
respectively. What neither can qualify is whether the cloud SDK calls
themselves, against real infrastructure, behave the way `aws.go`/
`aws_inventory.go` (AWS), `azure.go`/`azure_sdk.go` (Azure) or `gcp_sdk.go`
(GCP) assume. Standing up a full Kubernetes cluster, Helm install and
Operator reconciliation loop just to exercise one adapter's real-cloud
calls would multiply each workflow's failure surface (cluster bring-up,
Helm, scale-set wiring) without adding real-cloud coverage — every one of
those extra layers is already qualified against fakes elsewhere. Calling
the adapter directly, the same way `emulator_test.go`/`azure_emulator_test.go`
already do against Moto/ministack/floci-az, isolates exactly the one thing
that cannot be qualified any other way: real cloud API behavior.

This also fixes the boundary of "actual VM job execution" from issue #3's
wording, for all three providers identically. A real GitHub Actions job
actually running on the VM needs a live scale set and a real ephemeral JIT
registration token — that is Operator-and-GitHub-integration surface, not
adapter surface, and pulling it into any of these workflows would have
re-introduced exactly the multi-layer failure surface the previous
paragraph avoids. It remains a named, tracked gap in each provider's own
"Known gaps" section (`docs/qualification-real-cloud.md`), not a silently
dropped requirement.

## Why there is no second confirmation phrase beyond workflow_dispatch

This workflow originally required a `confirm_real_spend` input matching an
exact literal phrase (`I-UNDERSTAND-THIS-COSTS-REAL-MONEY`), checked both
in `qualify.yml` itself and, independently, inside each
`*_realcloud_test.go` file (`requireRealCloudConfirmation`, reading
`RUNNERSCOUT_QUALIFY_CONFIRM` from the process environment) - a deliberate
second speed bump on top of `workflow_dispatch`, reasoned at the time as
protection against a *scripted* dispatch (`gh workflow run qualify.yml -f
...`) composed once and reused, or a templated automation, re-triggering
real spend with no human actually reading a confirmation at the time of
that particular run.

This was removed. The reasoning it was built on turned out not to survive
contact with how this workflow is actually used: `workflow_dispatch`
already requires a human with write access to this repository to choose to
run it, with these specific inputs, every single time - there is no
"leftover default" or "stale reused invocation" risk the way there can be
with, say, a config file committed once and forgotten, because a dispatch
is an explicit act taken at the moment it happens, not a standing setting
that could silently apply again later. A second exact-phrase gate on top
of that added real, ongoing friction (one more required input to
paste correctly into every single dispatch, forever) for a threat model -
an automated system somehow acquiring write access to this repository and
scripting dispatches - that a second string in the same dispatch command
does nothing to actually stop; anything capable of calling `gh workflow
run qualify.yml -f provider=aws -f ...` is equally capable of appending one
more `-f confirm_real_spend=...` to that same call. The phrase never
protected against automation with access; it only added a step for the
human who already has access and has already decided to run this. This
mirrors, exactly, the reasoning that led to removing network provisioning's
own separate `confirm`-style gate in the same workflow (see "Network
provisioning" below): the act of dispatching, with real inputs, already is
the confirmation.

The corresponding `requireRealCloudConfirmation`/`realCloudConfirmPhrase`
Go-level guard (shared byte-for-byte across `aws_realcloud_test.go`,
`azure_realcloud_test.go` and `gcp_realcloud_test.go`) was removed
alongside it - keeping that guard while removing the workflow-level input
would have just made every real dispatch fail outright (the test would
read an always-empty `RUNNERSCOUT_QUALIFY_CONFIRM` and immediately
`t.Fatalf`), not preserved any actual safety property.

## Why `vpc_id`/`vnet_id`/`gcp_network` are inputs at all, never passed to `Config`

`internal/provider.Config` has no VPC field for AWS, no VNet field for
Azure, and no network field for GCP — `createAWS`/`awsInventory` trust
`Config.Subnet`/`Config.SecurityGroup` directly, `createAzure` trusts
`Config.Subnet`/`Config.SecurityGroup` (ARM resource IDs) directly, and
`createGCP` trusts `Config.Subnet` (a subnetwork) directly, none of them
ever cross-checking which parent network any of these belong to. That is a
reasonable adapter-level design in every case (the adapter's job is to use
the subnet/subnetwork it is given, not to validate an operator's own
network topology) but it means no adapter provides any protection against
a pasted-wrong-subnet mistake. Each workflow's own pre-flight step exists
precisely to supply that missing cross-check, entirely outside the
adapter: `vpc_id`/`vnet_id`/`gcp_network` are never passed to the Go
program at all (there is nowhere in `Config` for any of them to go) — they
exist solely so a read-only describe call can confirm, before any billable
call, that the pinned identifiers actually belong together. Azure's own
`vnet_id`/`subnet_id` pair gets an additional, cheaper first check no
other provider has: because Azure ARM resource IDs self-describe their own
subscription/resource-group/provider/type in their literal path,
`qualify-azure.yml`'s pre-flight can structurally confirm `subnet_id` is a
`/subnets/*` child of `vnet_id` by string comparison alone, with zero API
calls, before the API-level cross-check (`az resource show`) that AWS and
GCP also each perform.

## Why GCP needs a `gcp_zone` input AWS does not have an equivalent for

`qualify-aws.yml` derives the instance's Availability Zone from `subnet_id`
itself, via a read-only `describe-subnets` call, rather than taking it as a
separate input — an AWS subnet lives in exactly one AZ, so the AZ cannot
drift from the pinned subnet, and asking for it separately would just
invite a mismatch between two inputs describing the same fact.
`qualify-azure.yml` takes `availability_zone` as its own required input for
a different reason: it is a property of the VM resource itself, not
derivable from the subnet the way AWS derives its AZ. GCP's subnetworks
are **regional**, not zonal: a given subnetwork is valid for launch into
any zone within its region, so there is no single source of truth to
derive a zone from at all. `gcp_zone` is therefore its own required,
pinned input, cross-validated directly against `gcp_region` (via `gcloud
compute zones describe`) rather than inferred from the subnetwork the way
AWS's AZ is.

## Why there is no `architecture` input for GCP, but there is for AWS

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
Azure has no per-resource "architecture" concept to validate either — the
managed-image API `azure_image.go` requires has no architecture field at
all — but this is a genuine absence in Azure's own API shape, not an
adapter-level gap the way GCP's is, so it needs no equivalent "Known gaps"
entry.

## Why each provider's qualification identity is stored as repository *variables*, not secrets, except AWS's role ARN

AWS's `AWS_QUALIFICATION_ROLE_ARN` is stored as a secret even though a role
ARN is not sensitive on its own, to match how it was actually provisioned
in that repository. GCP's `GCP_QUALIFICATION_PROJECT_ID`/
`GCP_QUALIFICATION_SERVICE_ACCOUNT`/
`GCP_QUALIFICATION_WORKLOAD_IDENTITY_PROVIDER` and Azure's
`AZURE_QUALIFICATION_CLIENT_ID`/`AZURE_QUALIFICATION_TENANT_ID`/
`AZURE_QUALIFICATION_SUBSCRIPTION_ID` were, independently, provisioned as
repository **variables** instead (confirmed via `gh-tsouza variable list`).
The instruction behind this piece of work was explicit: use whatever
naming and secret/variable convention was actually set up, not a
convention invented to match AWS. All of these values are, like the AWS
role ARN, non-sensitive on their own — the actual trust boundary in every
case is enforced server-side (AWS's IAM role trust policy; GCP's WIF
pool/provider trust condition and attribute mapping; Azure AD's federated
credential subject match), not by the confidentiality of these
identifiers — so storing them as variables is a legitimate, independent
choice per provider, not an inconsistency to reconcile.

`qualify-azure.yml` was initially written reading `secrets.AZURE_QUALIFICATION_CLIENT_ID`/
`secrets.AZURE_QUALIFICATION_TENANT_ID` — a real bug, caught during review
once these values were confirmed to actually exist as `vars.*`, not
`secrets.*` (mirroring the exact same class of bug AWS's own
`AWS_QUALIFICATION_ROLE_ARN` hit and had fixed first — see "Provenance"
below). Both references were corrected to `vars.*` before this workflow
was ever pushed.

## Why OIDC and AWS's static-key fallback were both built, not deferred

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
identity was provisioned as WIF-only, with no matching key secret. So
unlike AWS's static fallback (cheap to support *and* already provisioned
as a real fallback path), a GCP static-key fallback here would be
speculative machinery for a credential shape nothing provisions and the
task explicitly asked not to build — the same "build it inert" pattern
that applies to *authorization* (the repository variables) does not extend
to inventing an unrequested, unprovisioned second *credential mode*. This
is a genuine, reasoned divergence from AWS's shape, not an oversight. See
"Azure" below for why Azure also has no fallback, for a related but
distinct reason.

## Why a second, independent client for verification, in every provider

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
construction. `azure_realcloud_test.go`'s `newIndependentAzureClients`
follows the same pattern via `azidentity.NewDefaultAzureCredential` rather
than `AzureSDK.credentials()`/`credentials.go`'s `azureCredential` (see
"Azure" below for the one Azure-specific nuance: a typed client, not just
an independently-constructed one, is required here). In every case this is
not independence of *identity* — there is only one set of credentials
available to the job either way — it is independence of *code path*: the
running-state wait, the post-delete existence re-query, and the
post-delete leftover-resource re-query all go through code the adapter
itself never executes, so a bug that made the adapter's own
`Observe`/`Delete` wrongly report success would not also make this
verification agree.

One genuine AWS/GCP difference: `google.golang.org/api/compute/v1`'s
generated REST client ships no built-in operation waiter equivalent to
`aws-sdk-go-v2/service/ec2`'s `NewInstanceRunningWaiter`/
`NewInstanceTerminatedWaiter`. `gcp_realcloud_test.go` therefore implements
its own small poll loops (`waitForGCPInstanceStatus`/
`waitForGCPInstanceAbsent`) rather than pulling in a second, heavier GCP
client library solely to obtain a waiter — a deliberately simple, in-file
solution sized to the one thing it needs to do. `azure_realcloud_test.go`
does the same for the same reason (`waitForAzureRunningState`/
`waitForAzureVMAbsent`) — see "Azure" below.

## Why GCP's real-cloud test does not observe a real Spot price

`aws_realcloud_test.go` observes a real EC2 Spot price because
`internal/prices.AWSSpotClient` genuinely exists and is wired into
`Command.AWS.SpotPrices()`. `azure_realcloud_test.go` observes a real
Azure Spot price the same way, via `internal/prices.AzureSpotClient`
(`Command.Azure.SpotPrices()`), against Azure's public, unauthenticated
retail-prices endpoint. No GCP equivalent exists anywhere in this
codebase: this session's own investigation for issue #2 (see
[prices-gcp.md](prices-gcp.md) and
[prices-gcp.background.md](prices-gcp.background.md))
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

## Why GCP's and Azure's real-Spot-interruption limit reasoning differs from AWS's

AWS provides no API to force a Spot interruption on demand — a simple,
unambiguous "does not exist" the AWS piece could state directly. GCP's and
Azure's answers each required more care before writing them down. GCP does
document `gcloud compute instances simulate-maintenance-event`, which can
terminate a Spot instance (Spot VMs cannot live-migrate, so a simulated
host maintenance event on one results in termination per Google's own
documentation on that command's behavior). It would have been easy to
reach for that as GCP's answer to AWS's "no on-demand API." It was
deliberately not used here: `gcp_sdk.go`'s `gcpConfirmedPreemption` looks
specifically for a `compute.instances.preempted` zone-operation record,
and `simulate-maintenance-event` is documented as a host-maintenance/
live-migration testing tool, not a preemption-specific one — nothing
confirms it produces that exact operation type rather than some other
maintenance-triggered termination record. Using it here without that
confirmation would risk a false qualification result: a "passing" test
that actually validated the wrong code path, or worse, a "failing" one
that looked like a real regression in `gcpConfirmedPreemption` when the
real cause was just a different (correct) termination reason never
intended to match it. Given that ambiguity, and this task's explicit
instruction to check first rather than assume parity with AWS, the honest
conclusion is that GCP provides no *confirmed-equivalent, on-demand,
safe-to-rely-on* way to force the specific event this test would need —
functionally the same outcome as AWS's flat "no API," reached by verifying
rather than assuming.

Azure's Compute API, checked with the same care, genuinely has no
on-demand mechanism at all — not even an ambiguous one like GCP's
`simulate-maintenance-event`. The only Azure-side capability that can
simulate a Spot eviction on demand is Azure Chaos Studio, a separate,
distinct, opt-in paid service this codebase does not integrate with
anywhere; standing it up would be new, unrelated scope, not a
qualification-workflow task. `azure_realcloud_test.go` therefore reaches
the same conclusion as AWS's piece by the same route AWS did (a genuine
absence, not an unconfirmed ambiguity like GCP's) — it can only confirm
`azureConfirmedPreemption` correctly reports "not interrupted" against a
real, healthy running instance; that detection logic's actual Event
Grid/Storage Queue-fed behavior is covered separately, against synthesized
payloads, by the existing unit test suite.

## Why deletion has its own fixed, separate time budget

`max_runtime_minutes` bounds how long the qualification is willing to keep
a real VM running while *confirming it works* (create, wait for running/
RUNNING/`PowerState/running`, observe, and — for AWS and Azure — price) —
that is the actual "VM-time allocation" issue #3 asks to be numerically
bounded. Deletion is a different kind of obligation: it must always be
attempted in full, never truncated because an earlier phase used up the
budget. Deriving the teardown deadline from whatever was left of
`max_runtime_minutes` would create exactly the wrong incentive under time
pressure (a slow "confirm it works" phase would leave less time to
guarantee cleanup, the one step that must never be shortchanged). A fixed,
hard-coded 5-minute budget, independent of the input entirely, avoids that
coupling — identical across all three workflows.

## Why the workflow-level `if: always()` step is the *authoritative* backstop, not the Go test's `t.Cleanup`

`t.Cleanup` only runs if the Go test process is still alive to run it. A
job-level `timeout-minutes` kill or a manual cancellation terminates the
process outright, skipping any registered `t.Cleanup` entirely — this is
exactly the scenario each doc's "Guaranteed cleanup" section describes. Each
workflow's final step is written to survive that: it depends on nothing
(its `if: always()`) and computes everything it uses (allocation id, region/
project/subscription/zone) from job-level `env:`/`inputs` directly rather
than any prior step's output, so it still runs correctly even when every
earlier step, including the Go test itself, never got that far. GCP's
version additionally guards on `gcloud` actually being installed
(`setup-gcloud` never ran if credential configuration itself failed)
before attempting any sweep — a guard AWS's and Azure's equivalent steps
do not need, since the AWS CLI ships preinstalled on `ubuntu-24.04`
runners, and Azure's cleanup step reuses the `az` CLI session `azure/login`
already established earlier in the same job (guarded instead on `az
account show` succeeding, which fails the same way if credentials were
never configured).

## Network provisioning (issue #89)

Every provider's network identifiers (`aws_vpc_id`/`aws_subnet_id`/
`aws_security_group_id`, `azure_resource_group`/`azure_subnet_id`/
`azure_nsg_id`, `gcp_network`/`gcp_subnetwork`) were originally
operator-pinned inputs, cross-validated before any create call (see "Why
`vpc_id`/`vnet_id`/`gcp_network` are inputs at all" above). Issue #89
replaced all six with a per-run OpenTofu apply/destroy
(`tools/tofu/qualify-network/{aws,azure,gcp}/`) instead, for a reason that
has nothing to do with safety and everything to do with what an
operator-pinned network actually costs to keep around: AWS's NAT Gateway
bills hourly whether anything uses it or not, so a qualification network
left standing between runs is an unbounded, ongoing cost for a workflow
that is dispatched rarely — provisioning it fresh per run and destroying it
immediately after turns that into a few cents per run instead. Azure's and
GCP's equivalents don't bill hourly, but were changed to match for
consistency across all three providers (an operator reading one provider's
lifecycle should not have to learn a second one for the others), and
because a network cross-validated once and reused indefinitely can also
silently drift from what the workflow's own pre-flight checks last
verified (an out-of-band change to a shared, long-lived VPC/VNet), whereas
one created and destroyed by the same job every run cannot drift at all.

### Why a separate network-provisioner identity per cloud, not the qualification identity reused

The qualification identity (`AWS_QUALIFICATION_ROLE_ARN`,
`AZURE_QUALIFICATION_*`, `GCP_QUALIFICATION_*`) exists to create/observe/
delete a VM inside a network it is handed — nothing about that task needs
permission to create or delete the network itself. Granting it that
permission anyway, purely so one identity could do both jobs, would widen
the blast radius of a single compromised or misused credential to include
being able to stand up or tear down arbitrary VPCs/VNets/VPC networks, not
just the one this workflow's own VM lifecycle needs. A second, disjoint
identity per cloud (`AWS_NETWORK_PROVISIONER_ROLE_ARN`,
`AZURE_NETWORK_PROVISIONER_*`, `GCP_NETWORK_PROVISIONER_*`) keeps each
identity's permissions scoped to exactly the resource types it actually
touches — verified directly against each cloud's real provisioned
role/policy (Azure's custom role has no `Microsoft.Compute/*` action at
all; AWS's policy has no `iam:*` or VPC-unrelated `ec2:*` action; GCP's
custom role has no `compute.instances.*`/`compute.disks.*` action) — and
means a bug or scope-creep in one identity's usage cannot reach the other
identity's resources even within the same job.

### Why no remote OpenTofu state backend

`tools/tofu/qualify-network/*/README.md`'s "State" sections cover this in
full; the short version is that a remote backend (S3+DynamoDB, an Azure
Storage container, a GCS bucket) solves a problem this workflow does not
have. Remote state exists to let multiple, independent `apply`/`destroy`
invocations — from different machines, different times, different actors —
agree on one shared source of truth for what currently exists. This
workflow's own `apply` and `destroy` for a given run are two steps of the
*same* job, on the *same* runner filesystem, always in that order, always
paired — the state file simply never needs to leave the one machine that
created it. Adding a remote backend here would be provisioning
infrastructure (a bucket, a lock table, credentials to reach them) to solve
a coordination problem that literally cannot occur in this workflow's own
usage pattern. The by-hand usage documented in each module's own README
(preparing `tools/e2e/*`'s pinned inputs) is the one case where state
genuinely could need to move between machines — and that path is
explicitly out of scope for what `qualify.yml` itself needs, so it was
left as an operator's own responsibility rather than built out speculatively.

### Provenance: a separately-exposed provision/destroy workflow, tried and reverted

This was first built as a fourth, independently-dispatchable workflow
(`qualify-network.yml`, PRs #90–95) with its own `provision`/`destroy`
`workflow_dispatch` inputs, its own cross-run state handoff (an
`apply_run_id` input pinning which prior run's uploaded-artifact state a
`destroy` dispatch should download and act on), and its own docs
(`docs/qualify-network.md`/`.background.md`). This was built without being
asked for — the actual instruction never asked for network provisioning to
be its own dispatchable surface, only for the compute-side qualification
workflows to stop requiring an operator to pin a pre-existing network.
Once flagged, it was retired outright rather than kept alongside the
simpler design: `qualify-network.yml`, its docs, and its cross-run
artifact-handoff machinery were all deleted, and the same
`tools/tofu/qualify-network/*` modules were instead applied and destroyed
as ordinary steps inside `qualify.yml`'s own existing per-provider matrix
job — apply, run the real-cloud test, destroy, all in one dispatch, one
job, no separate exposure. This also deleted an entire category of
complexity that a dedicated workflow required and the folded-in version
does not: no remote state backend, no artifact upload/download, no
`apply_run_id` pinning, no window in which a network could be left
standing (billing) between a `provision` dispatch and someone remembering
to `destroy` it later.

## Azure

Azure's adapter (`azure.go`/`azure_sdk.go`) and its production credential
resolution (`credentials.go`'s `azureCredential`) already differ from AWS's
in ways that force several of the choices below to differ too — these are
not stylistic variations, they follow directly from what Azure's real APIs
and this codebase's existing Azure code actually do.

### Why there is no client-secret fallback here

AWS's static-key fallback exists because AWS credentials genuinely have two
independently useful shapes (a role to assume, or a long-lived key pair)
that both converge on the same three environment variables downstream —
supporting both turned out to be nearly free (see above). Azure's
`azureCredential` supports several credential *shapes*
(`AZURE_CLIENT_SECRET`, `AZURE_CLIENT_CERTIFICATE_PATH`,
`AZURE_FEDERATED_TOKEN_FILE`, or a managed-identity fallback), but a
client secret is a long-lived, directly-usable bearer credential in a way a
federated GitHub OIDC token is not (the token itself is short-lived and
useless outside the one federated-credential trust relationship that
accepts it) — introducing a second, secret-bearing path here would add a
durable credential this workflow has no need for, purely to mirror AWS's
shape rather than because Azure's own qualification needs it. Workload
Identity Federation alone is sufficient, matches this repository's existing
OIDC-preferred posture (GHCR/cosign in `release-build.yml`, AWS's own OIDC
path above), and is what the task setting this workflow up asked for
explicitly.

### Why two separate OIDC token exchanges in one job

`azure/login` authenticates the `az` CLI session this workflow's own
pre-flight and cleanup steps use — but it does not export any reusable
token or file for a downstream Go SDK to consume; unlike
`aws-actions/configure-aws-credentials`, which exports plain
`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/`AWS_SESSION_TOKEN` environment
variables any AWS SDK anywhere can pick up, Azure has no equivalent
generic, resource-agnostic bearer-token export mechanism baked into
`azure/login`. So the workflow performs a second, independent GitHub OIDC
token exchange (a plain `curl` against `ACTIONS_ID_TOKEN_REQUEST_URL` with
audience `api://AzureADTokenExchange` — the exact audience an Azure AD
federated credential expects), writes it to its own file, and exposes it as
`AZURE_FEDERATED_TOKEN_FILE` alongside `AZURE_CLIENT_ID`/`AZURE_TENANT_ID`.
This is the exact shape `credentials.go`'s `azureCredential` already
resolves via `azidentity.NewWorkloadIdentityCredential` when `NewCommand`
is given a `nil` environment map — so, just like AWS's convergence on three
shared environment variables, no branching is needed anywhere below this
one step: the Go test's `NewCommand(cfg, nil)` call needs no Azure-specific
knowledge of how it got authenticated.

Both `azure/login` and this second token fetch ultimately authenticate as
the *same* Azure AD application via the *same* federated-credential trust
relationship — this is two independent call sites reaching one identity,
not two identities, mirroring exactly the "independence of code path, not
of identity" reasoning documented above, just applied to credential
acquisition rather than verification.

One real, load-bearing consequence of this: a GitHub Actions OIDC token is
short-lived (on the order of minutes), while the Azure AD access token
`WorkloadIdentityCredential` obtains by presenting it is materially
longer-lived (on the order of an hour) and is what actually gets cached and
reused for the rest of the Go test's run. The federated-token-fetch step
therefore runs as late as practical (immediately before the Go test step,
after all pre-flight validation is done) rather than at job start, so the
GitHub token it hands off is as fresh as possible at the one moment it
actually gets exchanged.

### Why `armcompute` is a new, justified dependency

Confirming a real running state independently (mirroring AWS's
`ec2.NewInstanceRunningWaiter`) needs the VM's real runtime power state.
Production Azure code (`azure.go`/`azure_sdk.go`) never needs this and
therefore never imports `armcompute` at all — it only ever constructs the
generic `armresources.Client` (`GetByID`/`BeginDeleteByID`/
`BeginUpdateByID` by full ARM resource ID and API version), because every
production check is either "has this ARM deployment reached a terminal
state" (`azureTerminal`) or "does this resource exist, with the right
tags/identity" (`azureInventory`) — neither needs a VM's power state. The
generic resource GET response does not include `instanceView`/power-state
data under any option this codebase's own `armresources.Client.GetByID`
call takes; only the typed `Microsoft.Compute` VM API's `$expand`
parameter (`armcompute.VirtualMachinesClientGetOptions.Expand`) does. Adding
`armcompute` — solely for `azure_realcloud_test.go`'s own independent
verification, gated behind the `realcloud` build tag — is therefore a real,
necessary addition, not a speculative one; `go.mod` lists it because
`go mod tidy` was run with `GOFLAGS="-tags=realcloud,emulators"` so it is
correctly recorded as a direct (not indirect) dependency despite being
unreachable under this module's default build tags.

### Why `deleteAzure`'s one-resource-per-call shape needs its own teardown loop

AWS's `TerminateInstances` cascades through `DeleteOnTermination` in one
call. Azure's `deleteAzure` (`azure.go`) deletes exactly one dependent
resource per invocation — the VM first, then the NIC, then the disk, each
only once it is no longer `ManagedBy` anything else — and expects the
caller to re-observe and call it again to advance to the next resource.
This is not a shortcut relative to AWS's one-shot delete; the ARM
deployment template's own `deleteOption: "Delete"` on both the NIC and OS
disk already asks Azure to cascade-delete them automatically once the VM
itself is gone, so `deleteAzure`'s per-call, `ManagedBy`-gated design exists
to avoid racing that platform-driven cascade with a redundant explicit
delete, falling back to an explicit delete only for whatever the cascade
did not already remove. `azure_realcloud_test.go`'s `driveAzureTeardown`
therefore calls only the adapter's public `Observe` and `Delete` — never
any unexported adapter internal — in a bounded loop until `Observe` reports
nothing left, exactly reproducing how a real controller would drive this
same reconciliation. Repeated `Delete` calls with an unchanged `Allocation`
value are safe here: `lifecycle.MergeResources`'s union semantics mean a
resource's real-world disappearance never shrinks the durable
`Resources` an allocation is checkpointed against, so `deleteAzure`'s own
"has this changed since last observed" guard never spuriously trips across
the loop's iterations.

### Why a fresh, ephemeral SSH key instead of an operator-provided one

`internal/provider.Command.Validate` requires `Config.SSHPublicKey` to
start with `ssh-` for every Azure deployment (`azure.go`'s
`linuxConfiguration.ssh.publicKeys[].keyData]`, with password authentication
disabled) — Azure's ARM API will not provision the VM at all without a
real-shaped key. AWS has no equivalent requirement. Rather than asking an
operator to provision, store and rotate a real SSH key pair for a
qualification VM nothing is ever meant to log into (this test's `Bootstrap`
is a synthetic placeholder — see `azure_realcloud_test.go`'s header
comment), the test generates a throwaway ed25519 key pair itself at the
start of every run and discards the private half immediately after
encoding the public half. This is strictly less operational surface than a
managed secret would be, for a value nothing downstream ever needs to
verify against.

### Why `Requirements.MaxPriceMicros` is set to Azure's `-1` sentinel

`azure.go`'s Spot `billingProfile.maxPrice` is populated directly from
`Allocation.Requirements.MaxPriceMicros` with no special-case handling.
Azure documents `-1` as its own sentinel meaning "no price cap — evict only
on capacity, pay up to the on-demand rate"; any other value below the
current Spot price causes Azure to refuse to allocate the VM at all. AWS's
`RunInstances` needs no equivalent value — omitting a max price already
means "cap at on-demand" — which is why `aws_realcloud_test.go` never sets
`Requirements` at all. Azure's qualification test sets
`Requirements.MaxPriceMicros = -1_000_000` (micros) for exactly this
reason: a real Azure-specific API requirement, not an AWS-blind default
carried over out of habit.

### Why the independent verification client uses `DefaultAzureCredential`, not a second explicit mode

Mirroring the shared "independent client for verification" reasoning
above: `newIndependentAzureClients` builds its `armresources.Client`/
`armcompute.VirtualMachinesClient` pair through `azidentity`'s
`DefaultAzureCredential` — a chain of several distinct credential types
(environment, workload identity, managed identity, Azure CLI, ...) — rather
than reusing `AzureSDK.credentials()`/`credentials.go`'s `azureCredential`,
which explicitly selects exactly one credential type via its own
if/else-style switch. In this workflow, both ultimately resolve the same
underlying federated identity (the same `AZURE_CLIENT_ID`/`AZURE_TENANT_ID`/
`AZURE_FEDERATED_TOKEN_FILE` environment variables are visible to both), so
again this is independence of *code path*, not of *identity*: the running-
state wait, the post-delete VM-absence wait, and the post-delete tag-based
leftover sweep all go through code the adapter itself never executes.

### Why the independent post-delete sweep is tag-based, not name-based

Production's own `AzureSDK.list()` (`azure_sdk.go`) matches resources by
the three specific names (`<id>`, `<id>-nic`, `<id>-os`) it already expects
to exist. Reusing that same name-based approach for independent
verification would only ever re-confirm the exact set of resource kinds
production already assumes, which is not a meaningfully independent check.
`azure_realcloud_test.go`'s `azureIndependentLeftovers` instead lists every
resource in the resource group tagged `runnerscout-operation=<allocation
id>`, regardless of resource type or name — a strictly stronger check that
would also catch a kind this adapter's template does not even provision
today, such as a public IP address (the deployment template in `azure.go`
never allocates one — this codebase's Azure VMs are reachable only over the
pinned private subnet, for the WireGuard peer network — so none is ever
expected here; the sweep exists as a defensive, always-run assertion of
that fact, not because one has ever appeared).

## What real dispatches found

Every design decision above was reasoned through and reviewed without ever
running this workflow for real - issue #3's own qualification purpose is
precisely to find what that kind of review cannot. The first real
dispatches against all three clouds (2026-09-14, after issue #89 folded
network provisioning into this same workflow) found several real bugs,
none caught by any prior review. Two separate, unrelated identity issues
also blocked the very first attempt at each of these dispatches
(pre-existing, not workflow bugs): this repository has GitHub's
"immutable subject claims" org policy enforced, so the actual presented
OIDC `sub` claim is `repo:<owner>@<owner-id>/<repo>@<repo-id>:ref:...`,
not the plain `repo:<owner>/<repo>:ref:...` every AWS/Azure identity had
originally been provisioned with - fixed by updating each federated
credential's/IAM role's trust condition to the immutable form, for both
the qualification and network-provisioner identity on both clouds (GCP's
WIF attribute mapping was unaffected - it does not do raw `sub`-string
matching). With that fixed, dispatches actually reached this workflow's
own logic, surfacing the bugs below:

- **A GCP test bug, not a production bug** (fixed separately - see that
  fix's own PR/commit for the full diagnosis): `TestQualifyRealGCPSpotLifecycle`
  called `p.Observe` on a virgin allocation ID as a pre-create sanity
  check, which always failed - `observeGCP` unconditionally requires
  evidence of an already-committed create operation, a precondition that
  is correct for how the real controller actually calls `Observe` (only
  ever after a create has already been attempted) but makes it unusable as
  a "does this ID already have leftover resources" check before one.
- **A real Azure CLI incompatibility**: the VM-level safety-net step's `az
  resource list --resource-group "$rg" --tag runnerscout-operation="$alloc"`
  calls error outright - `az resource list` does not accept `--resource-group`
  and `--tag` together. This had never been hit before because every
  earlier dispatch attempt failed even earlier in the job (the OIDC
  subject mismatch above, on the very first attempt) - this specific step
  had simply never run yet. Fixed by moving the tag match into the
  `--query` JMESPath expression instead of the server-side `--tag` filter.
- **Two real AWS IAM gaps, found across successive re-dispatches**: the
  network-provisioner policy was first missing `ec2:DescribeVpcAttribute`,
  needed for `aws_vpc`'s own `enable_dns_hostnames` reconciliation - `tofu
  apply` failed partway through creating the VPC itself (leaving one
  orphaned, free VPC with no subnet/NAT Gateway/route table ever created
  after it - manually verified and cleaned up directly, zero ongoing
  cost), and the equivalent `tofu destroy` failed the same way. With that
  fixed, the next dispatch got further - creating the VPC, subnets and an
  Elastic IP - before failing on the *second* gap:
  `ec2:DescribeAddressesAttribute`, needed for `aws_eip`'s own domain-
  attribute reconciliation (the same "Terraform reads back an attribute
  after creating the resource, and that read needs its own permission"
  shape as the VPC gap, and as GCP's `setLabels`/`setMetadata`/
  `setScheduling` gaps below). This one briefly left a real, billable,
  unassociated Elastic IP (AWS charges for an idle EIP) plus an orphaned
  VPC/subnet pair/internet gateway/security group - manually verified and
  released/deleted directly within minutes of the failure, so real cost
  was negligible (a fraction of an hour's idle-EIP rate). Both gaps were
  fixed by adding the missing permissions to the live role.
- **A third and fourth real AWS IAM gap**, found on a later re-dispatch,
  once the compute-side AMI/instance-type inputs had also been resolved:
  `ec2:DescribeNetworkInterfaces` (needed by `aws_subnet`'s and
  `aws_security_group`'s own destroy logic, which lists and cleans up any
  ENIs still attached before deleting the subnet/security group itself)
  and `ec2:DisassociateAddress` (needed to detach the NAT EIP before
  releasing it). Missing both meant `tofu destroy` failed outright,
  leaving one orphaned, billable, unassociated Elastic IP and a full
  leftover VPC/subnet-pair/internet-gateway/security-group behind -
  manually verified (no running instance, no NAT Gateway - Terraform had
  already deleted that one before hitting the ENI-listing error) and
  cleaned up directly within minutes. Fixed by adding both permissions to
  the live role.
- **A real, one-time AWS-account-level bootstrap gap, not a workflow or
  IAM-policy bug**: with the network-side gaps above all fixed, a dispatch
  reached the actual `RunInstances` call - which failed with
  `Client.AuthFailure.ServiceLinkedRoleCreationNotPermitted: The provided
  credentials do not have permission to create the service-linked role for
  EC2 Spot Instances` (found via CloudTrail, since `createAWS`'s own error
  wrapping - `"AWS create commitment unknown"` - deliberately does not
  leak the real provider error, the same discipline as `gcp_sdk.go`'s
  wrapping). AWS auto-creates the `AWSServiceRoleForEC2Spot`
  service-linked role the first time any identity in an account ever
  requests a Spot Instance - and creating it requires
  `iam:CreateServiceLinkedRole`, a permission the qualification identity
  deliberately does not have (by the same least-privilege design as
  everything else it's scoped to: it launches/observes/deletes compute,
  never touches IAM). This is not a gap in this workflow's own design -
  every AWS account that has never used EC2 Spot before needs this
  one-time, account-level bootstrap regardless of what identity or tool
  eventually requests the first Spot Instance, and granting an ongoing
  qualification identity permission to create service-linked roles on
  demand would be a real, unnecessary widening of its blast radius for a
  need that only ever occurs once per account. Fixed the intended way:
  `aws iam create-service-linked-role --aws-service-name spot.amazonaws.com`,
  run once, directly, with the account's own root/administrator
  credentials - after which every identity in the account can request Spot
  Instances normally, this qualification identity included.
- **A real EC2 API eventual-consistency race, in production code**: with
  the service-linked role bootstrapped, a dispatch's `RunInstances` call
  finally succeeded - and `createAWS`'s own post-create validation still
  failed, with `"AWS create commitment unknown"` this time wrapping a
  different real condition: the response's `Instances[0].BlockDeviceMappings`
  came back empty (`observed` stayed empty against a non-empty `expected`),
  even though the instance and its network interface were both genuinely
  present in that same response. EC2's `RunInstances` response can
  legitimately return before the new instance's EBS volume attachment
  record is fully populated - a real, intermittent API race, not a
  four-oh-something error to catch and branch on. Unlike GCP's analogous
  delete-timeout finding, this one **does** help production: `createAWS`'s
  own 30-second budget has ample room for a short, bounded retry (three
  attempts, two seconds apart, re-querying `DescribeInstances` for the
  same instance ID) before concluding the dependencies are genuinely
  missing, and `internal/operator/operator.go`'s own per-`Step` timeout
  (also 30 seconds) is the same budget the unfixed code already ran
  under - this fix does not need a wider parent budget to matter, unlike
  `deleteGCP`'s timeout. The safety-net step force-terminated the real
  (billed, but briefly-lived) instance this race caused - see the next
  finding for a real bug that surfaced in that same cleanup.
- **A second AWS VM-level safety-net race, exposed by the instance the
  finding above actually created**: after force-terminating a leftover
  instance, the safety net immediately tried to delete its EBS volume -
  racing AWS's own automatic `DeleteOnTermination` cleanup and failing
  with `VolumeInUse` (the instance was still `shutting-down`, not yet
  `terminated`, when the delete-volume call landed). The volume and its
  network interface both carry `DeleteOnTermination = true` already (see
  `createAWS`'s own `RunInstancesInput` construction), so once the
  instance genuinely finishes terminating, both disappear on their own -
  the safety net's own explicit delete calls exist only to catch the case
  where `DeleteOnTermination` itself doesn't fire (a create that never
  even reached instance-launch, for example), not to race an
  already-firing one. Fixed by waiting for `aws ec2 wait
  instance-terminated` right after `terminate-instances`, before touching
  volumes or ENIs at all - bounded by that waiter's own default timeout,
  with `|| true` so a slow/stuck termination still lets the sweep below
  attempt its own cleanup rather than aborting outright. In this
  particular run, real end state was confirmed safe regardless (the
  instance, its volume, and its ENI were all independently verified gone
  within a minute of the run finishing) - this fix removes a spurious job
  failure and a confusing `VolumeInUse` error, not a real leak.
- **A stale-credential bug in the AWS VM-level safety-net step**,
  surfaced by the AWS IAM gap above: when a network `tofu apply` failure
  causes every qualification-identity credential step after it to be
  correctly skipped (implicit `success()` gating), `AWS_ACCESS_KEY_ID` is
  left set to whatever the NETWORK-PROVISIONER identity's credentials
  were, since nothing overwrote them. The AWS VM-level safety-net step
  originally checked only "is `AWS_ACCESS_KEY_ID` non-empty" to decide
  whether qualification credentials were ever configured - true here, just
  for the wrong identity - so it ran anyway and failed with
  `UnauthorizedOperation` trying to call `ec2:DescribeInstances` with a
  role that was never granted that permission, instead of cleanly
  recognizing "the qualification phase never got this far." Fixed by
  checking `RUNNERSCOUT_QUALIFY_ACCOUNT_ID` instead - an env var set only
  by a step gated behind the qualification identity's own auth having
  actually succeeded, not by the mere presence of *some* AWS credential in
  the job environment. GCP's VM-level safety-net step has the exact same
  shape (`GOOGLE_APPLICATION_CREDENTIALS` would suffer the identical
  problem) and was fixed the same way as a preventative measure (a new
  `RUNNERSCOUT_QUALIFY_GCP_AUTH_CONFIRMED` marker) even though no real GCP
  dispatch has actually triggered this specific failure yet - GCP's
  network apply has, so far, either succeeded or failed for reasons
  unrelated to IAM (see below), never yet leaving qualification
  credentials unconfigured while `GOOGLE_APPLICATION_CREDENTIALS` stayed
  set to the network-provisioner's file. Azure's equivalent safety-net
  step never had this bug at all: it checks `az account show` succeeding,
  a live call against whichever identity is *currently* active, not a
  static env-var presence check.
- **A second and third real GCP IAM gap**, found across the next two
  dispatches after the pre-create test bug was fixed: `gcp_sdk.go`'s
  `createGCP` sets labels, metadata (the startup-script) and Spot
  scheduling as part of the `instances.create`/`disks.create` insert calls
  themselves, never via distinct `setLabels`/`setMetadata`/`setScheduling`
  API calls - which had led to the (wrong) conclusion, during a
  documentation pass, that `compute.instances.setLabels`/
  `compute.disks.setLabels` were therefore unnecessary permissions. Real
  dispatches proved that reasoning wrong at the IAM-permission level: GCE's
  own authorization checks for setting labels/metadata/scheduling during
  instance/disk creation are still gated on their respective `setX`
  permissions, not just the `create` permission, even though no separate
  `setX` API method is ever called. Each create attempt failed with a live
  `Required 'compute.<resource>.setX' permission` error in turn (found via
  Cloud Logging, since `gcp_sdk.go`'s own error wrapping - `"GCP creation
  commitment unknown"` - deliberately does not leak the real provider
  error): `compute.disks.setLabels` first, then `compute.instances.setMetadata`.
  Fixed by adding `compute.instances.setLabels`, `compute.disks.setLabels`,
  `compute.instances.setMetadata` and `compute.instances.setScheduling` to
  the live role (the last one added proactively, on the same reasoning,
  once the pattern was clear, before it could cause a fourth failed
  round-trip).
- **A real GCP delete-timeout tuning gap, benefiting this test but not
  production**: with the IAM gaps above fixed, a dispatch finally created
  a real Spot instance successfully - and then failed at `p.Delete`,
  logging `"GCP operation commitment unknown"`. This is `deleteGCP`'s own
  internal 30-second `context.WithTimeout` expiring while still waiting
  for GCP's delete operation to reach `DONE` - not a failed delete: GCP
  keeps processing an already-submitted operation server-side regardless
  of whether the calling context is still watching it, and this test's own
  independent, longer-timeout absence-wait confirmed the instance and its
  boot disk really were gone shortly after. Deleting a real instance -
  which includes detaching/deleting its `PERSISTENT`, `AutoDelete` boot
  disk in the same async operation chain - evidently can take longer to
  reach `DONE` than creating one does (which completed well within its
  own, unchanged, 30-second budget in the same run). Raised `deleteGCP`'s
  internal timeout to 60 seconds - still well inside the 5-minute
  `dedicatedTeardownBudget` this test's own outer retry/independent-wait
  logic budgets for, so this genuinely avoids the spurious test failure
  hit here. It does **not** help production reconciliation the same way,
  though: `operator.go`'s `Step` call wraps every real `Delete` in its own
  30-second `context.WithTimeout`, which caps `deleteGCP`'s internal
  timeout at whatever's left of that shorter parent deadline regardless of
  the 60s value - a child context can never outlive its parent's deadline.
  In production this bug therefore still results in the same outcome it
  always did: one reconciliation `Step` may see this same "commitment
  unknown" error and simply retry on the next tick, which already
  tolerates it correctly (the allocation stays in `Deleting` until
  `Observe` confirms absence). Raising that shared, per-`Step` budget to
  actually help production too would be a separate, broader change (it
  bounds every provider call in every phase, not just GCP's delete) - out
  of scope for this fix.
- **A real Azure VM-size/managed-image compatibility gap, not a bug in
  this workflow's code**: with the OIDC subject mismatch fixed, dispatches
  reached real VM creation and hit a real Azure platform constraint
  instead: `azure_vm_size=Standard_D2als_v7` (the size originally chosen
  for the qualification image-build VM too) failed with `"The VM size
  'Standard_D2als_v7' cannot boot with OS image or disk"` - the classic
  managed image built for this qualification (`Microsoft.Compute/images/...`)
  carries no disk-controller-type metadata of its own, and this VM size's
  family only supports `NVMe`, which Azure won't infer for such an image
  without being told explicitly. Checking every unrestricted VM size in
  this subscription's `eastus` and `westus2` catalogs (`az vm list-skus`)
  found the *entire* general-purpose catalog is `NVMe`-only in both
  regions - the only families supporting `SCSI` at all are
  confidential-computing (`DC*`/`EC*`, which then failed differently:
  `"is not supported for creation of VMs and Virtual Machine Scale Set
  with '<NULL>' security type"` - they require an explicit
  `securityProfile.securityType` this workflow's ARM template does not
  set) and memory-optimized `M`-series (`M12s_v3` and up, minimum 12
  vCPUs) - which then hit a *third*, independent constraint: this
  subscription's default Spot/low-priority core quota in `eastus` is only
  3 vCPUs, well under `M12s_v3`'s 12.

  Added a new, purely additive, opt-in `Config.AzureDiskControllerType`
  field (empty by default, omitting `storageProfile.diskControllerType`
  from the ARM template entirely and preserving Azure's own default
  inference exactly as before this field existed) rather than
  unconditionally hardcoding `"NVMe"` in `createAzure` (would have
  silently broken any VM size that is genuinely `SCSI`-only, with no way
  to know from anything already in `Config`/`Allocation` whether a given
  size supports `NVMe` at all - real backward-compatibility risk to real
  production Azure users on older size families, not justified just to
  unblock this qualification run). `qualify.yml` threads this through as
  a new, optional `azure_disk_controller_type` input (`unset` by default,
  same sentinel-default pattern as `azure_availability_zone`, since choice
  inputs can't have an empty-string option) rather than something this
  workflow infers on its own - matching this repository's consistent
  "operator-supplied explicit configuration, no silent magic" convention
  for every other pinned image/network/compute-shape input.

  **This field turned out narrower than hoped.** The next real dispatch,
  with `azure_disk_controller_type=NVMe` set against the same classic
  managed image, was rejected outright by Azure itself:
  `InvalidParameter: "Disk controller type 'NVMe' not supported for user
  VM image."` A classic managed image's own implicit `SCSI` default
  cannot be overridden at VM-request time, no matter what
  `diskControllerType` value is sent - the ARM API accepts the field
  syntactically but still rejects the combination. So
  `azure_disk_controller_type` cannot actually unlock an `NVMe`-only VM
  size for a classic managed image; the field remains correctly-designed
  and genuinely useful for the narrower case of a size that supports
  *both* controller types where Azure's own inference picks the wrong one
  (a real, if smaller, case), but does not solve the actual problem this
  investigation set out to solve. With every unrestricted, non-
  confidential-computing VM size in this subscription's `eastus`/
  `westus2`/`centralus`/`eastus2`/`southcentralus`/`northeurope` catalogs
  checked directly (`az vm list-skus`) and found to be either `NVMe`-only
  or ARM-architected (`Dpxx`-class, incompatible with this qualification's
  `amd64` image regardless of controller type), this subscription
  genuinely has no compatible, cheap, `x86_64`, non-confidential VM size
  for a classic managed image at all. Two real fixes remained: teaching
  `validateAzureImage` (`azure_image.go`) to also accept a Compute Gallery
  image version's resource type (gallery images *do* support declaring
  `NVMe` support at the image-definition level, correctly, unlike a
  classic managed image), or adding `securityProfile.securityType` support
  for confidential computing. Compute Gallery support was built (below);
  confidential-computing support was not attempted, being a materially
  larger, riskier, and less broadly useful adapter change for the same
  underlying problem.

- **Compute Gallery image version support, added to resolve the above**:
  `validateAzureImage` now branches on the image resource's parsed
  `ResourceType` (`Microsoft.Compute/images` vs.
  `Microsoft.Compute/galleries/images/versions`, both confirmed directly
  against `arm.ParseResourceID`'s real output shape - a 3-level nested
  resource ID's `ResourceType.String()` returns the full nested path
  joined by `/`, not just the leaf segment) rather than hard-requiring the
  classic shape. The two validation paths genuinely differ, not just in
  resource type string: a gallery image version's own properties
  (`storageProfile.dataDiskImages`, `provisioningState`) say nothing about
  `osType`/`osState` - those live on the *parent* image definition
  (`Microsoft.Compute/galleries/images`) instead, requiring a second GET
  the classic-image path never needs. Both API calls use API version
  `2024-03-03`, confirmed directly against the real Azure API (via `az
  rest`) to carry every property either validation path reads, rather than
  assumed from the classic image path's own (different, newer) pinned
  version. A real gallery, image definition (with `DiskControllerTypes:
  SCSI, NVMe` declared explicitly) and image version were built from the
  already-captured classic image as its source (`az sig image-version
  create --managed-image <classic-image-id>` - no VM rebuild needed),
  confirming the whole chain end-to-end against real Azure resources, not
  just unit-test fixtures.
- **Two more real gaps, found by the first two dispatches against the new
  gallery image version**: `qualify.yml`'s own bash pre-flight `case`
  statement (validating `azure_image_id`'s shape before any cloud
  credential is even configured) was never updated alongside
  `validateAzureImage`'s new acceptance of a gallery image version - it
  still only matched the classic `Microsoft.Compute/images/...` shape,
  rejecting a genuinely valid gallery image version outright
  (`"does not look like a Microsoft.Compute/images resource ID"`) before
  the Go test ever ran. Fixed by adding the matching gallery-shape case
  pattern, verified directly with a standalone bash test against the real
  image ID. With that fixed, the next dispatch reached
  `validateAzureImage` itself and failed there instead, with `"Azure image
  identity or region unconfirmed"` - the qualification identity's RBAC
  role had never needed `Microsoft.Compute/galleries/*` permissions before
  (it only ever read classic managed images), so the new gallery-version
  GET call failed with an authorization error the generic "identity or
  region unconfirmed" error message doesn't distinguish from a genuine
  identity mismatch. Fixed by adding `Microsoft.Compute/galleries/read`,
  `Microsoft.Compute/galleries/images/read`, and
  `Microsoft.Compute/galleries/images/versions/read` to the live
  `RunnerScout Qualify` role. Both failures happened at or before the
  first cloud-credential-requiring step - zero leftover resources, zero
  cost, in both cases.

Three of these bugs actually resulted in a real, billed resource being
created: the GCP Spot instance that hit the delete-timeout finding above
(existed for well under two minutes, cleanly deleted, real cost a
fraction of a cent); the AWS Elastic IP left briefly unassociated by the
second AWS IAM gap (released within minutes, real cost negligible); and
the AWS Spot instance created by the `BlockDeviceMappings` race, force-
terminated by the safety net moments later (real cost a fraction of a
cent). GCP's own real dispatch (once every fix above landed) completed
the full lifecycle cleanly end to end - `TestQualifyRealGCPSpotLifecycle`
PASS, zero leftover resources - the first fully successful real-cloud
qualification this repository has run. Every other bug's failure
happened before any billable resource was ever created, or left behind
only a resource type that does not bill by itself (a bare
VPC/subnet/internet-gateway/security-group; an Azure NIC/VNet/NSG/subnet).
Each was found, diagnosed against the real cloud APIs (Cloud Logging, in
GCP's case, and Azure's Activity Log, since both adapters' own errors are
deliberately generic; AWS's own CloudTrail for the service-linked-role
gap), and fixed as its own focused PR rather than folded silently into a
larger change - matching this workflow's own one-focused-unit-per-provider
review discipline from when it was first built.

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
independently and risking silent drift between the two. Unlike the AWS
piece, this one found real GCP credentials already provisioned
(`GCP_QUALIFICATION_PROJECT_ID`/`GCP_QUALIFICATION_SERVICE_ACCOUNT`/
`GCP_QUALIFICATION_WORKLOAD_IDENTITY_PROVIDER` repository variables,
confirmed via `gh variable list`) — meaning `qualify-gcp.yml`, unlike
`qualify-aws.yml` at the time it was written, was not inert if dispatched.
It was, like the AWS piece, built, reviewed and committed without ever
being dispatched.

The Azure piece was written 2026-09-14, immediately after AWS's own PR
(#75) merged, following its exact reviewed pattern adapted to Azure's real
API shapes (see "Azure" above for each concrete difference and why). No
Azure secrets or variables existed in the repository at the time
`qualify-azure.yml` was first drafted (confirmed via `gh secret list`/`gh
variable list`); by the time it reached review, `AZURE_QUALIFICATION_CLIENT_ID`/
`AZURE_QUALIFICATION_TENANT_ID`/`AZURE_QUALIFICATION_SUBSCRIPTION_ID` had
been provisioned as repository variables — surfacing the `vars`-vs-`secrets`
bug described above, fixed before this piece was ever pushed, alongside one
missing `Microsoft.Compute/images/read` RBAC action the workflow's own
pre-flight image cross-check needs. `qualify-azure.yml` and
`azure_realcloud_test.go` were built, reviewed and committed without ever
being dispatched or provisioning any real Azure resource.
