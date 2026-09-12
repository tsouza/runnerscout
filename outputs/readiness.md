# Development readiness — DEV-0001 revision 3

Experimental development candidate; no qualified release. BASE-0002 remains a
partial discovery baseline. Full development and checked PR merges are authorized;
production deployment and paid cloud execution remain outside the current allocation.

| Claim | Current evidence and limits |
|---|---|
| Specification | DEV-0001 defines scope and preserved behavioral decisions. G02–G08 and CQ-01–CQ-12 remain partial. |
| Local controller/harness | Go 1.27.1 formatting, vet, build and 33 test executions passed with no missing, failed or skipped tests. Eight completion-manifest controls passed. Diagnostic evaluator custody is not independently protected. |
| Chart contracts | Ten render/package/parser/RBAC/Secret/probe/configuration tests passed with Helm 3.22.0 and the Kubernetes 1.37 target. |
| Real Helm lifecycle | Packaged chart on isolated Kubernetes 1.37 passed 12 checks: install, tests, RBAC, App/PAT upgrade, rollback, session reconnection, retained fleet state and namespace cleanup. Cluster/network cleanup had zero errors. |
| Runtime | Updated amd64 image passed non-root, read-only, network-disabled controller and AWS/Azure/GCP CLI smoke tests. Arm64 remains unqualified. |
| Emulators | Updated AWS CLI passed Ministack adapter create-response-loss/reconciliation/cleanup; Floci Azure REST smoke passed. Both environments were removed. Full Azure ARM adapter and GCP Compute are not emulator-qualified. |
| Go vulnerability check | govulncheck 1.8.0 completed with no vulnerabilities found. The preceding 1.1.4 parser crash on Go 1.27 is retained as failed tooling evidence. |
| Image security | Trivy 0.74.0: zero critical, 48 high findings remain. Image security is not accepted; official Azure constraints and OS/bundled findings remain tracked. |
| Live end-to-end | Real GitHub-to-VM execution, multicloud image/network behavior, independent resource inventory and interruption retries remain unqualified. |

The Helm test uses an idle HTTPS GitHub fixture. App key parsing and installation-
token exchange are exercised, but the fixture does not validate JWT signatures and
no real GitHub job or cloud VM executes. These claims are deliberately separate
from live integration. Exact image, chart, fixture and evaluator identities appear
in local-evidence.json; CI checks the published revision independently.

Implemented: finite-catalog resource-constrained placement; explicit on-demand
opt-in; bounded aggregate admission; durable creation intent and original deadlines;
Kubernetes ConfigMap CAS and Lease leadership; direct scale-set listener; mounted
App/PAT authentication; AWS/Azure/GCP adapters; observed cleanup; class cooldown;
catalog reload; immutable provider identity and mutable admission-limit binding;
default-disabled retry eligibility guard; Helm deployment and local diagnostics.

Pending: complete provider quote discovery/rejection classification; actual bounded
rerun composition and ambiguity reconciliation; allocation archival beyond 1,000;
CRD API and comprehensive examples; optional uniform private networking; architecture
and image security acceptance; independent evaluator qualification and live cloud
end-to-end evidence. The latest user requests are recorded in records.json and
release-0.1.0-plan.md. ConfigMap-only configuration does not satisfy the CRD request.

Dependency freshness and compatible-version exceptions are in dependency-status.md.
Go module/build caching is explicit in all seven Go CI/CodeQL jobs; observed logs
already demonstrate reuse from main. The new runtime Buildx cache awaits published
CI evidence. Development PRs merge only after the existing required checks pass.

No paid cloud or networking allocation exists. Continue independent implementation
and local qualification. Any later cloud test needs explicit isolated provider and
GitHub configuration references, pinned images/networks, numeric spending/runtime
limits and a cleanup owner. No release tag has been created.

CRD development foundation: ProviderConfig, RunnerClass, RunnerScaleSet, CapacityCatalog and NetworkProfile Go types and generated v1alpha1 schemas now exist on the development branch. The reader/compiler uses namespaced references, stable UID/resourceVersion snapshots, derived allocation ownership and external Secret references. Sixty local test executions passed; a real isolated Kubernetes 1.37 API server accepted all five CRD schemas and valid resources and rejected invalid limits/unknown fields. Snapshot and cleanup checks passed. This does not yet connect CRDs to the running controller, Helm CRD installation or complete worked examples. Enabled retries and shared-overlay execution remain explicitly unsupported until implemented.
