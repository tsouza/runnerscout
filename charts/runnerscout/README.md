# RunnerScout Helm chart

Development chart `0.1.0-dev.1`; supply an explicitly built runtime image. The
current target is Kubernetes 1.37 and Helm 3.22. The runtime includes the AWS/GCP
CLIs and native Azure SDK. Live GitHub-to-VM and release qualification remain open.

## Configuration

Choose one mode per release:

| Mode | Values | Authentication |
| --- | --- | --- |
| Mounted JSON | `config`, `github` | Existing mounted GitHub Secret and provider identities/files |
| Named CRDs | `crd.scaleSetName`, `crd.secretNames` | Same-namespace Secret references in the CRDs |

The [multicloud example](../../examples/multicloud/README.md) covers all five CRDs,
AWS/Azure/GCP, private subnets and an ordinary multistage workflow. For mounted
mode, `tests/values.json` is a structural fixture with dummy accounts and no capacity.
The chart does not create a GitHub scale set; use a dedicated existing one.

CRD mode packages schemas under `crds/`. Helm installs missing schemas and retains
them on uninstall; Helm does not upgrade them. Apply the reviewed schema files
before upgrading a CRD installation. Mounted-only installations can use
`--skip-crds` if cluster-scoped schema installation is not needed.

```sh
helm upgrade --install runnerscout charts/runnerscout --namespace runnerscout \
  --create-namespace --values my-values.yaml --wait --timeout 5m
helm test runnerscout --namespace runnerscout --timeout 2m
```

Create referenced CRDs and Secrets first. A suspended CRD intentionally reports
unready; omit `--wait` for its initial installation as shown in the example.
`helm test` uses the actual runtime parser: mounted mode validates offline, while
CRD mode reads its configuration/Secret snapshot through Kubernetes. Neither
checks live cloud authentication or provisions a VM.

## Credentials and isolation

Secret values do not belong in Helm values. `credentialSecrets` mounts provider
files at `/etc/runnerscout/providers/<Secret name>/`. File-valued CRD references
contain those paths, not credential contents. Workload identity can use
`serviceAccount.annotations`, `podLabels` and `env`; the Azure workload-identity
pod label is supported without allowing replacement of controller selector labels.

CRD Secret access is restricted to `get` on `crd.secretNames`; list/watch and
unreferenced Secrets are excluded. Use a dedicated namespace for each isolation
boundary. The controller runs as non-root with a read-only root filesystem,
bounded temporary storage, one replica and Recreate replacement. `/healthz` is
liveness; `/readyz` reflects leader/session/reconciliation readiness.

Optional NetworkPolicy denies traffic until suitable rules are supplied. Allow
cluster DNS, Kubernetes API, GitHub and provider endpoints for your CNI. Broad
HTTPS egress is not a hostname allowlist. CRD credentials/configuration reload
without a pod rollout; mounted GitHub credentials require a restart after rotation.

## Upgrade, rollback and removal

Keep configuration mode and scale-set state name stable across upgrades, even
when renaming the Deployment. The chart rejects changes that would strand the old
controller's ownership. CRDs are externally managed: rolling back the chart does
not rewind their settings or replace durable allocation state.

For CRD mode, delete the RunnerScaleSet and wait for its cleanup finalizer while
the controller and cloud credentials remain available. Then uninstall Helm. A
read-only pre-delete hook blocks uninstall while the root or unresolved durable
allocations remain. It does not delete CRDs, credentials or cloud resources.
Mounted mode still requires the operator to stop admissions and verify VM/disk/NIC
cleanup before uninstall. Durable state ConfigMaps are retained in both modes.

When managing RBAC/ServiceAccounts externally, also provide `<fullname>-guard`
with read-only access to the named RunnerScaleSet and namespaced ConfigMaps.
Successful Helm test pods are removed; set `tests.retainPod: true` temporarily for
`helm test --logs`. The next test replaces the retained pod.

`make chart` checks both modes, strict values, RBAC and packaged schema parity.
`make helm-integration` exercises the packaged chart against isolated Kubernetes
and an idle HTTPS GitHub fixture. Fixtures do not qualify live runner execution.
