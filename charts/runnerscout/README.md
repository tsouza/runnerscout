# RunnerScout Helm chart

Development chart version `0.1.0-dev.1`; no released runtime image is implied.
The current development target is Kubernetes 1.37 and maintained Helm 3.22. Supply an explicitly built image
containing `/usr/local/bin/runnerscout` and the AWS, Azure and GCP CLIs.
Build the local development image with `make image`; validate it with `make image-test`.
Runtime vulnerability acceptance and arm64 execution remain pending.

Use `tests/values.json` as a structural example only: its dummy accounts and empty
catalog cannot provision runners. Supply your controller configuration, image and
an existing GitHub Secret. App mode requires `appClientID`, `appInstallationID` and
the Secret key named by `privateKeyKey`. PAT mode uses `tokenKey`. Secret values
are never included in Helm values. The chart does not create a GitHub scale set;
`config.scaleSetID` must identify the dedicated scale set owned by this controller.

```sh
make chart
helm upgrade --install runnerscout charts/runnerscout --namespace runnerscout \
  --create-namespace --values my-values.yaml --wait --timeout 5m
helm test runnerscout --namespace runnerscout --timeout 2m
```

`helm test` checks configuration with the actual runtime parser. It does not
contact GitHub, Kubernetes or a cloud provider. End-to-end job qualification is a
separate required release gate. `make helm-integration` passed install/test/upgrade/rollback/uninstall against
a real isolated Kubernetes 1.37 cluster and idle HTTPS GitHub fixture. The packaged-chart run also passed PAT-to-App upgrade and rollback, including the installation-token exchange path. The fixture does not validate the App JWT cryptographically and does not establish live authentication. All 12 lifecycle checks and cleanup passed with Helm 3.22 and the refreshed runtime.

The chart uses one replica and Recreate replacement, namespace-scoped RBAC,
non-root execution, a read-only root filesystem and bounded temporary storage.
Provider credentials may be mounted from `credentialSecrets` under
`/etc/runnerscout/providers/<Secret name>`. Configure the relevant SDK/CLI file
paths through `env` or use workload identity through service-account annotations.
For example use `AWS_SHARED_CREDENTIALS_FILE`, `GOOGLE_APPLICATION_CREDENTIALS`
and a writable `AZURE_CONFIG_DIR` appropriate to the configured identity.

`catalog.existingConfigMap` mounts `catalog.json` without subPath so projected
updates can reach the controller. Update complete fresh catalogs atomically.
The configuration checksum rolls the deployment when Helm configuration changes.
External Secret rotation does not trigger a rollout; restart the deployment after
rotating credentials that the client reads only at startup.

Optional NetworkPolicy defaults to deny all ingress and egress when enabled.
Supply egress rules for cluster DNS, Kubernetes API, GitHub and provider endpoints
according to your CNI. A broad HTTPS rule is not a hostname allowlist.

Before uninstall, stop admission and reconcile every owned VM, disk and NIC.
Helm does not delete runtime-created durable state ConfigMaps or cloud resources.
Retain state until independent inventory confirms cleanup. Do not reuse the
same class/scale set concurrently from another release. Restoring a changed
provider/class binding is necessary for cleanup; deleting state is not recovery.

The deployment probes `/healthz` for liveness and `/readyz` for leader/session/reconciliation readiness. Cloud failures make the pod unready without causing liveness restart loops. See [runtime qualification](../../docs/runtime-image.md).

`config.maxRunners` can be raised or lowered during upgrades and rollback. Lower limits stop additional admission while existing allocations drain normally. Provider identity, requirements and provisioning/lifetime settings remain bound to durable state; restore their original values if a change is rejected.

Successful Helm test pods are deleted by default. For `helm test --logs`, first set `tests.retainPod: true`; the next test replaces the retained pod. Restore the default for automatic cleanup, or remove the retained test pod explicitly when finished debugging.
