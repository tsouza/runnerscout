# Controller qualification challenges — background

## Why these twelve

Each CQ was drawn from a defect class the existing test suite already
exercises at least partially, on the theory that a challenge with zero
prior evidence is a guess, not a qualification target. None of this
constitutes the "protected evaluator" issue #5 also asks for — it only
answers "what should such an evaluator check," not "who runs it" or
"what stops the candidate from also being the judge."

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
| CQ-12 | No single test proves this — it is a documentation/process discipline (see `docs/releases.md`'s "Emulators and fixtures do not satisfy live gates"), included so an evaluator checks the *claim*, not just the code path. |

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
severity is itself a judgment call this document should not make
unilaterally — "protected evaluator authority," the unresolved half of
issue #5, is precisely the question of who gets to rank consequences.
Numbering by the order each defect class was first identified in the
codebase's history avoids smuggling in a severity ranking through the ID
scheme itself.

## What this document deliberately does not claim

It does not claim CQ-01 through CQ-12 are exhaustive, that passing all
twelve qualifies a release, or that the cited tests are sufficient
evidence on their own — they are cited as *motivation* for each
challenge's existence, not as proof any candidate already satisfies it.
Whether re-running the cited tests counts as "executing" a CQ, versus
requiring an independently authored adversarial test per challenge, is
also left to the issue #5 discussion.
