# Controller qualification challenges

Part of [issue #5](https://github.com/tsouza/runnerscout/issues/5).

## Purpose

`make verify` is a local diagnostic control: it runs on the same commit a
candidate implementation produced, using an evaluator the candidate's own
author can edit. It is necessary but not sufficient evidence of
correctness. CQ-01 through CQ-12 name the specific adversarial behaviors a
qualified controller must resist, independent of any one implementation's
own test suite.

## Evaluator authority

Editable local tooling is not evidence on its own; enforcement that a
candidate cannot bypass is. `main` is protected by a GitHub Ruleset
(`main: pull requests and verified checks`, verified 2026-09-13) requiring
every one of the seven CI checks to pass and forbidding force-push and
deletion, with `current_user_can_bypass: never` — including for the
repository owner's own account. That non-bypassable CI run, not any local
`make verify` invocation, is this project's evaluator authority: CQ-01
through CQ-12 are qualified by adding an adversarial test for each to the
required CI suite (see the Provenance table below for the tests that
already cover most of them), so passing them is enforced the same way for
every change regardless of who authored it, and cannot be waived by
editing a local script.

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
