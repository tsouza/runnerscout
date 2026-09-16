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
and deadline. Setting `awsPriceRefresh`/`azurePriceRefresh`/`gcpPriceRefresh` in
the config file (or, in CRD mode, `priceRefresh` on the `CapacityCatalog`) opts
into live Spot price observation immediately before each admission cycle,
refreshing every matching offering's price and freshness from the real
provider API: AWS EC2 and Azure Retail Prices for every matching Spot
offering, and GCP's Cloud Billing Catalog API only for an offering that also
carries its own hand-pinned `gcpSkuRefs` (see `docs/prices-gcp.md`) - a GCP
offering with no `gcpSkuRefs` stays on its static catalog price regardless.
None of this discovers new pools - adding an offering to the catalog remains
a manual, file-based (or CRD-applied) change either way.
Restore the original provider/class configuration if durable fleet binding
rejects a change; do not delete state to bypass it. Admission stops at 1,000
retained allocations - no automatic archival exists yet; plan restarts or
reduce retention pressure well before a long-running unattended deployment
approaches that ceiling.

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
checks do not provide atomic tagging. VM, disk and NIC service identities are
checkpointed across restarts, including proven identities from partial creation
recovery. Changed generations, conflicting ownership and untracked VM attachments
block deletion. Preflight and deployment are separate requests: keep the resource group
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
`runnerscout -scale-set=build -namespace=runnerscout`. Install the six schemas
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

Azure dependency records include a `uid` alongside each resource path. Older
binaries that cannot read this field must not be used to resume those allocations.
Never strip a generation ID to force rollback or adopt a replacement resource.

A `NetworkProfile` with `spec.mode: wireguard` requires exactly one entry in
`spec.mappings`; more than one is rejected with "wireguard networking supports
exactly one network mapping". Each mapping's `providerRef`, `region`,
`networkID`, `subnetID` and `cidrs` follow the same requirements `separate`
mode already imposes on them (see `api/v1alpha1/types.go`'s `NetworkMapping`
type and `examples/multicloud/network.yaml`/README.md for a worked `separate`
example). In `wireguard` mode, that same mapping's `cidrs` also double as the
overlay address pool every allocation on this `NetworkProfile` draws its
WireGuard tunnel address from — see
[networking-peer-model.md](networking-peer-model.md)'s "5. Overlay address
allocation" section for exactly how an address is picked, how collisions
with other active allocations are avoided, and what happens when the pool is
full. `spec.allowedServices` has no meaning in `wireguard` mode and is
rejected if set. The optional `spec.enrollmentRef` names a Secret holding an
operator-supplied WireGuard pre-shared key; it is validated like every other
Secret reference in this codebase and is defense-in-depth only, never a trust
root — see [networking-peer-model.md](networking-peer-model.md)'s "Secret
shape" section for what it actually protects. Leaving it unset is fully
supported and is the default.

A wireguard-mode runner VM image must include the
`runnerscout-wireguard-agent` binary, built from
`cmd/runnerscout-wireguard-agent`. It is a separate binary from the
`runnerscout` controller, never imported by it, so that its netstack
(gVisor) dependency tree never reaches the controller binary's own
dependency graph or size. `examples/wireguard-agent/` now provides a
reference systemd unit and README for building this binary into a runner
image and starting it at boot from the cloud-init-delivered payload at
`/run/runnerscout/wireguard.json` — see that directory instead of building
this integration from scratch. It is example material to copy and adapt,
not something this repository installs automatically. The binary also
accepts `-poll-interval`, which defaults to 30 seconds.

The controller needs no additional configuration to serve wireguard mode's
existing configuration surface (see "Known limitations" below for what still
does not work). The WireGuard peer-poll endpoint and the `NetworkPeers` hook
it depends on are wired unconditionally at startup, for both the CRD-driven
and mounted-config entry points; no new Helm chart value, flag, environment
variable or Secret name exists for this mode. It costs nothing extra when
no `NetworkProfile` uses `wireguard` mode.

`internal/operator.HandleDesiredRunnerCount` assigns every new wireguard-mode
allocation a `lifecycle.Allocation.WireGuardOverlayAddress` from its
`NetworkProfile`'s CIDR pool at the same point it sets `NetworkProfile`
itself, so a real wireguard-mode allocation's cloud-init payload now carries
a real, unique overlay address — `runnerscout-wireguard-agent`'s
`LoadPayload` no longer refuses to load it for that reason. Combined with
`examples/wireguard-agent/`'s reference boot integration, wireguard mode is
now functionally usable end-to-end.

Known limitations: a wireguard-mode `NetworkProfile` supports exactly one
`NetworkMapping` — a single directly-routable subnet. Cross-region or
cross-provider WireGuard peering is not supported. On GCP, capturing a VM's
WireGuard endpoint costs one additional API call per VM creation when
wireguard mode is used; AWS and Azure capture it from data their create
paths already fetch, at no extra cost. The VM-side peer-poll endpoint has
no rate limiting: it is read-only, costs one Load per request (and, only
after a successful auth, one List), and its response size is bounded by
the real number of allocations in one `NetworkProfile`, never by anything
an unauthenticated caller controls; a compromised VM polling far faster
than its intended interval is the accepted residual risk. Changing a
`NetworkMapping`'s `cidrs` after allocations already exist against it is not
reconciled: an already-checkpointed `WireGuardOverlayAddress` is never
re-validated against a changed pool, matching how this codebase already
treats every other `NetworkMapping` field.

A minimal wireguard-mode `NetworkProfile`, with one mapping and no
`enrollmentRef`:

```yaml
apiVersion: runnerscout.io/v1alpha1
kind: NetworkProfile
metadata:
  name: private-wireguard
  namespace: runnerscout
spec:
  mode: wireguard
  mappings:
    - providerRef:
        name: aws
      region: us-east-1
      networkID: vpc-00000000000000000
      subnetID: subnet-00000000000000000
      cidrs: [10.31.0.0/24]
```

A `RunnerClass` references it the same way it references a `separate` mode
profile, via `spec.networkRef`:

```yaml
apiVersion: runnerscout.io/v1alpha1
kind: RunnerClass
metadata:
  name: linux-amd64-wireguard
  namespace: runnerscout
spec:
  resources:
    cpu: 2
    memoryMiB: 4096
    architecture: amd64
    capabilities: [docker]
  placement:
    policy: lowest-price
    regions: [us-east-1]
    maxPriceMicros: 100000
    allowOnDemand: false
  providers:
    - name: aws
  catalogRef:
    name: multicloud
  networkRef:
    name: private-wireguard
  retry:
    enabled: false
    maxRetries: 0
    acknowledgeRepeatedEffects: false
```
