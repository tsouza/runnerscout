# Operations

Build with `make build`; inspect `runnerscout -h` before configuring an environment.
The operator requires a dedicated Kubernetes namespace, a unique state name and
scale-set ownership. Never run ARC against that same scale set. Use mounted Secret
files for GitHub credentials and workload identity for cloud provider credentials.

Catalog prices are USD microdollars per compute hour and expire after five minutes.
Refresh from authoritative provider quotes. A partial catalog cannot authorize fallback.
No total-job cost guarantee includes storage, egress or interruption effects.

On local provisioning timeout, inspect state and cloud inventory. Do not cancel a
workflow to mimic a GitHub job failure. Reconcile unknown create operations before
resetting demand. Never remove durable state or ownership records while resources
may exist. A successful delete request is not proof of deletion.

Before live qualification allocate a maximum currency spend, two concurrent VMs,
30-minute campaign deadline, per-VM lifetime cap and cleanup owner. Verify images,
private subnets/firewalls, outbound GitHub reachability and independent cloud inventory.
Retain redacted evidence for each provider separately; stop creation on any ownership
or cleanup failure. Cloud qualification remains pending until this allocation exists.


Authentication supports `-github-app-client-id`, `-github-app-installation-id` and
`-github-app-key-file` together, or `-github-token-file`. The modes are mutually
exclusive. Credentials are mounted files; never put values into command arguments.

Use `catalogPath` to reload a complete JSON price snapshot for new admissions.
Update that file atomically. Each admitted allocation retains its original snapshot
and deadline. Provider quote discovery is not yet automatic. Restore the original
provider/class configuration if durable fleet binding rejects a change; do not delete
state to bypass it. Admission stops at 1,000 retained allocations until archival is
implemented. This experimental runtime is not yet suitable for unattended production.

For local Kubernetes qualification, create a dedicated kind v0.33.0 cluster using
kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5, set `RUNNERSCOUT_TEST_KUBECONFIG` to its explicit kubeconfig,
then run `make integration`. The test creates a unique namespace, exercises real
resourceVersion conflict rejection and waits for namespace deletion. It never uses
the default kubeconfig or skips when the explicit test environment is missing.

AWS configurations require `accountID` as well as their named credential profile
or workload identity. STS caller identity must match before EC2 observations or
effects. A profile rebound to another account produces an explicit error and
retains cleanup obligations.

Azure authentication uses the native Go SDK. Set `AZURE_CLIENT_ID`,
`AZURE_TENANT_ID` and `AZURE_FEDERATED_TOKEN_FILE` for federated workload identity,
or the supported Azure environment credential variables for a service principal.
Otherwise the SDK uses managed identity, optionally selected by `AZURE_CLIENT_ID`.
A failed configured credential does not fall back to developer CLI credentials.
Mount credential files through Secrets or workload-identity admission; never place
secret values in the public configuration. Existing `az login` caches are not used.
