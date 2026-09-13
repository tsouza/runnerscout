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
retains cleanup obligations. AWS runner images must be available Linux EBS-root
images matching the selected architecture, without Marketplace product codes.
EBS volumes use encryption and delete-on-termination; durable dependency IDs retain
cleanup obligations for residual disks and interfaces. Images must support IMDSv2 in cloud-init.
The VM metadata endpoint requires tokens with a one-hop response limit; IPv6
metadata and instance-tag access are disabled. Runner VMs have no instance profile.

Azure authentication uses the native Go SDK. Set `AZURE_CLIENT_ID`,
`AZURE_TENANT_ID` and `AZURE_FEDERATED_TOKEN_FILE` for federated workload identity,
or the supported Azure environment credential variables for a service principal.
Otherwise the SDK uses managed identity, optionally selected by `AZURE_CLIENT_ID`.
A failed configured credential does not fall back to developer CLI credentials.
Mount credential files through Secrets or workload-identity admission; never place
secret values in the public configuration. Existing `az login` caches are not used.

Azure images must be available, generalized Linux managed images in the selected
region, containing only an OS disk. RunnerScout reads and validates their metadata
before deployment; the provider identity needs permission to read each image.
Data-disk images and specialized images are rejected. The managed-image API does
not expose architecture: maintain the image-to-machine architecture mapping in
the catalog and qualify the image before admitting jobs.

Azure creation requires confirmed absence of the deployment, VM, NIC and disk
names before submitting an ARM template. An occupied name or unknown inventory
stops creation. Creation recovery can finish interrupted OS-disk ownership tagging after a
terminal deployment. It requires matching VM ownership, image and disk attachment
evidence, and verifies service-generated VM/disk identities around the update.
Normal observation remains read-only. Missing evidence or conflicting ownership
retains the allocation for investigation; recovery never redeploys the VM. These
checks do not provide atomic tagging or persist immutable Azure identities across
restarts. Preflight and deployment are separate requests: keep the resource group
dedicated to RunnerScout, exclude competing resource writers and preserve its state.

Each named provider uses its own credential scope. AWS profiles require an
explicit mounted `AWS_CONFIG_FILE` or `AWS_SHARED_CREDENTIALS_FILE`; implicit
home-directory profiles are not used. AWS uses the native Go SDK and rereads
selected credential files per operation.
Static keys, mounted profiles and web identity are supported; credential processes,
developer SSO, interactive MFA and implicit instance-metadata credentials are rejected.
Each operation freezes one credential identity for account verification and EC2 calls.

GCP uses the native Go SDK. Select a mounted credential JSON file with
`GOOGLE_APPLICATION_CREDENTIALS` or `CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE`;
different files in both variables are rejected. An explicit file never falls
back to another identity after failure. Without a file, the SDK uses metadata
workload identity; developer ADC and gcloud login caches are not read. Executable
credential sources are rejected. The running client detects projected credential-file
changes on its next request; invalid or missing updates fail without reusing an old
token. Grant access to instance and disk inventory/creation/deletion
and zonal operation inventory/observation in the configured project.

GCP labels the VM and its boot disk at creation and checks operation commitment
before cleanup. A residual disk retains the allocation until observed absent.
An untagged or foreign disk is never adopted from its name; restore authoritative
ownership evidence or investigate it before retiring the allocation.

Run the CRD controller inside the cluster with
`runnerscout -scale-set=build -namespace=runnerscout`. Install the five schemas
under `config/crd/bases/` and create the referenced namespaced resources first.
This mode reads GitHub and provider credentials from their named Secret
references; mounted configuration and GitHub authentication flags are mutually
exclusive with it. The Helm chart selects this mode with `crd.scaleSetName` and restricts Secret
reads to the names in `crd.secretNames`.

Inspect the RunnerScaleSet `Ready` condition when configuration cannot be loaded.
Suspension or invalid references stop new admissions while existing allocations
remain under reconciliation. Fix invalid references or restore required cloud
credentials; missing GitHub credentials do not prevent cloud recovery or deletion.
Delete the RunnerScaleSet and wait for its cleanup finalizer before stopping its
controller. Preserve its configuration checkpoint and allocation records while
cleanup is unresolved. A missing/corrupt checkpoint or replacement object UID
blocks adoption; it is not permission to recreate or erase ownership records.

Use `-check-crd` with the namespace and scale-set flags for read-only Kubernetes
configuration validation. `-check-uninstall` requires root absence and completed
durable cleanup without reading Secrets or contacting cloud providers. Helm runs
these checks in its CRD test and pre-delete hooks, respectively.

Allocation records containing cloud dependency IDs use the `allocation-v2` data key.
Older development binaries cannot read these records; rollback requires a binary
that understands this format. Never remove dependency IDs or rename the key to
force a downgrade. Restore a compatible controller and retain state until cleanup
is confirmed. Providers that cannot reconcile a recorded dependency refuse work.
