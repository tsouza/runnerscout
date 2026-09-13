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
(`main: pull requests and verified checks`, verified 2026-09-13, its
check suite split 2026-09-13 — see below) forbidding force-push and
deletion, with `current_user_can_bypass: never` — including for the
repository owner's own account.

That Ruleset's `required_status_checks` gate a merge on two fast checks
only, `verify` and `vulnerability` (each well under two minutes): every
PR gets fast feedback regardless of size, and a small or docs-only change
is never charged the cost of a full cloud-adjacent suite just to land.
The other five checks — `kubernetes-integration`, `analyze` (CodeQL),
`cloud-emulators`, `chart`, `runtime-image` — are not a merge gate; they
run automatically on every push to `main`, immediately after a merge, so
`main` stays continuously and non-bypassably validated. Nobody can skip
these running on `main`, but a merge is no longer blocked on them first:
a regression they catch is fixed forward on `main` within roughly the run
time that check used to add to every PR, rather than never having landed.
This is a deliberate trade against the previous all-seven-before-merge
model — see
[qualification.background.md](qualification.background.md) for why.

CQ-01 through CQ-12 are qualified by an adversarial test for each,
covered by this checked suite (see the Provenance table below for the
tests that already cover most of them) — non-bypassable in the sense that
running them on `main` cannot be skipped or waived by editing a local
script, not in the sense of blocking every PR's merge on them.

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
