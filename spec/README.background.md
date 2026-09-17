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
followed directly from that gap: pruning (issue #161's GC, gated on
nothing but age in its original form) and a deliberately unpatched fifth
transition (`UnknownLeak`) standing in for "GitHub's registration diverges
for a reason other than any of the four known transitions" - research
finding #3's own phrasing, since no single-transition patch can be
complete against a bug nobody has written yet.

## The pruning design went through two shapes - the first was wrong

The first working version gated `Prune` on a separate `checked` boolean,
set either by a terminal transition's own deregistration call or by a
standalone `Reconcile` action modeling a periodic bulk list-and-diff
against GitHub (the capability finding #3 says the vendored
`github.com/actions/scaleset` client does not expose). The matching Go
code added `lifecycle.Allocation.RegistrationCleared`, set at each of the
four transition sites, and gated `Operator.pruneTerminalAllocations` on it.

An adversarial review (a second model, Opus, reviewing this branch
end-to-end with instructions to find fault, not confirm it looked fine)
found this was broken in production terms: `RegistrationCleared` is a Go
struct field that defaults to `false` (the zero value) for any record
already in a cluster's Store when this code deploys. Nothing ever
re-processes an already-terminal record to set it retroactively - `Step`
returns immediately for `Deleted`/`TimedOut` - so every terminal record
that predates the deploy would be permanently unprunable, silently
reopening #161 for the entire pre-upgrade backlog. Worse, the code
comments justified the trade-off by citing "`Runners` unconfigured" as the
reason a record might never get the flag - a case
`internal/operator/runners.go`'s own comment says cannot happen in
production (`operator.New`/`NewWithCredentials` always wire a real
client) - while never mentioning the pre-upgrade case that actually would.
The same review also found the model's own `checked` mechanism had the
identical bug: `UnknownLeak` can fire before `Claim` ever ran, leaving
`githubReg = "NotRegistered"`, and `Reconcile`'s guard
(`githubReg[id] = "Registered"`) would then never fire for that id - so
`checked` stayed `false` forever and `Prune` was permanently blocked. The
model had reproduced, inside the artifact meant to catch this class of
bug, the exact shape of stuck state it exists to find.

The fix, in both places, was to stop persisting a flag set once in the
past and instead re-verify at the moment of use. `Operator.pruneTerminalAllocations`
now calls `Runners.DeregisterRunner` immediately before `Store.Delete`,
every time, for every candidate - which needs no migration and is
uniformly correct regardless of when a record was created or which
transition (if any) is the one that reached `Deleted`/`TimedOut` for it.
`RunnerRegistration.tla`'s `Prune` action does the matching thing: the
confirm-or-clear step is folded into the same action that marks a record
pruned, not gated behind a separately-set flag. This also produced a
stronger result than the original design aimed for: `PruneReVerifies=TRUE`
keeps `NoIrrecoverableOrphan` clean even with `EnableUnknownLeak=TRUE` -
safety against *any* leak cause, known or not, not just the four patched
transitions plus a reconciliation capability that does not exist yet. A
true periodic reconciliation independent of any one allocation's lifecycle
would still improve *promptness* (catching a leak before
`terminalRetention` elapses, rather than at the next prune pass) - that
remains a real, unbuilt capability, but it is a liveness argument (bounded
time-to-fix), and this module only checks safety invariants.

`lifecycle.Allocation.RegistrationCleared` was removed entirely rather
than kept as redundant defense-in-depth: it added a bug, a permanently
untested set of three of its four assignment sites (confirmed by mutation
testing - commenting out three of the four `a.RegistrationCleared = ...`
lines left the full test suite green), and no longer had a purpose once
`pruneTerminalAllocations` re-verifies on its own.

## Bounded model checking, precisely

TLC's "no error found" results are exhaustive over exactly the constants
each `.cfg` supplies, never over all N. Some of that boundedness is inert
in practice - `RunnerRegistration.tla` has zero coupling between distinct
`AllocIDs` (no shared variable, no guard referencing another id's state,
no `MaxRunners` ceiling): distinct-state counts at `|AllocIDs| = 1, 2, 3`
are 11, 121, 1331 - exactly 11^N, confirming it is a Cartesian product of
independent single-allocation machines. A per-allocation safety property
like `NoOrphanedRegistration` needs only one `AllocID` to be exhaustive;
the second `AllocID` these configs use buys nothing beyond confirming no
accidental cross-id interference exists, which is worth doing once but is
not evidence of anything that scales with N.

That same fact is also a real limit on what this module can show:
"a stranded registration permanently eats a `maxRunners` slot" (the
production impact that originally motivated #162) is a claim about a
*shared ceiling*, and this module has no variable for one.
`NoOrphanedRegistration` proves no *individual* terminal allocation ever
holds a live registration, which - summed over every id - implies the
*aggregate count* of terminal-but-still-registered runners is always
zero. That is a proof about the cause (no orphan ever exists), not a
model of the starvation effect itself (GitHub refusing to offer more jobs
because it believes the scale set is full), which would need its own
`MaxRunners`-ceiling variable to show directly. An earlier draft of this
document conflated the two, dismissing admission-ceiling divergence
(E17/E18) as something `githubReg` "already" covers - that overstated
what a per-allocation absence-of-orphan invariant actually demonstrates.

`RetryBound.tla` is the one module whose bound is not arbitrary:
`recovery.go`'s own `p.MaxRetries < 1 || p.MaxRetries > 3` makes the real
domain exactly `{1, 2, 3}`, and all three are checked (`RetryBound_max1/2/3.cfg`)
rather than picking one value and calling it representative.
`TickConcurrency.tla` and `BudgetAdmission.tla` use small constants
(`MaxInflight=3`, `MaxBudgetMicros=3`/`ReservationMicros=1`/`MaxAdmitted=5`)
chosen only to keep the state space small for a fast run - nothing in
either module's logic depends on those specific values, but nothing
proves that formally either; bumping them and re-running is the way to
gain more confidence, not a substitute for checking.

## Two invariants are restatements of their own guards, not discoveries

`TickConcurrency.tla`'s `MuReleasedOnlyWhenIdle` under the *fixed* config,
and `BudgetAdmission.tla`'s `BudgetNeverExceeded`, both hold because the
only action that could violate them has a guard identical to the
invariant itself (`ReleaseNormally` requires `inflight = 0`; `Admit`
requires `SpentToday + ReservationMicros <= MaxBudgetMicros`). TLC
checking them is a sanity check on internal consistency (a copy-paste
error or off-by-one in the guard would show up), not an independent
verification that the model matches the real Go arithmetic. The results
that carry real signal in those two modules are the *buggy* config
(`TickConcurrency`, a genuine 3-step counterexample against the
pre-#157 code's shape) and the *lockout_witness* config
(`BudgetAdmission`, a genuine reachable-state demonstration of research
finding #6). Both `.tla` files say this explicitly in their own comments
now, next to the invariant in question.

## The abandoned eviction-policy module (D5)

See `BudgetAdmission.tla`'s own header comment for the full account - it
was rewritten once already, because the first version's stated reasons
did not hold up: it claimed the blocker was needing per-allocation
identity, when `RunnerRegistration.tla` in the same directory already is a
per-allocation-identity model, and it claimed `lifecycle.Allocation`
lacking a `readyAt` field made the policy "not even expressible," when a
model of a *candidate* policy positing a field the real code doesn't have
yet is exactly what `UnknownLeak` already does elsewhere in this
directory. The honest reason is scope: validating a genuinely correct
thrashing check needs real per-allocation identity plus a cooldown/fairness
argument, which is a meaningfully bigger undertaking than the other four
modules here, and was left for a dedicated pass rather than rushed to a
result that would not have held up.

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
- **Operator crash/restart mid-transition** is a real gap in this
  exploration, not a reasoned exclusion: it was in the original research
  enumeration and this branch widens the deregister-then-save window from
  one Step site to four without ever modeling a crash between them.
  `RunnerRegistration.tla` has no crash action and no invariant covering
  the converse divergence direction (a non-terminal allocation whose
  registration has already been removed, e.g. by a crash after
  `Deregister` but before the phase transition persists) - it only ever
  checks `terminal => not registered`. Left unmodeled for scope, not
  because it was checked and found safe.
- **Config/binding change with allocations in flight (E28)** has a real
  code path (`internal/operator/operator.go`'s "provider or class binding
  changed; restore original configuration for cleanup" rejection) that
  was never represented here.
- **Job cancelled or queued (E1/E2)** is, structurally, a plausible
  independent cause of registration divergence - the same shape
  `UnknownLeak` already stands in for abstractly, but never named or
  distinguished from an unspecified "future bug."
- **Catalog staleness for an already-Creating/Running allocation (E5/E7)**
  was never represented in any module here.

None of the four items above were evaluated and deliberately excluded the
way credentials/CIDR-churn/leader-election were - they are gaps in this
pass's coverage, named here so a future pass does not have to rediscover
that they are missing.

## AdmissionSlot.tla: a gap this pass never even named

Issue #174 (a v1.2.0 production incident: a batch of TimedOut allocations
under sustained real demand permanently consumed their admission slots,
because release was gated on the same zero-demand `Cohort` reset used for a
full-fleet-idle rebaseline) exposed a coverage gap distinct from every one
listed above. The four items in "why the remaining events were set aside"
were at least named as gaps once the original research enumeration
surfaced them, even though they were never modeled. This one was not named
anywhere - not modeled, not deliberately excluded, not flagged for a future
pass. `admission.State`'s own per-allocation lifecycle (`Admitted`,
`fleet.Released`, which terminal phase actually releases a slot) was never
treated as its own subsystem worth enumerating events for. `BudgetAdmission.tla`
touches the same code area but only as an aggregate proxy for a different
question (the daily spend ceiling); it has no `Admitted`/`Released`
variables and could not have caught this by construction, independent of
which invariants it checks.

The honest reason: the original enumeration was organized around the two
things that prompted this whole exploration - GitHub's registration state
diverging, and the budget/ceiling question about price dropping while
capacity is full. A state machine that looked like solved, understood
plumbing (a slot counter and a release flag) never got the same
first-principles "what are all the events that can happen to this" pass
`RunnerRegistration.tla`'s subject got. `AdmissionSlot.tla` is that pass,
applied retroactively, once a production incident forced the question. Its
`EveryTerminalIsReleasable` invariant would have caught #174 before it
shipped, had this module existed first: `ReleasablePhases` (standing in for
the code's actual release guard) and `{"Deleted", "TimedOut"}` (standing in
for what "provably inert, should release" means) are independently
parameterized, and the pre-174 config's three-step counterexample
(`Claim -> ToTimedOut`, no third step ever enabled) reproduces the real
incident's shape exactly.

## JIT spacing and external-failure visibility: a different class than the others

`JITRequest.tla` and `ExternalFailureVisibility.tla` were added after
issue #176 exposed a gap the earlier modules could not catch by construction.
The earlier modules model runnerscout's own state machines; #176 was not a
state-machine invariant violation but an external side effect (`GitHub JIT
config generation`) failing under a concurrent burst, whose real error was
then swallowed and misattributed to GCP.

`JITRequest.tla` models only the spacing property the mitigation adds: a
shared burst-1 token prevents two allocations from starting JIT requests in
the same clock slot. It keeps the failed-request path in the model: a failed
JIT request returns the allocation to `Pending` and consumes an attempt, so
the model does not silently pretend failure is terminal. It still does not
model GitHub's undocumented limit or real HTTP statuses, because those are
not part of this codebase's state space. The meaningful result is the
`spacing_disabled` counterexample; the `spacing_enabled` run is a
guard-restatement sanity check, not an independent discovery, and the
module's header says so.

`ExternalFailureVisibility.tla` models the diagnosability property that was
actually broken: an observable condition must name the failing subsystem
instead of collapsing every preparation failure to one sentinel. It covers
the original JIT case (`pre178`) and the current gaps across WireGuard,
cloud-create, observe, and delete failures (`current`), because the
swallowing shape is not specific to JIT. `post_all_fixes` is the extrapolated
fix, not current behavior. TLA+ models the failure causes as distinct labels,
not as the real error strings, so a clean run here means the classification
invariant holds, not that the Go text itself is correct.

## Why `make tlc` and not CI

These models check themselves, not the Go code - there is no mechanism
that would catch a real implementation change silently drifting away from
what `RunnerRegistration.tla` or the others describe. Running them on every
CI build would suggest a stronger guarantee than they actually provide.
`make tlc` keeps them runnable on demand, for whoever is reasoning about
one of these areas next, without implying they gate merges the way
`make test` does.

## A known, accepted duplication

Each config's expectation (clean vs. which invariant it's expected to
violate) is currently written in four places: the `.cfg` file's own header
comment, the `.tla` module's header comment, `tools/tlc.py`'s `RUNS`
table, and this directory's `README.md` matrix. That already drifted once
(the `RetryBound_max1/2/3.cfg` split and the `reverifying_prune.cfg`
rename both required updating four places by hand) and could again. A
single source of truth (e.g. `tools/tlc.py` parsing the expectation out of
each `.cfg`'s own header) would remove the class of bug, but was not built
here - noted so a future change to any config remembers to check all four
places rather than assuming one script catches drift.
