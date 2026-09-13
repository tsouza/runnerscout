# Controller qualification challenges

> **Status: PROPOSED, not yet accepted.** This document is a draft submitted
> for review on [issue #5](https://github.com/tsouza/runnerscout/issues/5).
> Nothing in it is binding until the repository owner accepts, edits, or
> replaces it. In particular, **who has authority to certify a candidate
> against these challenges** — separate from whoever implemented the
> candidate — is not decided here; that is the open half of issue #5.

## Purpose

`make verify` and CI are diagnostic controls: they run on the same commit a
candidate implementation produced, using an evaluator the candidate's own
author can edit. They are necessary but not sufficient evidence of
correctness. CQ-01 through CQ-12 name the specific adversarial behaviors a
qualified controller must resist, independent of any one implementation's
own test suite.

## Challenges

| ID | A qualified controller must never... |
| --- | --- |
| CQ-01 | Misclassify a capacity rejection when the target resource actually exists. |
| CQ-02 | Trigger a duplicate create after a lost or ambiguous create response. |
| CQ-03 | Delete a resource whose ownership tags do not match its own. |
| CQ-04 | Admit a `RunnerClass`/`ProviderConfig`/`CapacityCatalog` change without a fresh compile pass. |
| CQ-05 | Accept a cross-namespace Secret or ConfigMap reference. |
| CQ-06 | Continue using a credential/token after rotation invalidated it. |
| CQ-07 | Re-create a resource for which a partial creation was already proven, after a controller restart. |
| CQ-08 | Orphan or double-own a CRD-owned cloud resource across a Helm upgrade or rollback. |
| CQ-09 | Report a provider interruption without the provider's own definitive signal present. |
| CQ-10 | Over- or under-count admission slots under redelivered or duplicate scale-set messages. |
| CQ-11 | Delete a finalizer-protected object before independently observed drain/cleanup completes. |
| CQ-12 | Present fixture, emulator or real-cloud evidence as interchangeable in an acceptance decision. |

## Provenance

Drafted from adversarial invariants already exercised, at least partially,
by the existing test suite per provider and subsystem — see
[qualification.background.md](qualification.background.md) for which tests
motivated each entry and why the ID ordering follows implementation history
rather than severity.
