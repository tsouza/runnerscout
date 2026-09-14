# Quickstart

This walks through installing RunnerScout and bounding its spend with a
`CapacityBudget` - the highest-value thing to set up first. It assumes an
existing Kubernetes cluster and `kubectl`/`helm` access; see
[operations.md](operations.md) for configuration details and
[examples/multicloud](../examples/multicloud/README.md) for a complete,
real multi-provider deployment.

## 1. Install the CRDs and chart

```sh
kubectl create namespace runnerscout
kubectl apply --server-side -f charts/runnerscout/crds/
helm upgrade --install runnerscout charts/runnerscout --namespace runnerscout
```

The chart installs suspended by default - no admission happens yet.

## 2. Set a daily spend ceiling

Create a `CapacityBudget`. Its spec is one field: the worst-case daily
ceiling, in USD micros (1,000,000 micros = \$1.00):

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

## 3. Reference it from your RunnerScaleSet

Add `budgetRef` to the `RunnerScaleSet` you're already configuring per
[operations.md](operations.md) (provider, class, catalog and scale set
manifests - see the [worked example](../examples/multicloud/README.md) for
a full set to start from):

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
  # github, maxRunners, provisioningSeconds, maxLifetimeSeconds, suspend: ...
```

From this point, the controller bounds admission by both `maxRunners` and
the budget's ceiling: whichever is more restrictive on a given day wins. No
`budgetRef` at all leaves admission unbounded by spend, as before this CRD
existed. See [capacity-budget.md](capacity-budget.md) for exactly what the
ceiling does and does not cover.

## 4. Unsuspend

Once catalog prices, network and provider credentials are in place (see the
worked example's own walkthrough), set `spec.suspend: false` on the
`RunnerScaleSet` and wait for its `Ready` condition.
