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
evidence for CQ-01 through CQ-12 today, since each already runs in the
required CI suite on every change - re-running them is executing that CQ,
not just motivation for it. A CQ whose citation is later removed or
weakened reverts to unqualified until a replacement lands.
