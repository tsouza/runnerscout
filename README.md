# RunnerScout

RunnerScout is an open-source Kubernetes control plane for standalone ephemeral
GitHub Actions runners across cloud spot capacity. It is a companion to ARC,
with separate scale-set ownership and ordinary `runs-on` workflow targeting.
The provider target includes **AWS, Azure and GCP**; live qualification remains pending for all three.

**Development status:** experimental, no release. Live GitHub-to-VM execution and
multicloud qualification are pending. See [readiness](outputs/readiness.md).

The design preserves portable resource constraints, named provider configurations,
lowest recorded compute-price placement, opt-in on-demand fallback, durable
allocation identities, bounded provisioning and cleanup after uncertain effects.
Spot job retries default to disabled; repeated workflow effects must be acknowledged.

Build and verify with Go 1.27.1 and Python 3:

```sh
make verify
make build
```

Read [the target](outputs/target.md), [operations](docs/operations.md),
[security](SECURITY.md), and [contributing](CONTRIBUTING.md).
No production deployment or live cloud spend is part of repository bootstrap.

Run `make emulators` on a Linux Docker host for isolated Ministack AWS API tests
and a Floci Azure VM-control-plane smoke test. This lane uses dummy credentials
and an internal Docker network. It does not execute real cloud VMs. Floci GCP
currently lacks standalone Compute Engine coverage; GCP uses explicit fixtures.

The [0.1.0 completion plan](outputs/release-0.1.0-plan.md) tracks the full release scope. The [development Helm chart](charts/runnerscout/README.md) is verified with `make chart`; the packaged-chart lifecycle passed on isolated Kubernetes with an idle GitHub fixture. Live end-to-end release qualification remains pending.
