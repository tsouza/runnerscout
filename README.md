<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-dark.svg">
    <img src="docs/assets/logo-light.svg" alt="RunnerScout" width="404">
  </picture>
</p>

<p align="center">
  <em>ephemeral GitHub Actions runners on real cloud spot capacity</em>
</p>

<p align="center">
  <a href="https://github.com/tsouza/runnerscout/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/tsouza/runnerscout/ci.yml?branch=main&style=flat-square&label=CI" alt="CI status"></a>
  <a href="https://github.com/tsouza/runnerscout/releases/latest"><img src="https://img.shields.io/github/v/release/tsouza/runnerscout?include_prereleases&style=flat-square" alt="Latest release"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/tsouza/runnerscout?style=flat-square" alt="Go version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/tsouza/runnerscout?style=flat-square" alt="License"></a>
  <a href="https://deepwiki.com/tsouza/runnerscout"><img src="https://deepwiki.com/badge.svg" alt="Ask DeepWiki"></a>
  <a href="https://artifacthub.io/packages/search?repo=runnerscout"><img src="https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/runnerscout" alt="Artifact Hub"></a>
</p>

---

A Kubernetes controller for ephemeral GitHub Actions runners on standalone cloud
VMs. RunnerScout searches AWS, Azure and GCP spot capacity using portable resource
requirements and ordinary `runs-on` targeting. It can run alongside
[Actions Runner Controller](https://github.com/actions/actions-runner-controller)
with separate scale-set ownership.

Real VM creation, interruption reporting and cleanup have each been
independently qualified against live AWS, Azure and GCP spot capacity - see
[qualification-real-cloud.md](docs/qualification-real-cloud.md)
for what that qualification covers (and its named gaps, including that real
GitHub Actions job execution is a separate, larger piece it does not attempt)
and how to re-run it. The controller supports
mounted configuration or a named RunnerScaleSet CRD. The Helm chart supports both
modes; see the [complete CRD example](examples/multicloud/README.md).

- Hard resource, region and price limits remain enforced during placement.
- On-demand fallback is opt-in and requires definitive spot exhaustion.
- Durable allocation identities preserve recovery and cleanup across restarts.
- GitHub retains job-result authority when local provisioning times out.

## Quickstart

Install the chart and bound spend with a `CapacityBudget` - the highest-value
thing to set up first. This assumes an existing Kubernetes cluster and
`kubectl`/`helm` access.

**1. Install the CRDs and chart** (installs suspended by default - no
admission happens yet):

```sh
kubectl create namespace runnerscout
kubectl apply --server-side -f charts/runnerscout/crds/
helm upgrade --install runnerscout charts/runnerscout --namespace runnerscout
```

**2. Set a daily spend ceiling.** A `CapacityBudget`'s spec is one field -
the worst-case daily ceiling, in USD micros (1,000,000 micros = \$1.00):

```yaml
# budget.yaml
apiVersion: runnerscout.io/v1alpha1
kind: CapacityBudget
metadata:
  name: daily
  namespace: runnerscout
spec:
  dailyBudgetMicros: 5000000 # $5.00/day
```

```sh
kubectl apply -f budget.yaml
```

**3. Reference it from your `RunnerScaleSet`** (alongside the provider,
class and catalog manifests from the [worked example](examples/multicloud/README.md)):

```yaml
apiVersion: runnerscout.io/v1alpha1
kind: RunnerScaleSet
metadata:
  name: build
  namespace: runnerscout
spec:
  runnerClassRef:
    name: linux-amd64
  budgetRef:
    name: daily
  suspend: true # unsuspended in step 4, once the rest below is wired up
  # github, maxRunners, provisioningSeconds, maxLifetimeSeconds: ...
```

From here, the controller bounds admission by both `maxRunners` and the
budget's ceiling - whichever is more restrictive on a given day wins. No
`budgetRef` at all leaves admission unbounded by spend. See
[capacity-budget.md](docs/capacity-budget.md) for exactly what the ceiling
does and does not cover.

**4. Unsuspend.** Once catalog prices, network and provider credentials are
in place (see the worked example's own walkthrough), set `spec.suspend:
false` on the `RunnerScaleSet` and wait for its `Ready` condition.

## Get started

Use the [Helm chart](charts/runnerscout/README.md) for development installation,
and [operations guide](docs/operations.md) for configuration and recovery.
See [architecture](docs/architecture.md) for placement and lifecycle contracts.

To build locally, install the Go version in `go.mod` and Python 3:

```sh
make verify
make build
```

`make emulators` runs isolated AWS/Azure API checks on a Linux Docker host.
Emulators do not execute real cloud VMs or establish live-provider support.
GCP has no emulator tier - see [emulators.md](docs/emulators.md) for what
covers it instead and why.

## Contribute

Read [contributing](CONTRIBUTING.md), [CI](docs/ci.md),
[security](SECURITY.md) and [release requirements](docs/releases.md).

## License

RunnerScout is [MIT licensed](LICENSE).
