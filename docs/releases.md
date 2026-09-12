# Releases

RunnerScout has no accepted release baseline. Version tags use
`vMAJOR.MINOR.PATCH`; pre-1.0 APIs remain experimental. The release pipeline is
tracked in [#17](https://github.com/tsouza/runnerscout/issues/17).

## Required acceptance

Release is blocked while the repository has any open pull request or open issue.
All work must be resolved before closure. The release commit must be the current
`main` head, and its CI must pass; failed, pending, cancelled or missing required
checks block release. Recheck repository state immediately before promotion.
Run `python3 tools/release_preflight.py --candidate "$RUNNERSCOUT_RELEASE_COMMIT"`
with the account-scoped `gh-tsouza` wrapper available. The preflight retains its
read-only repository snapshot under ignored `evidence/` and does not publish or
certify the remaining qualification requirements below.

Before 0.1.0, complete the requested runtime, all five CRDs and worked examples,
Helm lifecycle, optional networking and provider/retry behavior. Complete
project-wide ACPR immediately before cutting the release, against the final
candidate: challenge correctness, DRY, KISS,
code/document/test consistency, reasoning, unjustified deferrals, contradictions
and tests that do not prove their claimed behavior. Fix findings and retain
re-review evidence outside the source tree.

Publication requires successful evidence for the exact source commit and artifact
digests. Missing, skipped, cancelled, stale, failing or inconclusive required
checks block promotion. Required coverage includes:

- Unit, race, static and independent-oracle tests, including bug regressions.
- CRD validation, namespace boundaries, compatibility and complete examples.
- Kubernetes recovery/fencing and Helm install, upgrade, rollback and cleanup.
- Real GitHub-to-VM execution and interruption/cleanup for AWS, Azure and GCP.
- Optional-network isolation, readiness and peer cleanup; local and live results
  remain separate claims.
- amd64/arm64 execution, vulnerability acceptance and supply-chain verification.

Emulators and fixtures do not satisfy live gates. Paid qualification needs a
separately authorized bounded allocation. Compare subsequent releases against
accepted contracts and migration paths. Never rewrite expected results or remove
required tests automatically to accept a regression.

## Artifacts and promotion

Build versioned images, a Helm package, checksums, SBOMs, signatures and provenance
from one source commit. Verify agreement between versions and digests. Promote
the tested artifacts without rebuilding. Release notes describe compatibility,
migration and rollback limits and link verification evidence.

PR jobs have no publication authority. Use a protected, versioned acceptance
policy outside the candidate's unilateral control. Exercise dry runs and negative
controls for regressions, absent evidence, mismatched digests/versions and invalid
provenance before enabling publication. A passing editable workflow alone does
not establish independent acceptance authority.

Source archives contain product code, tests, examples and durable documentation.
Development ledgers, handoffs, plans and evidence dumps belong in ignored local
storage or review/CI artifacts. `make verify` checks source-tree hygiene.

Rollback must preserve durable ownership and unfinished cleanup obligations;
retain prior accepted artifacts and supported migration paths.
