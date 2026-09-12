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
