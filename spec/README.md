# TLA+ models

Hand-written TLA+ models of specific parts of runnerscout's own domain
logic, checked with TLC. Run all of them with `make tlc` (downloads
`tla2tools.jar` into `spec/.tools/` on first run; not wired into CI - see
[README.background.md](README.background.md) for why).

These models are not derived from the Go code and are not a substitute for
`make test`. They exist to explore the full space of states and actions a
part of the system could reach - including ones the current code does not
yet handle - and to check specific safety properties against that space
exhaustively rather than by inspection. A clean TLC run proves the *model*
satisfies its stated property; it says nothing about the real
implementation unless someone keeps the model honest by hand as the code
changes.

## Running

```
make tlc
```

Each `.cfg` file pairs one `CONFIG` with one `.tla` module and is checked
against a declared expectation (clean, or a specific named invariant
violation) in `tools/tlc.py`'s own `RUNS` list. `make tlc` exits non-zero if
any run's actual outcome does not match its declared expectation.

To run one pair directly:

```
java -jar spec/.tools/tla2tools.jar -deadlock -config spec/<name>.cfg spec/<module>.tla
```

## Coverage matrix

| Model | Covers | Configs | Result |
|---|---|---|---|
| `RunnerRegistration.tla` | GitHub's runner-registration state vs. `Allocation.phase`, across all four Deleted/TimedOut transitions, plus GC pruning and a reconciliation action | `pending_only` | **Violates** `NoOrphanedRegistration` (reproduces the gap PR #163 alone left open) |
| | | `all_four` | Clean (PR #168) |
| | | `unknown_leak` | **Violates** `NoIrrecoverableOrphan` (an unpatched fifth path, combined with today's age-only #161 pruning, produces a permanently unrecoverable orphan) |
| | | `reconciliation` | Clean (checked-gated pruning + a reconciliation action closes the gap `unknown_leak` found) |
| `TickConcurrency.tla` | `Operator.Tick`'s `o.mu` release contract against its own spawned goroutines | `buggy` | **Violates** `MuReleasedOnlyWhenIdle` (reproduces PR #157's bug) |
| | | `fixed` | Clean (PR #157's fix) |
| `BudgetAdmission.tla` | `budget.go`'s admission-gating arithmetic (`spentToday`, the ceiling check) | `ceiling` | Clean (the gate itself is sound) |
| | | `lockout_witness` | **Violates** `NeverFullyLockedOutWhileIdle` (a real, reachable trade-off: an all-interrupted day can show the ceiling as fully spent while real utilization is zero) |
| `RetryBound.tla` | `retry.go`'s bounded interruption-rerun protocol | (one config) | Clean (`MaxRetries` is respected; at most one rerun outstanding per RunID) |

Every "Violates" row above is a **deliberate** counterexample or witness
config - see each `.tla` file's own header comment for what it demonstrates
and why the violation is expected, not a regression.

## What is not modeled here, and why

- **Admission-ceiling divergence between runnerscout's local `Admitted` and
  GitHub's own claimed-runner count (E17/E18)** - the `RunnerRegistration`
  model's `githubReg` variable already is this divergence; a separate
  counter-based model would not add anything `NoOrphanedRegistration`
  doesn't already check.
- **A price-only eviction policy for D5** (kill a `Running` allocation when
  a cheaper offering appears - the original motivating example for this
  whole exploration) - deliberately not modeled. `lifecycle.Allocation` has
  no `readyAt` timestamp, so "just started" cannot even be expressed yet,
  and checking whether a candidate policy thrashes needs per-allocation
  identity, not the aggregate counts `BudgetAdmission.tla` uses. This
  remains open design work; see [README.background.md](README.background.md)
  for what a next attempt would need.
- **Credential invalidation mid-cycle (E32), network-profile CIDR pool /
  peer-membership churn (E26/E27), leader-election handoff overlap (E25)**
  - considered and set aside; see
  [README.background.md](README.background.md) for why each one is a poor
  fit for a small hand-written model rather than simply skipped.

See [README.background.md](README.background.md) for the full state-space
research this grew out of, and the reasoning behind every scope decision
above.
