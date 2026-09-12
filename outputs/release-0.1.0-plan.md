# RunnerScout 0.1.0 completion plan

The user requests a complete tested end-to-end 0.1.0, including a complete tested Helm chart. This scope is active; an experimental controller or passing emulator lane alone cannot satisfy it. Construction and repository development are authorized; paid cloud execution and production deployment remain excluded without further allocation.

| Required result | Evidence required | Current state |
| --- | --- | --- |
| Preserved specification and behavioral contracts | G01–G08 item-by-item review, traceable decisions and examples | Partial DEV-0001; BASE-0002 remains partial |
| Complete AWS, Azure and GCP behavior | Provider discovery, price freshness, rejection semantics, durable unknown-effect recovery and independent cleanup inventory | Adapters implemented; quote discovery and classification incomplete |
| Ordinary GitHub workflow end-to-end | Real scale-set job assignment, VM bootstrap, terminal results and cleanup, each provider | Not demonstrated |
| Bounded interruption retries | Opt-in disabled by default, unique REST identity, rerun lineage, duplicate/ambiguous response recovery, dependent-job acknowledgment | Eligibility guard only; composition incomplete |
| Installable runtime | Reproducible container with supported provider CLIs, non-root execution, architecture coverage and scan evidence | amd64 local image and restricted smoke tests implemented; architecture and vulnerability acceptance pending |
| Full Helm chart | Schema/render/config tests; real install, helm test, upgrade, rollback, uninstall and retained-state checks; RBAC, Secret and workload-identity configurations | Nine render/package/config contract tests and probes implemented; real lifecycle qualification pending |
| Controller assurance | Fault/timeout/restart tests, oracle controls, independently protected evaluator and CQ-01–CQ-12 | Local diagnostic controls only |
| Qualified release | Exact-source passing required CI, complete above evidence, versioned chart/image/artifacts and release notes | Not accepted; no release tag |

Implementation order: finish foundation PR #6; construct and qualify chart/runtime deployment; complete provider quote/rejection and retry composition; expand local end-to-end seams; obtain any indispensable bounded external qualification allocation; audit all requirements before publishing 0.1.0. Tests must distinguish fixture, emulator, real Kubernetes and real cloud/GitHub evidence. Missing external evidence does not prevent independent implementation work.
