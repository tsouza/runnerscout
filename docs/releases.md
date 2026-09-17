# Releases

Version tags use `vMAJOR.MINOR.PATCH`; pre-1.0 APIs remain experimental. A
`v0.x.y` tag is always released as a GitHub prerelease; `v1.0.0` and later
never is.

## Automated pipeline

Releases go through a `chore(release)` pull request, not a directly pushed
tag:

1. A human dispatches `.github/workflows/prepare-release.yml`
   (`workflow_dispatch`, choosing `patch`/`minor`/`major`). It runs
   `python3 tools/prepare_release.py --bump <level>`, which computes the
   next `vMAJOR.MINOR.PATCH` from the latest matching tag, bumps
   `charts/runnerscout/Chart.yaml`'s `version`/`appVersion` fields, and
   renders a new section into `CHANGELOG.md` from commit subjects since
   that tag (bucketed by their `type:`/`type(scope):` prefix - `fix`,
   `feat`, `docs`, `chore`, etc. - into Added/Fixed/Changed/Documentation/
   Chores/Other; anything unrecognized lands in Other rather than being
   dropped). The workflow then commits those two files to a `release/vX.Y.Z`
   branch and opens `chore(release): vX.Y.Z` as a normal pull request -
   nothing is tagged, built, or published yet.

   Pushing that branch and opening the PR uses an optional `RELEASE_PAT`
   repository secret (a fine-grained personal access token scoped to this
   repo with `contents: write` + `pull-requests: write`) instead of the
   workflow's own default `GITHUB_TOKEN`, falling back to `GITHUB_TOKEN` if
   the secret is not configured. This matters because GitHub's own
   recursion guard does not let an event authored by the default
   `GITHUB_TOKEN` trigger other workflows' `push:`/`pull_request:`
   triggers - without `RELEASE_PAT`, `ci.yml` would never run on the
   opened PR, and this repository's own branch protection (requiring the
   `verify`/`vulnerability` checks before merge) would leave it permanently
   unmergeable. Without `RELEASE_PAT` configured, `prepare-release.yml`
   still opens the PR (with a `::warning::` annotation on the run) but its
   CI needs a manual nudge - closing and reopening the PR, or pushing an
   empty commit to it - before it can be merged.
2. A human reviews and merges that PR like any other. **Merging it is what
   makes a release happen** - there is no separate "now actually release"
   step.
3. `.github/workflows/release.yml` triggers on every push to `main` and
   runs three jobs:
   - **`gate`** runs `python3 tools/release_gate.py`, which reads
     `charts/runnerscout/Chart.yaml`'s `appVersion` at this commit and
     checks whether a matching `vMAJOR.MINOR.PATCH` tag already exists. An
     ordinary merge to `main` (not a release PR) leaves `appVersion`
     unchanged, so the tag already exists and `gate` outputs `publish=false`
     - the rest of the workflow is skipped entirely, at essentially no CI
     cost. Only a just-merged `chore(release)` PR's own commit has a
     genuinely new `appVersion` with no matching tag yet.
   - **`preflight`** runs only when `gate` says `publish=true`. Unchanged
     from the old pipeline: `python3 tools/release_preflight.py --candidate
     <sha> --gh gh` (plain `gh`, authenticated with the workflow's own
     `GITHUB_TOKEN` - `gh-tsouza` is a local-machine-only wrapper, not used
     in CI). This blocks on any open pull request or open issue, on the
     candidate not being `main`'s current head, and on any of the 7 named
     checks (`verify`, `vulnerability`, `kubernetes-integration`, `analyze`,
     `cloud-emulators`, `chart`, `runtime-image`) not being `COMPLETED` +
     `SUCCESS` against that exact commit, produced by `github-actions`. Its
     manifest is uploaded as a build artifact whether the gate passes or
     fails, so a blocked release's reasons are inspectable from the Actions
     UI.
   - **`goreleaser`** runs only when both `gate` and `preflight` pass. It
     creates and pushes the `vX.Y.Z` annotated tag at the merge commit
     (idempotent: a tag that already exists at this exact commit - e.g. a
     re-run after a transient failure - is left alone; one that exists
     anywhere else is a hard error, never silently moved), then runs
     `goreleaser release --clean` via `goreleaser/goreleaser-action`. Every
     build/sign/publish/release step lives in `.goreleaser.yml` now, not
     hand-rolled workflow steps:
     - `dockers_v2` builds the multi-arch (`linux/amd64`, `linux/arm64`)
       runtime image directly from the existing multi-stage `Dockerfile`
       (which does its own `go build` - goreleaser has no `builds:`/
       `archives:` entries here, since nothing in this repo consumes a
       standalone binary archive) and pushes it to
       `ghcr.io/tsouza/runnerscout`, tagged with both the version and the
       full commit SHA. `sbom: true` attaches a buildx-native SBOM
       attestation to the image index.
     - `docker_signs` signs the pushed image keylessly with `cosign sign`,
       using the job's own GitHub Actions OIDC identity to obtain a
       short-lived certificate from Sigstore's public Fulcio CA - no signing
       key stored anywhere, signature and certificate published to
       Sigstore's public Rekor transparency log. Verify with:

       ```sh
       cosign verify ghcr.io/tsouza/runnerscout@<digest> \
         --certificate-identity-regexp '^https://github\.com/tsouza/runnerscout/\.github/workflows/release\.yml@refs/heads/main$' \
         --certificate-oidc-issuer https://token.actions.githubusercontent.com
       ```
     - A `before.hooks` step packages the Helm chart
       (`helm package --version/--app-version <version>`) into
       `release-artifacts/chart/` (outside goreleaser's own `dist/`, which
       errors if anything exists there before it creates it itself).
       `checksum.extra_files` and `release.extra_files` both reference that
       chart package, so it is included in `checksums.txt` and attached to
       the GitHub Release goreleaser creates, alongside every checksummed
       artifact.
     - `release` creates the GitHub Release itself (`prerelease: auto`,
       matching this document's own major-version-only prerelease rule) and
       attaches the Helm chart package and `checksums.txt`.

Publishing requires no manual step: `ghcr.io/tsouza/runnerscout`'s first-ever
push (v0.1.0) was public immediately, and the image signature verifies with
an anonymous, unauthenticated pull. If a future GitHub account or
organization default ever creates the package private instead, a repository
admin can open the package's own settings and change its visibility to
public - GitHub does not expose an endpoint the workflow's `GITHUB_TOKEN`
can call to do this itself.

No evidence this repository needs a draft-then-published release flip
(checked: `gh api repos/tsouza/runnerscout/rulesets` shows only a branch
protection ruleset on `main`, nothing targeting releases/tags) - goreleaser
publishes the GitHub Release directly.

The pipeline runs `gate` → `preflight` → `goreleaser` end to end, every
release-worthy commit gated on the same non-bypassable
`tools/release_preflight.py` check, and it remains fully inert for an
ordinary merge to `main` - no tag created, no image pushed, no GitHub
Release opened - until a `chore(release)` PR that actually bumped
`Chart.yaml`'s `appVersion` is the thing that merged.

## Required acceptance

Release is blocked while the repository has any open pull request or open issue.
All work must be resolved before closure. The release commit must be the current
`main` head, and its CI must pass; failed, pending, cancelled or missing required
checks block release. Recheck repository state immediately before promotion.
The `preflight` job above runs this check automatically for every commit
`gate` recognizes as a release (a merged `chore(release)` PR);
`python3 tools/release_preflight.py --candidate "$RUNNERSCOUT_RELEASE_COMMIT"`
also remains runnable by hand with the account-scoped `gh-tsouza` wrapper. The
preflight retains its read-only repository snapshot under ignored `evidence/`
and does not publish or certify the remaining qualification requirements
below — those stay human/process steps, not something automation performs.

Every release requires the requested runtime, all six CRDs and worked
examples, Helm lifecycle, optional networking and provider/retry behavior
complete and current for the candidate commit. Complete a full project-wide
critical review immediately before cutting the release, against the final
candidate: challenge correctness, DRY, KISS, code/document/test consistency,
reasoning, unjustified deferrals, contradictions and tests that do not prove
their claimed behavior. Fix findings and retain re-review evidence outside
the source tree.

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

The AWS, Azure and GCP pieces of the "Real GitHub-to-VM execution" line above
are the three matrix branches of `.github/workflows/qualify.yml`, a single
`workflow_dispatch`-only, numerically bounded workflow (a `provider` input
selects `all` or one of `aws`/`azure`/`gcp`) that exercises the real
AWS/Azure/GCP provider adapters against real EC2/Azure Compute/Compute
Engine - see [qualification-real-cloud.md](qualification-real-cloud.md). All
three providers' qualification identities (AWS's `AWS_QUALIFICATION_ROLE_ARN`;
Azure's `AZURE_QUALIFICATION_CLIENT_ID`/`AZURE_QUALIFICATION_TENANT_ID`/
`AZURE_QUALIFICATION_SUBSCRIPTION_ID`; GCP's `GCP_QUALIFICATION_PROJECT_ID`/
`GCP_QUALIFICATION_SERVICE_ACCOUNT`/`GCP_QUALIFICATION_WORKLOAD_IDENTITY_PROVIDER`)
are provisioned in this repository today, and the workflow has been
dispatched against real AWS, Azure and GCP resources for each provider, with
a clean pass and independently verified zero leftover billable resources on
each - see [qualification-real-cloud.background.md](qualification-real-cloud.background.md#what-real-dispatches-found)
for what each real dispatch found and fixed along the way. A release
candidate still needs its own qualification run against the exact candidate
commit; a past pass on a different commit does not carry forward.

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
