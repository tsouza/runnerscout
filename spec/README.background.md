# TLA+ models — background

## Why this exists

Prompted by a question about whether runnerscout's own broker logic (spot
capacity, admission, GitHub's scale-set protocol) is the kind of problem
TLA+ is suited to. A first research pass modeled the domain from first
principles - not from reading the Go code and transcribing it, but by
enumerating the real events and decisions a spot-capacity CI-runner broker
could face, then comparing that enumeration against what the code actually
implements. That pass's single highest-value finding: GitHub's own
runner-registration state is a second, independently-evolving state machine
this codebase only ever writes to (via `GenerateJitRunnerConfig`), never
reads back and reconciles - and the just-fixed issue #162 patched exactly
one of the transitions that can leave it orphaned.

`RunnerRegistration.tla` operationalized that finding: TLC found a real
three-step counterexample against the code as it stood right after PR #163
(`Claim -> ToCreating -> CreateConfirmedAbsent`), landing in
`phase=Deleted, githubReg=Registered` - precisely the `Creating -> Deleted`
gap the research had flagged. That became issue #167 and PR #168.

## Why the model kept growing

Asked whether the first model actually mapped "all possible actions," the
honest answer was no - it covered exactly the four known Deleted/TimedOut
transitions, nothing else. Two extensions to `RunnerRegistration.tla`
followed directly from that gap:

1. **Pruning.** Issue #161's GC deletes a terminal allocation's only local
   record after 24h, with no dependency on registration state. Modeled as
   `Prune`, gated (in the `unknown_leak`/`reconciliation` configs) on a new
   `checked` variable - and TLC confirmed that today's real, age-only gate
   is unsafe against *any* leak path the four known transitions don't
   cover, known or not-yet-written.
2. **The unknown fifth path.** No single-transition patch can be complete
   against a bug nobody has written yet. `UnknownLeak` is a deliberately
   unpatched fifth transition standing in for exactly that (GitHub's
   registration diverging "for a reason other than any of the four known
   transitions" - research finding #3's own phrasing). Its presence is what
   makes `unknown_leak.cfg`'s violation of `NoIrrecoverableOrphan`
   meaningful: it is not restating the already-fixed #167 gap, it is asking
   "given #168 is fully applied, is the *system* complete?" The answer is
   no - only a `Reconcile` action (a periodic, transition-independent
   re-sync against GitHub's own registration list) closes it, and even then
   only once pruning is gated on having actually run that check.

That `checked`-gate finding was concrete and buildable *today*, independent
of the reconciliation action itself (which needs a bulk list-and-diff
capability the vendored `github.com/actions/scaleset` client does not
expose) - so it became a real fix: `lifecycle.Allocation.RegistrationCleared`,
set only when a terminal transition's own `deregister` call is confirmed,
and `Operator.pruneTerminalAllocations` now requires it. This is the one
place this exploration changed production code rather than only adding a
model of it.

## Why `TickConcurrency.tla` and `BudgetAdmission.tla`, and not further

Continuing the "map everything" instruction, two more modules were built
where they had concrete payoff:

- `TickConcurrency.tla` reproduces PR #157's already-fixed bug (an early
  `return` releasing `o.mu` while spawned goroutines were still running) as
  a three-step counterexample, and confirms the current code closes it.
  Chosen because it is the other historical bug this codebase has already
  had in exactly this "missing state handling" shape - the report's own
  point (finding #8) that a model would have caught it too, made concrete.
- `BudgetAdmission.tla` targets the user's own original motivating example
  (kill a `Running` allocation when budget frees up). It confirms the
  ceiling arithmetic itself is sound, and produces a reachable-state
  witness for research finding #6 (an all-interrupted day can look fully
  spent while real utilization is zero) - a real, demonstrable trade-off,
  not a hypothetical one.

An eviction-policy module for D5 was attempted and abandoned mid-build: an
aggregate-counts model (admitted/timedOut/interrupted counts, no
per-allocation identity) cannot distinguish "many different allocations
each evicted once" from "the same allocation evicted repeatedly," so it
cannot actually demonstrate or rule out thrashing - the one property that
would matter most for a real eviction policy. Presenting that toy result
anyway would have been misleading rather than useful; a real attempt needs
per-allocation identity (a small fixed set of allocation "slots," each
independently cycling through admit/evict/readmit), which is a
meaningfully bigger model than the others here and was left for a future
pass rather than rushed.

`RetryBound.tla` was cheap to build (the real code's own comments already
argue carefully for exactly the properties checked) and confirmed clean -
a useful negative result: this part of the codebase already reasons about
its state space by hand as carefully as a TLA+ model would.

## Why the remaining events were set aside, specifically

- **Credential invalidation mid-cycle (E32)** is fundamentally an
  error-classification question (does an invalid credential fail loudly
  and safely, or silently proceed with a stale one) - a property better
  checked by reading `internal/provider/*_credentials.go` and their tests
  than by state-space exploration, since the "state space" here is really
  just "valid or not," with no interesting interleaving.
- **Network-profile CIDR pool exhaustion and peer-membership churn
  (E26/E27)** run entirely under the same single-writer `o.mu` +
  Kubernetes-CAS discipline as every other admission decision in this
  codebase; the only genuinely distinct risk is two leader instances
  computing overlapping address ranges independently, which is the same
  leader-election-overlap question as E25 below, not a new one.
- **Leader-election handoff overlap (E25)** depends on `client-go`'s own
  `leaderelection` package never invoking `OnStartedLeading` after `Run`
  has returned - an external library's contract, not something this
  codebase's own `gate`/`workers` synchronization in `leadership.go` could
  violate on its own (that local pattern was read closely and is a
  straightforward, already-correct mutex-plus-`WaitGroup` shape). Modeling
  `client-go`'s own internals was judged out of proportion to the risk.

## Why `make tlc` and not CI

These models check themselves, not the Go code - there is no mechanism
that would catch a real implementation change silently drifting away from
what `RunnerRegistration.tla` or the others describe. Running them on every
CI build would suggest a stronger guarantee than they actually provide.
`make tlc` keeps them runnable on demand, for whoever is reasoning about
one of these areas next, without implying they gate merges the way
`make test` does.
