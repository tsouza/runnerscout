# Releases

RunnerScout has no accepted release baseline. Version tags use
`vMAJOR.MINOR.PATCH`; pre-1.0 APIs remain experimental. The release pipeline is
tracked in [#17](https://github.com/tsouza/runnerscout/issues/17).

## Automated pipeline

Pushing a tag matching `v*.*.*` triggers `.github/workflows/release.yml`:

1. **`preflight`** resolves the tagged commit's exact SHA and runs
   `python3 tools/release_preflight.py --candidate <sha> --gh gh` (plain `gh`,
   authenticated with the workflow's own `GITHUB_TOKEN` — `gh-tsouza` is a
   local-machine-only wrapper for interactive human sessions and is not used
   in CI). This blocks on any open pull request or open issue, on the
   candidate not being `main`'s current head, and on any of the 7 named
   checks (`verify`, `vulnerability`, `kubernetes-integration`, `analyze`,
   `cloud-emulators`, `chart`, `runtime-image`) not being `COMPLETED` +
   `SUCCESS` against that exact commit, produced by `github-actions`. Its
   manifest is uploaded as a build artifact whether the gate passes or fails,
   so a blocked release's reasons are inspectable from the Actions UI.
2. **`build`** runs only if `preflight` passes. It invokes
   `.github/workflows/release-build.yml` as a reusable workflow
   (`workflow_call`) against the same resolved SHA, producing the multi-arch
   image OCI archive, Helm chart package, SPDX SBOM and checksums as workflow
   artifacts. `release-build.yml` keeps its standalone `workflow_dispatch`
   trigger for ad hoc manual builds against any commit, but that path only
   ever runs the `build` job, never the `publish` job below.
3. **`publish`**, a job inside `release-build.yml`, runs only when that
   workflow was itself invoked via `workflow_call` — which today only ever
   happens from `release.yml`'s `build` job above, after `preflight` has
   passed. It pushes the multi-arch image just built to
   `ghcr.io/tsouza/runnerscout`, tagged with both the resolved commit SHA and
   the release tag, using the workflow's own `GITHUB_TOKEN` (`packages:
   write` permission) — GHCR accepts this for a public repository with no
   registry secret configured. It then signs the pushed image keylessly with
   `cosign sign` and attests the SBOM keylessly with `cosign attest --type
   spdxjson`, both using the workflow's own GitHub Actions OIDC identity
   (`id-token: write` permission) to obtain a short-lived certificate from
   Sigstore's public Fulcio CA — no signing key is stored anywhere, and the
   signature, certificate and attestation are published to Sigstore's public
   Rekor transparency log. A downstream consumer verifies the image with:

   ```sh
   cosign verify ghcr.io/tsouza/runnerscout@<digest> \
     --certificate-identity-regexp '^https://github\.com/tsouza/runnerscout/\.github/workflows/release-build\.yml@refs/tags/v.*$' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com
   ```

   and the SBOM attestation with `cosign verify-attestation` using the same
   `--certificate-identity-regexp`/`--certificate-oidc-issuer` pair and
   `--type spdxjson`.

Publishing has one required one-time manual step outside this automation:
GHCR creates a package as **private** on its first-ever push, regardless of
the pushing repository's own visibility, and GitHub does not expose an
endpoint the workflow's `GITHUB_TOKEN` can call to change that — a repository
admin must open the new package's settings on GitHub once, after the first
tag release, and change its visibility to public (and confirm it's linked to
this repository) before downstream consumers can pull or verify it without
authentication. Every push after that stays public.

This pipeline still never creates a GitHub Release object (`gh release
create` or the equivalent API) — that remains a distinct, separate decision,
made outside this automation, per issue #17.

## Required acceptance

Release is blocked while the repository has any open pull request or open issue.
All work must be resolved before closure. The release commit must be the current
`main` head, and its CI must pass; failed, pending, cancelled or missing required
checks block release. Recheck repository state immediately before promotion.
The `preflight` job above runs this check automatically for every tag push;
`python3 tools/release_preflight.py --candidate "$RUNNERSCOUT_RELEASE_COMMIT"`
also remains runnable by hand with the account-scoped `gh-tsouza` wrapper. The
preflight retains its read-only repository snapshot under ignored `evidence/`
and does not publish or certify the remaining qualification requirements
below — those stay human/process steps, not something automation performs.

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
