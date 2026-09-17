# TLA+ models

Hand-written TLA+ models of specific parts of runnerscout's own domain
logic, checked with TLC. Run all of them with `make tlc` (downloads
`tla2tools.jar` into `spec/.tools/` on first run; not wired into CI - see
[README.background.md](README.background.md) for why).

These models are not derived from the Go code and are not a substitute for
`make test`. They exist to explore the states and actions a part of the
system could reach - including ones the current code does not yet handle -
and to check specific safety properties against that space, and to report
what TLC actually found: either a clean run over the checked bounds, or a
concrete counterexample.

**Bounded, not universal.** Every "clean" result below holds for exactly
the fixed constants (`AllocIDs`, `MaxAdmitted`, `MaxRetries`, ...) each
`.cfg` supplies, not for all N. Several of these invariants are also
restatements of the action guards that produce them rather than
independent discoveries - each `.tla` file's own comments say which is
which. See [README.background.md](README.background.md) for what this
means concretely for each module.

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
java -jar spec/.tools/tla2tools.jar -deadlock -cleanup -config spec/<name>.cfg spec/<module>.tla
```

## Coverage matrix

| Model | Covers | Configs | Result |
|---|---|---|---|
| `RunnerRegistration.tla` | GitHub's runner-registration state vs. `Allocation.phase`, across all four Deleted/TimedOut transitions, plus GC pruning and an unpatched fifth leak path | `pending_only` | **Violates** `NoOrphanedRegistration` (reproduces PR #163 exactly as it shipped) |
| | | `all_four` | Clean (PR #168 + the current re-verifying prune design) |
| | | `unknown_leak` | **Violates** `NoIrrecoverableOrphan` (an unpatched fifth path, combined with #161's *original* age-only pruning, produces a permanently unrecoverable orphan) |
| | | `reverifying_prune` | Clean for `NoIrrecoverableOrphan` (the design that actually shipped: pruning re-verifies the registration immediately before deleting a record, closing the *irrecoverable*-orphan gap regardless of which transition - or an unpatched one - produced it; `NoOrphanedRegistration` itself is still transiently violated right after the leak, which is expected) |
| `TickConcurrency.tla` | `Operator.Tick`'s `o.mu` release contract against its own spawned goroutines | `buggy` | **Violates** `MuReleasedOnlyWhenIdle` (reproduces PR #157's bug) |
| | | `fixed` | Clean (PR #157's fix) |
| `BudgetAdmission.tla` | `budget.go`'s admission-gating arithmetic (`spentToday`, the ceiling check) | `ceiling` | Clean (the gate itself is internally consistent) |
| | | `lockout_witness` | **Violates** `NeverFullyLockedOutWhileIdle` (a real, reachable trade-off: an all-interrupted day can show the ceiling as fully spent while real utilization is zero) |
| `RetryBound.tla` | `retry.go`'s bounded interruption-rerun protocol | `max1`/`max2`/`max3` | Clean for every value `recovery.go` actually allows (`MaxRetries \in 1..3`) |
| `AdmissionSlot.tla` | `admission.State`/`fleet.Released`'s per-allocation slot-release discipline (distinct from `BudgetAdmission.tla`'s aggregate spend ceiling) | `pre174` | **Violates** `EveryTerminalIsReleasable` (reproduces issue #174 exactly as it shipped in v1.2.0: a TimedOut allocation had no path back to a usable slot) |
| | | `post175` | Clean (PR #175: both terminal phases release their slot) |
| `JITRequest.tla` | the shared `jitLimiter` spacing for GitHub JIT-config requests added by #179 | `spacing_disabled` | **Violates** `NoJitBurst` (reproduces the pre-#179 burst shape: two allocations can start JIT requests in the same clock slot) |
| | | `spacing_enabled` | Clean (the burst-1 limiter requires an intervening `JITAdvance`) |
| `ExternalFailureVisibility.tla` | the observable-cause swallowing behind #176, expanded across JIT, WireGuard, cloud-create, observe, and delete failures | `pre178` | **Violates** `FailureCauseVisible` (a JIT failure is recorded as the generic preparation sentinel) |
| | | `current` | **Violates** `FailureCauseVisible` (current code preserves only JIT; WireGuard/cloud-create/observe/delete failures still collapse) |
| | | `post_all_fixes` | Clean (the extrapolated fix: every external failure class preserves its own cause) |
| `ListenerSession.tla` | the scale-set listener's own demand-observation protocol (`MessageSessionClient.GetMessage`/`TotalAssignedJobs`) - the one subsystem every other module here sits downstream of and none of them model | `stale_session_witness` | **Violates** `NoSilentlyStrandedDemand` (a listener session silently orphaned on GitHub's side strands real queued demand indefinitely - confirmed in production; a `202`/no-new-message poll response is indistinguishable from a genuinely idle scale set at the protocol level, and the client has no internal recovery path for it) |
| `ExternalCallBudget.tla` | every external (GitHub/cloud) call reachable from `runLeader`'s cancel-only, no-deadline ctx outside Step's own already-bounded per-allocation goroutine - six real call sites, enumerated exactly | `pre_fix` | **Violates** `NeverPermanentlyStuck` (any one of the six stalling hangs reconciliation forever - reproduces a real incident: a leader pod idle, no log line, readyz 503, for 49+ minutes, identically on restart) |
| | | `partial_fix_witness` | **Violates** `NeverPermanentlyStuck` (leaving even one of the six unbounded - not just the one the incident report pointed at directly - still permanently strands reconciliation) |
| | | `post_fix` | Clean (all six call sites now wrapped in their own `externalCallBudget`/`githubStartupBudget` context) |

Every "Violates" row above is a **deliberate** counterexample or witness
config - see each `.tla` file's own header comment for what it demonstrates
and why the violation is expected, not a regression.

## What is not modeled here

- Admission-ceiling divergence between runnerscout's local `Admitted` and
  GitHub's own claimed-runner count (E17/E18).
- A price-only eviction policy for D5 (kill a `Running` allocation when a
  cheaper offering appears - the original motivating example for this
  whole exploration).
- Credential invalidation mid-cycle (E32), network-profile CIDR pool /
  peer-membership churn (E26/E27), leader-election handoff overlap (E25).
- Operator crash/restart mid-transition, config/binding changes with
  allocations in flight (E28), job-queued/cancelled events (E1/E2),
  catalog staleness for an already-committed allocation (E5/E7).

See [README.background.md](README.background.md) for why each of these was
set aside rather than modeled, what a real attempt at the eviction policy
would need, and the full state-space research this grew out of.
