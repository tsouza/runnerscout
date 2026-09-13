# Controller qualification challenges — background

## Why these twelve

Each CQ was drawn from a defect class the existing test suite already
exercises at least partially, on the theory that a challenge with zero
prior evidence is a guess, not a qualification target. "What stops the
candidate from also being the judge" is answered in `qualification.md`'s
Evaluator authority section: the non-bypassable required-CI ruleset on
`main`, not any local script the candidate can edit.

| ID | Motivating evidence (partial, from this repo) |
| --- | --- |
| CQ-01 | `TestAWSSDKDefinitiveCapacityRejectionHasNoReceipt`, `TestAzureCreateDefinitiveCapacityRejectionHasNoReceipt`, `TestGCPSDKDefinitiveCapacityRejectionHasNoReceipt` and their `...CapacityCodeWith{SurvivingVM,ResidualNIC}StaysUnknown` counterparts — each proves a capacity error code is classified only when a live inventory check also confirms nothing was created. |
| CQ-02 | `TestAWSSDKLostCreateResponseRecoversDependencies`, `TestLostCreateResponseRestartAndCleanup` — a lost response must recover the same resource, not create a second one. |
| CQ-03 | `TestAzureForeignDiskCannotBeDeleted`, `TestGCPOwnershipBlocksDeletion` — tag/label mismatch must block deletion outright. |
| CQ-04 | `TestCompileStaleCatalogAllowsRecoveryButNotAdmission` — a stale catalog degrades admission without silently keeping old assumptions. |
| CQ-05 | `TestCompileRejectsBrokenOrCrossNamespaceReferences`, `TestReadRejectsUnknownConfigurationAndNamespaceSpoofing` — cross-namespace references are rejected before any effect. |
| CQ-06 | `TestResolveSecretsRejectsRotationRecreationAndNamespaceSpoofing`, `TestLoadedKeyIgnoresStatusButDetectsIdentityAndSecretRotation` — rotation must be detected and acted on, not just observed. |
| CQ-07 | `TestCreationRecoveryKeepsProvenIdentitiesOnUnknownOutcome`, `TestCreationRecoveryRequiresCheckpointAndNeverCreatesReplacement` — the create-recovery fencing this whole codebase is organized around. |
| CQ-08 | `TestRuntimeReplacementRetainsOldOwnershipWithoutCleanupLoop`, `TestRuntimeFinalizerConflictRetriesWithoutRepeatingCloudCleanup` — Helm-driven object replacement must not double-run or lose cleanup. |
| CQ-09 | `TestAWSObservationConfirmsSpotInterruption` / `TestAWSObservationRequiresSpotOfferingForInterruption` (added alongside this document) — interruption requires the provider's own definitive code, gated by offering type. |
| CQ-10 | `TestRedeliveryAndBoundedRearming`, `TestLoweredLimitDrainsWithoutRearmingOrDroppingAdmissions` — admission accounting under redelivery. |
| CQ-11 | `TestRuntimeDeletionRequiresObservedDrainAndRetainsOtherFinalizers` — finalizer removal gated on observed drain, not requested drain. |
| CQ-12 | `tools/test_evaluate.py`'s `EvidenceTypeIntegrity.test_evaluator_report_never_claims_live_cloud_integration` — statically proves `tools/evaluate.py`'s report can only ever claim `live_cloud_integration=False`, as a single unconditional literal (see `docs/releases.md`'s "Emulators and fixtures do not satisfy live gates"), included so an evaluator checks the *claim*, not just the code path. |

## Why a flat list instead of grouping by provider or by subsystem

Grouping by provider (AWS/Azure/GCP) or by package (`lifecycle`,
`admission`, `configapi`) was considered and rejected: both groupings
would make it easy to certify "AWS is fine" or "admission is fine" in
isolation while missing that a fix in one area silently reintroduced a
defect class from another (e.g., a `lifecycle` change that reintroduces
CQ-02's lost-response duplicate-create risk for a provider whose tests
happen to live in a different package). A flat, defect-class-first list
forces an evaluator to check the same failure mode everywhere it could
recur, not just where it was first found.

## Why numbering follows discovery order, not severity

An earlier draft ranked challenges by estimated blast radius (data loss >
duplicate spend > availability > audit-trail gaps). It was dropped because
severity ranking would need re-litigating every time a new CQ is added
between two existing ones. Numbering by the order each defect class was
first identified in the codebase's history avoids that churn and avoids
smuggling in a severity ranking through the ID scheme itself.

## What this document deliberately does not claim

It does not claim CQ-01 through CQ-12 are exhaustive or that passing all
twelve qualifies a release on its own. The cited tests are the qualifying
evidence for CQ-01 through CQ-12 today, since each already runs, on every
change, in the checked suite described in `qualification.md`'s Evaluator
authority section - re-running them is executing that CQ, not just
motivation for it. A CQ whose citation is later removed or weakened
reverts to unqualified until a replacement lands.

## Why the required-check suite was split into fast (pre-merge) and full
(post-merge)

Originally all seven CI checks were required before a PR could merge,
including a multi-arch `runtime-image` build (~9 minutes), a real `kind`
cluster spin-up (`kubernetes-integration`), and cloud emulator containers
(`cloud-emulators`) - a docs-only two-file PR (#53) paid that same ~10-15
minute bill as a real code change. Raised directly: "it feels to me that
CI checks are getting super heavy, do we need it all like this for any
tiny change?" A first, narrower fix (a `dorny/paths-filter`-gated skip for
diffs containing only `**/*.md`, landed then superseded in #56) addressed
the docs-only case specifically but left every other small PR paying the
full cost. The broader instruction that followed - "make simple checks
and then do full heavy check prior to releasing... change whatever is
needed, even in project rules" - is what this split implements: `verify`
and `vulnerability` gate every merge; the other five checks validate
`main` immediately after each merge instead.

This repo has no distinct release event yet (`release-build.yml` is
`workflow_dispatch`-only, never automated - see `docs/releases.md`), so
"prior to releasing" is approximated today as "immediately after merging
to main," the earliest point after landing where a release could actually
be cut. Wiring the full suite as an explicit pre-release gate is real,
separate scope belonging to issue #17 (build a release pipeline that
blocks regressions and unqualified promotion), not invented here.

The trade-off this accepts: a regression the full suite would have caught
can be live on `main`, however briefly, before the post-merge run flags
it - the old model's absolute "nothing lands without qualifying" guarantee
becomes "nothing lands without being checked shortly after, automatically
and non-bypassably, with the result visible and fixed forward." This was
a deliberate, explicit trade the instruction above accepted, not an
oversight.

## Why a post-merge failure now opens a tracking issue

A later adversarial review of this split pointed out that "visible" in the
trade-off above only meant "not deleted from the Actions tab" - nothing
reopened the PR, blocked the next merge, or notified anyone, so a failed or
cancelled post-merge run could sit unnoticed indefinitely. The repo has no
Slack, email, PagerDuty, or webhook integration configured anywhere, and
adding a dependency on one of those just to close this gap would trade a
small, real problem for a new external dependency with its own credentials
and failure modes. `report-post-merge-failure` closes the gap with what the
repo already has: `gh issue create`/`gh issue comment`, authenticated with
the workflow's own `GITHUB_TOKEN`.

`cancelled` is reported alongside `failure` even though #60 already stops
one push's run from cancelling another's, which was the specific
cancellation this suite had actually suffered. That fix does not touch
every way a run can be cancelled - a human cancelling it directly, a
runner-infrastructure outage, an org-level concurrency limit - and none of
those leave the commit any more validated than an ordinary failure does, so
the report treats them the same.

The same job shape (aggregate `needs.*.result`, hand the failed-job list to
a shared composite action) is duplicated in `codeql.yml` for `analyze`
rather than factored into one job, because GitHub Actions jobs cannot
`needs:` a job in a different workflow file - the same constraint that
already left this repo's `changes` job duplicated across both workflows
instead of unified into a reusable workflow. What is shared is the part
most worth not duplicating: the `gh label create`/`gh issue list`/`gh issue
create`/`gh issue comment` logic itself lives once, in
`.github/actions/report-post-merge-failure`, mirroring the existing
`go-build-cache` composite action's role as this repo's pattern for sharing
step logic across jobs.

Both workflows report to the same `post-merge-failure` label, so a failure
in `ci.yml`'s heavy jobs and a failure in `codeql.yml`'s `analyze` on the
same or a later commit consolidate onto one open issue instead of two -
the stated goal of checking for an existing open issue before creating one.
The corollary is a small, accepted race: if both workflows' reporting jobs
happen to run for the first time within the same few seconds, each could
find no open issue yet and both create one. This is not resolved with a
lock, since the cost of an occasional duplicate issue (a human notices and
closes one as a duplicate) is smaller than the complexity a distributed
lock would add for what is already a best-effort forcing function, not a
correctness-critical one.
