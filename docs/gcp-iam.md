# GCP `ProviderConfig` IAM permissions

A `gcp` `ProviderConfig`'s service account (the identity behind
`GOOGLE_APPLICATION_CREDENTIALS`/`CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE` or
workload identity - see `docs/operations.md`) needs the following permissions,
granted via a custom IAM role bound to it in the target project, for
`gcp-spot` provisioning to succeed:

```
compute.instances.create
compute.instances.delete
compute.instances.get
compute.instances.list
compute.instances.setLabels
compute.instances.setMetadata
compute.disks.create
compute.disks.get
compute.disks.delete
compute.disks.setLabels
compute.zoneOperations.get
compute.zoneOperations.list
compute.subnetworks.use
compute.networks.get
compute.images.useReadOnly
```

`compute.instances.create` alone is not sufficient. `createGCP`'s single
`Instances.Insert` call also:

- attaches a new boot disk inline via `initializeParams`, which GCP's IAM
  check treats as also creating a disk (`compute.disks.create`);
- labels both the instance and that inline boot disk
  (`compute.instances.setLabels`, `compute.disks.setLabels`) - these labels
  (`p.gcpLabels(a)`) are later relied on for ownership checks in
  `gcpInventory`/`ownsGCP`;
- embeds the startup script as instance metadata
  (`compute.instances.setMetadata`).

Each of these needs its own permission on top of `*.create`.

A role missing any one of these permissions makes `Instances.Insert` itself
403, which `createGCP` cannot distinguish from any other ambiguous create
failure: the allocation stays committed to `Creating` (never returns to
`Pending` to retry, and never reaches `LocalProvisioningTimeout`), and
`Tick` surfaces `"create commitment unknown for <id>"` as an error each
cycle - which fails `/readyz` and logs `reconciliation incomplete; durable
obligations retained`, so this is visible, but never names IAM or the
missing permission as the cause. The condition eventually resolves to
`CreateConfirmedAbsent` once a later `Observe` confirms the VM was never
created. The only place the underlying 403 itself appears is GCP's own
Admin Activity audit log:

```
gcloud logging read 'protoPayload.methodName:"compute.instances"' --project=<project>
```

See [gcp-iam.background.md](gcp-iam.background.md) for how this list was
derived and why the failure mode is silent.
