# Qualification network provisioning: AWS, Azure and GCP — background

## Why this exists

Neither `qualify.yml` (issue #3's real-cloud provider qualification) nor
the E2E harness (issue #80) has ever created the network they launch
qualification/E2E instances into - both treat it as a pre-existing, pinned,
operator-supplied identifier, deliberately, so that "pinned images/networks"
(issue #3's own wording) stays a hard property rather than something a
workflow could silently discover or create wrong. That leaves the question
of how the network actually gets created in the first place, once, before
either of those workflows can be dispatched at all against a fresh cloud
account. Earlier in the session this repository's qualification identities
were provisioned, but none of the three cloud accounts had this prerequisite
network yet - and standing it up by hand ran into three concrete problems:

- No local AWS credentials were configured in the session's environment at
  all - `aws sts get-caller-identity` failed with `NoCredentials`.
- Azure/GCP CLI *network-creation* calls, attempted interactively from the
  session, were flagged and blocked by the local environment's own safety
  classifier ("Real-World Transactions"/"Modify Shared Resources") - the
  same category of block that also applies to genuinely risky actions, so
  it was respected rather than routed around.
- Manual CLI provisioning, even where it would have worked, leaves no
  reviewable, re-runnable record of exactly what was created - unlike
  everything else real-money-adjacent in this repository, which is code,
  reviewed via PR, before it ever touches a real account.

The user's own direction, once this was surfaced, was explicit: use
OpenTofu, and run it in CI, not from local CLI access.

## Why OpenTofu instead of raw cloud CLI in a script

`tools/e2e/bring-up*.sh` and the qualify.yml workflows already talk to cloud
APIs directly via `aws`/`az`/`gcloud`, so "why not just write another shell
script that calls the CLI to create the network" is a fair question. The
difference is idempotency and drift detection: those existing scripts are
read-only pre-flight checks (cross-validating a *pinned* identifier) or
one-shot creates matched by an equally one-shot delete in the same test run
(the `*_realcloud_test.go` files' own `t.Cleanup`). Provisioning a
*long-lived-relative-to-a-run* network doesn't fit either shape - it needs
"create if missing, converge if already applied with different inputs,
tear down cleanly on request" semantics, which is exactly what OpenTofu's
state-tracked resource model provides and a hand-rolled idempotent shell
script would have to reimplement badly (checking existence before every
create call, tracking what it created for teardown, etc. - Terraform/
OpenTofu's actual job).

## Why apply-before/destroy-after instead of "apply once, keep forever"

The first design instinct (and the modules' first-drafted READMEs) was
"apply once per cloud account, leave the network running indefinitely" -
matching how the qualification identity's own permissions are scoped
(read-only against a *pre-existing* network) and treating this as a true
one-time bootstrap. This fell apart once AWS's module was built: AWS's
`createAWS` unconditionally sets `AssociatePublicIpAddress: false` on every
launched instance (verified in `internal/provider/aws.go`), so an Internet
Gateway alone cannot give a qualification/E2E instance real outbound
connectivity - only a NAT Gateway can, and a NAT Gateway bills **hourly**
(~$0.045/hr plus data processing) whether or not anything is using it.
"Apply once, keep forever" would have turned a bounded, cheap qualification
run into an unbounded ~$32-45/month standing bill the user never agreed to
- a materially different kind of commitment than the "a few dollars" the
user had authorized for real-cloud qualification runs themselves.

Surfacing this tradeoff directly to the user produced the actual design:
provision the network immediately before a run and destroy it immediately
after, via OpenTofu's own `apply`/`destroy`, so the NAT Gateway's cost is
bounded to the duration of one run (a few cents) rather than a standing
monthly bill. Azure's and GCP's own resources don't bill hourly - applying
once and leaving them running would have cost nothing extra - but the same
apply-before/destroy-after lifecycle was applied to all three providers
anyway, for consistency (`qualify-network.yml`'s apply/destroy logic is
then identical across the matrix, with no provider-specific lifecycle
branching) and because "pinned" here was reinterpreted to mean "fixed for
the duration of one run," not "created once and never rebuilt" - a
narrower, cheaper reading of the same qualification philosophy, not a
departure from it.

## Why cross-run artifact pinning instead of a remote state backend or cache

Once `apply` and `destroy` became separate, deliberate operator actions
(rather than one script doing both), OpenTofu's local state file - by
design, one process's ephemeral working file, gitignored, never a remote
backend (see each module's own README "State" section) - needed some way to
survive from the CI job that ran `apply` to a *later*, separate CI job that
runs `destroy`. Three options were considered:

- **A real remote backend** (an S3 bucket + DynamoDB lock table, an Azure
  Storage Account container, a GCS bucket) - rejected as disproportionate:
  provisioning and securing a whole extra piece of durable cloud
  infrastructure, in each of three clouds, just to persist a few KB of
  state for a rarely-run bootstrap module, would have been more
  infrastructure than the thing it manages.
- **GitHub Actions cache** (`actions/cache`), keyed by provider name so a
  later run could restore it - rejected because cache keys are effectively
  write-once (re-saving under an existing key conflicts), so a second
  `apply` of the same provider would need extra cache-eviction logic just
  to update it, and cache entries are also weeks-eviction-bounded in a way
  that doesn't match "keep this until an explicit destroy," a state a
  human might legitimately want to sit for a while before tearing down.
- **Run-scoped artifact + explicit run ID pin** (what was built): `apply`
  uploads its resulting `terraform.tfstate` as a normal workflow artifact;
  `destroy` requires the operator to supply that specific `apply` run's ID
  (`apply_run_id`) and downloads the artifact from exactly that run via
  `actions/download-artifact`'s cross-run `run-id` input. This needed no
  new cloud infrastructure, no cache-eviction handling, and - matching this
  repository's "nothing auto-discovered, everything pinned" philosophy
  (`docs/qualification-real-cloud.md`) applied to state selection, not just
  cloud identifiers - makes "which network's state am I about to destroy"
  an explicit, operator-supplied value rather than "whatever a
  `latest`-tagged lookup happens to resolve to," which could silently
  destroy the wrong network's resources if two applies of the same
  provider ever overlapped.

## Why a separate `NETWORK_PROVISIONER_*` identity, not the qualification identity

`qualify.yml`'s own AWS/Azure/GCP identities were deliberately built this
session as least-privilege: launch/observe/delete one VM into a
*pre-existing* network, nothing else. Confirmed directly against the actual
provisioned Azure custom role (`RunnerScout Qualify`): its permissions
include `Microsoft.Network/virtualNetworks/read` and
`.../subnets/join/action`, but no `.../virtualNetworks/write` of any kind -
it cannot create or modify a VNet even if asked to. Widening that identity
to also create/delete networks - the tempting shortcut, since the OIDC
trust relationship already exists - would have permanently grown a
purpose-built least-privilege identity's blast radius for what is a rare,
bounded bootstrap need, unrelated to what that identity exists to do day to
day. A deliberately separate `NETWORK_PROVISIONER_*` identity per cloud,
scoped to exactly VPC/VNet/subnet/NSG/security-group/firewall create+delete
and nothing else, keeps both identities' permissions honest about what they
actually need - the same reasoning that produced the qualification
identities' own narrow scopes in the first place, applied consistently
rather than abandoned the first time it was inconvenient.

## What remains open

Provisioning the actual `NETWORK_PROVISIONER_*` cloud identities themselves
(the IAM role/OIDC trust for AWS, the app registration/federated credential
for Azure, the service account/Workload Identity Pool for GCP) is real
cloud setup work, not yet done as of this document's writing - see issue
#89 for status. `qualify-network.yml` is built to fail cleanly at its own
credential-presence check until that happens, the same "inert until
provisioned" pattern `qualify.yml`'s own AWS OIDC path used before its
identity existed.
