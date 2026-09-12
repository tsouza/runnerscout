# Release pipeline and regression policy

Status: requested under AUTH-RELEASE-PIPELINE-001 and
DEC-RELEASE-REGRESSION-001. Implement and dry-run the pipeline before publishing
any release. RunnerScout has no accepted release baseline yet; BASE-0002 is a
partial discovery baseline and cannot authorize a first release.

## Artifact construction

Build explicit-version linux/amd64 and linux/arm64 runtime images and a Helm chart
package from one immutable source commit. Verify version agreement across the
binary, chart, image labels, manifests and release notes. Record source, builder,
compiler, dependencies and artifact digests. Produce checksums, SBOMs, signed
provenance and verifiable signatures using least-privilege publication credentials.
Reuse the tested artifact for promotion; do not rebuild a different artifact after
checks pass. Candidate artifacts and release artifacts have distinct identities.

## Promotion gates

Publication is fail-closed. Require an explicit complete set of successful checks
for the exact candidate source and artifact digests; reject missing, skipped,
cancelled, stale, partial or inconclusive evidence. A successful workflow wrapper
is insufficient if any required test did not execute or finish.

Required regression coverage includes:

- Unit/race/static checks and the independent placement/lifecycle oracle controls.
- A reproducing test for each fixed bug; retain previous regression cases.
- CRD structural validation, reference boundaries, supported-version compatibility,
  conversion/migration and default behavior; comprehensive example validation.
- Real Kubernetes reconciliation, restart, conflict/fencing and durable cleanup.
- Helm package install/test/upgrade/rollback/uninstall on the supported matrix,
  including preserved runtime state and credentials/RBAC/network boundaries.
- Provider conformance, unknown-effect and cleanup tests, followed by the required
  real GitHub-to-VM and interruption scenarios. Emulators do not satisfy live gates.
- Optional-network defaults, connectivity isolation, DNS/MTU faults and peer cleanup,
  with local-overlay versus real-cloud evidence recorded separately.
- Multi-architecture execution and vulnerability/security acceptance for the exact
  runtime and runner-image artifacts, plus SBOM/provenance/signature verification.

Compare against the last accepted release contracts and supported migration paths.
The first release must establish complete acceptance instead of comparing only with
an unqualified development candidate. Intentional versioned changes require an
explicit reviewed contract/migration update; no automatic golden-result rewriting,
quiet test removal, skip-to-pass conversion or release-only bypass is permitted.
Preserve failures, rejected candidates and the evidence explaining remediation.

## Trust and execution

Untrusted PR jobs cannot publish releases or populate a trusted acceptance decision.
Use pinned workflows/tools and a protected, versioned release policy/evaluator
outside the candidate's unilateral control. Artifact downloads are verified data,
not a reason to execute arbitrary scripts from a candidate artifact. Establish
actual evaluator custody before describing a check as protected; editable local
records alone are not an enforced boundary.

Dry runs exercise artifact assembly, verification and rejected-candidate paths
without creating a release tag or pushing release images/charts. Test deliberate
regressions, missing evidence, digest/version mismatch, stale baseline, skipped
checks and invalid provenance; each must prevent promotion. Require positive
controls proving a fully qualified candidate can be promoted under the same policy.

## Release and rollback

Publish immutable versioned images/charts and release notes linking their digests,
compatibility/migration information and evidence only after all gates pass.
Document upgrades, rollback limits, credential handling and cleanup obligations;
a Helm rollback must not silently discard state or leave cloud resources unowned.
Keep prior accepted artifacts and supported rollback paths available. Never use
mutable tags alone as proof of the artifact that was tested or installed.

The existing unresolved image vulnerabilities, arm64/live-cloud coverage and
protected evaluator custody are real acceptance gaps. Building this pipeline does
not waive them, authorize paid cloud execution or imply a 0.1.0 release is ready.
