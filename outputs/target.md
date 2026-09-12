# Development target DEV-0001

2026-09-12. Refines partial discovery BASE-0002 under explicit construction authority.
This is an implementation target; full G01–G08 readiness is not inferred.

## Fixed scope and interfaces

Go 1.26.7; GitHub.com with actions/scaleset v0.4.0 (public preview); Kubernetes
1.35 API target; Ubuntu 24.04 Linux amd64/arm64 images maintained by the operator.
AWS EC2 and GCP Compute are the first provider targets. Azure/GHES/Windows are
outside the first implementation matrix, not demonstrated unsupported by nature.
ARC is optional at runtime and must own different scale sets.

Requirements: positive integer vCPU and memory MiB, architecture, optional CPU vendor,
explicit required capabilities and allowed named providers/regions. Unknown required
facts are ineligible. Unsupported policy is rejected. VM compute prices use integer
USD microdollars/hour, valid for at most five minutes; disk/egress/tax are excluded.
No floating-point conversion or currency conversion. Equal prices tie-break by pool ID.
Catalogs are finite operator-declared pool snapshots, not an assertion of all globally
existing SKUs. Every allowed provider must declare enumeration complete; stale, missing,
unauthorized or partial observations cannot justify on-demand fallback.

Provider interface: Create(operation, offering, bootstrap), Observe(operation),
Delete(operation). Durable allocation ID identifies every effect. Create errors other
than explicit capacity rejection are unknown commitment; observe before replacement.
Observations are present, absent or unknown. Delete completion requires observed absence.
Owned provider/account/region identity and immutable operation name scope deletion.
Cloud API invocations use context deadlines; no response body or credentials in logs.

State: pending -> creating -> running -> deleting -> deleted; timeout is a local
condition with continuing cleanup. Persist creating intent before cloud I/O. A restart
in creating observes the operation; it never blindly creates a replacement. Provider
absence alone after an ambiguous create is insufficient to retry: unresolved commitment
stays blocked until operator reconciliation. This trades availability for bounded spend.
Capacity rejection is retryable within the original deadline and create-attempt cap.

Demand is an absolute snapshot; provisioning counts toward the cap. Redelivery must
not add allocations. Admission expires after ten minutes by default, maximum three
create attempts per allocation. An unchanged nonzero demand cohort does not rearm
expired slots. Successful terminal job completion plus observed VM deletion releases that admission slot for subsequent demand. Expired slots remain consumed. A zero-demand observation starts a new cohort only after old resources
are observed absent; explicit operator reset also requires confirmed cleanup.
No local timeout cancels or fails a GitHub workflow. At most ten active allocations
per configured controller; operations serialized with Kubernetes resourceVersion CAS.
Each deployment owns one named class and scale set; multiple deployments may use
distinct names. Cross-class weighted fairness and fleet-global caps remain future work.

Spot exhaustion requires definitive capacity rejections for every eligible spot pool
in the same complete, fresh snapshot. Cooldown/unknown/unvisited is not exhaustion.
Fallback needs explicit opt-in and preserves every hard requirement and price ceiling.
Failed pools are cooled down across the class for five minutes; GitHub still chooses
compatible existing runners, so no job-specific placement guarantee is made.

Job retries: disabled by default. Enabled policy requires confirmed cloud interruption,
terminal failed REST job, unique runner-name/run/attempt correlation, bounded lineage,
and acknowledgment of dependent-job reruns/repeated effects. Never cast scale-set string
jobId to REST job ID. Ambiguous rerun responses require reconciliation, not blind POST.
The live recovery composition remains a release blocker until verified.

## Assurance and dependency plan

M0 repository and target -> M1 placement/lifecycle, independent oracle and fault tests
-> M2 durable Kubernetes + direct scale-set + VM adapters -> M3 live AWS ordinary workflow
-> M4 live GCP and retry composition -> M5 protected evaluator and full qualification.
Consumer coding may proceed before real seam qualification; promotion claims may not.

Local checks: formatting, vet, race tests, independent catalog oracle, registry checks,
bounded evidence reporting. CI: same checks, build and vulnerability scan. Each run
has a 10-minute local/15-minute CI cap; retain every failed attempt. Development campaign:
60 evaluations, six hours cumulative evaluator wall time, no auto-renewal. Three identical
failure families trigger diagnosis; no unattended restart loop. Release requires real
GitHub/Kubernetes/AWS/GCP effects, independent cleanup inventory, positive and negative
controls, evaluator integrity challenges and CQ-01–CQ-12. Editable local reports are
diagnostic evidence, not protected acceptance authority. Maintainer tsouza owns decisions,
operations and evaluation custody. Protected evaluator isolation remains unestablished.

## Implementation limits tracked explicitly

The initial runtime uses namespace-scoped ConfigMaps and one mounted class configuration,
not a CRD API. The provider boundary currently uses external AWS CLI v2 / gcloud
commands with workload credentials. GCP capacity errors remain commitment-unknown;
only exact AWS InsufficientInstanceCapacity is classified as definitive rejection.
Provider quote discovery, complete GCP rejection classification and live rerun execution remain required implementation work. The runtime reloads catalogPath at admission and persists class-wide five-minute cooldowns.
The pure retry guard exists but does not issue REST reruns. Authentication supports mounted GitHub App credentials or a PAT; neither is stored in public configuration.
These gaps prevent claiming the full target implemented or release-ready.

Runtime recovery amendments: pricing validation cannot block startup cleanup. Existing
allocations retain their admitted catalog snapshot; new admissions reload catalogPath
when configured. Immutable class/provider/account bindings are persisted before effects;
configuration drift fails explicitly and requires restoring the original binding for
cleanup. The first runtime caps retained allocation records at 1,000 and stops admission
at that bound; automatic archival is pending. This is an operational limit, not deletion
of unconfirmed cleanup obligations.
