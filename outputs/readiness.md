# Development readiness — DEV-0001 revision 2

The experimental controller is implemented. This is not a qualified release.

| Claim | Evidence and limits |
|---|---|
| Specification readiness | G01 scope and authority defined. G02–G08 remain partial: full provider pricing/retry composition and protected controller qualification are open. |
| Local harness | Formatting, vet, build and the previously recorded 27-test baseline passed, including Azure, started-job recovery and AWS account drift. Eight completion-manifest integrity controls passed. CI verifies the exact PR revision; see PR #6. |
| Semantic controls | Three deliberate defects were detected, each after its unchanged positive control passed: unknown spot search treated as exhausted, wrong deadline boundary, delete acknowledgment treated as absence. |
| Kubernetes integration | Passed against a real isolated Kubernetes v1.35.0 kind cluster: durable reload, stale resourceVersion rejection and observed namespace cleanup. The test cluster was removed. |
| Candidate acceptance | Not accepted. Live GitHub-to-VM execution, AWS/Azure/GCP cleanup inventory and interruption retry composition are not demonstrated. |
| Protected evaluator | Not established. Editable local scripts/CI do not provide independent acceptance custody. CQ-01–CQ-12 remain open. |

Implemented: resource-constrained finite-catalog placement; explicit on-demand opt-in;
aggregate admission with persisted pending capacity; original provisioning deadlines;
Kubernetes CAS state and lease leadership; upstream scale-set listener; mounted App/PAT
authentication; AWS/Azure/GCP command adapters; durable create intent; observed cleanup;
class-wide capacity cooldown; new-admission catalog reload; provider-binding checks;
default-disabled retry eligibility guard; diagnostic evidence and CI.

Pending implementation/qualification: provider-driven quote discovery and complete GCP
capacity classifications; actual rerun REST effects and ambiguity reconciliation; image
and network qualification; automatic archival beyond 1,000 retained allocations;
complete protected development-controller policy and qualification. These gaps remain
visible in issues #2–#5 and are not represented as passing contracts.

Infrastructure history: the first local race build encountered a full host filesystem.
Its failure record remains retained. The user authorized cleanup; rebuildable Go caches
and inactive temporary build directories were removed. The subsequent full lane passed.
The first CI security scan found vulnerable dependencies; fixes are subject to a fresh
required scan rather than waiver. No live cloud spending or production deployment occurred.

Next integration prerequisite: explicit isolated AWS/Azure/GCP configuration and GitHub
credential references, pinned images/networks, numeric spend/VM-runtime allocation and
cleanup owner. The requested allocation is pending. Public source and collaboration:
https://github.com/tsouza/runnerscout/pull/6

User scope correction: Azure is mandatory alongside AWS and GCP. Its adapter and
four initial contract tests are implemented; live qualification is not claimed.
The user selected local emulator investigation instead of supplying paid-cloud
allocation. `make emulators` covers the supported API paths; unsupported GCP
Compute and Azure full CLI/ARM composition remain explicit gaps.

Emulator execution: Ministack 1.5.10 passed the actual AWS CLI/provider adapter
lost-create-response, operation reconciliation and cleanup test. Floci Azure 0.12.0
passed REST VM create/read/delete smoke checks. Both emulator containers and their
internal network were removed with no cleanup errors. Images are pinned by digest.
No Docker socket was mounted and no real cloud credentials were supplied. The initial
network-harness failure was retained before the corrected run. `pass` in that lane
means the two named supported paths; full Azure adapter, GCP Compute and live VM
execution remain unqualified.

Patched dependency scan: zero reachable vulnerabilities reported for Go 1.26.7 and
the updated modules. One advisory exists in a required module without a reachable
call according to govulncheck; this is not a claim that every dependency is advisory-free.

Helm development: chart 0.1.0-dev.1 has nine local render/package/configuration contract tests. The test hook invokes the real controller parser; it does not establish GitHub/VM execution. Real install, upgrade, rollback and uninstall qualification and runtime image packaging remain required by release-0.1.0-plan.md.

Runtime milestone: an amd64 image executed the controller config check and AWS/Azure/GCP CLIs under non-root, read-only, network-disabled restrictions. Two health tests add session rejection and shutdown coverage (31 total test executions including subtests). Initial Trivy image scanning found unresolved vulnerabilities; supported updates are being verified. No image security acceptance or real Helm lifecycle acceptance is claimed.
