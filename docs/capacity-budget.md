# CapacityBudget: bounding worst-case daily spend

A `CapacityBudget` is a `runnerscout.io/v1alpha1` CRD that bounds a
`RunnerScaleSet`'s worst-case daily spend, alongside its existing
`maxRunners` concurrency cap. Its spec holds only the ceiling:

```yaml
apiVersion: runnerscout.io/v1alpha1
kind: CapacityBudget
metadata:
  name: daily
  namespace: runnerscout
spec:
  dailyBudgetMicros: 5000000 # $5.00/day
```

A `RunnerScaleSet` opts in with `spec.budgetRef`:

```yaml
apiVersion: runnerscout.io/v1alpha1
kind: RunnerScaleSet
metadata:
  name: build
spec:
  budgetRef:
    name: daily
  # ...
```

No `budgetRef` (the default) leaves admission unbounded by spend, exactly as
before this CRD existed.

## What it bounds

On every admission pass, the controller computes each new allocation's
worst-case reservation - its `RunnerClass`'s `placement.maxPriceMicros`
(price is USD-micros per compute-hour) times the `RunnerScaleSet`'s
`maxLifetimeSeconds`, rounded up to the next micros - and sums the
reservations of every allocation admitted so far on the current UTC day.
A new admission is bounded to whatever fits in the remaining ceiling; when
the ceiling admits nothing at all, the `RunnerScaleSet`'s fleet condition
reads `BudgetExhausted` rather than the ordinary `DemandObserved`.

The reservation bounds *intended* lifetime only. Cleanup that runs longer
than expected (an unconfirmed delete retried past the allocation's
deadline) is not metered against the budget.

## What it does not do

Nothing about spend is ever persisted on the `CapacityBudget` object
itself - not a running total, not a remaining balance. Each referencing
`RunnerScaleSet` recomputes its own spend independently from its own
admitted allocations on every reconcile pass.

A `CapacityBudget` referenced by more than one `RunnerScaleSet` is **not**
a shared pool: each referencing scale set enforces the same ceiling
independently against only its own spend, so two scale sets sharing one
`CapacityBudget` can each independently spend up to the full ceiling, not
split it between them.
