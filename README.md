# RunnerScout

RunnerScout is an open-source Kubernetes control plane for standalone ephemeral
GitHub Actions runners across cloud spot capacity. It is a companion to ARC,
with separate scale-set ownership and ordinary `runs-on` workflow targeting.

**Development status:** experimental, no release. Live GitHub-to-VM execution and
multicloud qualification are pending. See [readiness](outputs/readiness.md).

The design preserves portable resource constraints, named provider configurations,
lowest recorded compute-price placement, opt-in on-demand fallback, durable
allocation identities, bounded provisioning and cleanup after uncertain effects.
Spot job retries default to disabled; repeated workflow effects must be acknowledged.

Build and verify with Go 1.26.7 and Python 3:

```sh
make verify
make build
```

Read [the target](outputs/target.md), [operations](docs/operations.md),
[security](SECURITY.md), and [contributing](CONTRIBUTING.md).
No production deployment or live cloud spend is part of repository bootstrap.
