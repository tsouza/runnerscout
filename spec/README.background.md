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
(`ToCreating -> Claim -> CreateConfirmedAbsent`), landing in
`phase=Deleted, githubReg=Registered` - precisely the `Creating -> Deleted`
gap the research had flagged. That became issue #167 and PR #168. (This
sentence's own action order was corrected later, once `Claim`/`ToCreating`'s
guards were fixed to match Step's real causal order - see "Six call sites,
then seven, then nine" below for how that was found; the counterexample
itself, and everything issues #167/#168 fixed, is unchanged.)

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
the original JIT case (`pre178`) and the WireGuard/cloud-create/observe/
delete gaps that existed alongside it (`post178_pre182`), because the
swallowing shape was not specific to JIT. That state is now historical: #182
closed the remaining four classes, so `post_all_fixes` is no longer an
extrapolation - it is what ships today, and the module's own header comment
was corrected to say so rather than continue claiming the gap as current
(a later review verification caught the earlier `current.cfg`, retired in
favor of `post178_pre182`, describing a state #182 had already fixed before
this correction - see the independent verification pass below for how that
was found). TLA+ models the failure causes as distinct labels, not as the
real error strings, so a clean run here means the classification invariant
holds, not that the Go text itself is correct.

## ListenerSession.tla: the subsystem every other module sits downstream of

Every module in this directory - `RunnerRegistration`, `TickConcurrency`,
`BudgetAdmission`, `RetryBound`, `AdmissionSlot`, `JITRequest`,
`ExternalFailureVisibility` - models something that happens *after*
`HandleDesiredRunnerCount` already has a demand count to act on. None of
them model where that count comes from: the scale-set listener's own
session (`github.com/actions/scaleset`'s `MessageSessionClient.GetMessage`,
driving `TotalAssignedJobs`). Asked directly why an exhaustive-modeling
mandate had missed this, the honest answer was that framing it as "outside
what the modeling was aimed at" was itself the same unjustified scope-
narrowing the mandate was meant to rule out - the listener pipeline was
never disclosed as excluded the way the four items in "why the remaining
events were set aside" above were; it just never got the first-principles
"what are all its states and actions" pass any subsystem here deserves.

The trigger was a live production report: a scale set stopped observing any
GitHub demand at all, with no error anywhere in the controller (Tick
succeeding, CR changes reacting correctly, admission bookkeeping doing
exactly what it was told) - because what it was told was persistently zero.
A suspend/resume cycle, forcing a fresh listener session, resolved it.
Reading `session_client.go` directly (not guessing) confirms why: a
`202 Accepted` long-poll response decodes to `(nil, nil)` - "no new message
right now" - identically whether nothing is genuinely queued or the session
has gone quietly orphaned on GitHub's own side. Only a `401` triggers the
client's one built-in recovery path (`refreshMessageSession`), and an
orphaned session, by definition, never returns one. `ListenerSession.tla`'s
`NoSilentlyStrandedDemand` witness is exactly that shape: `SessionGoesStale`
then `JobBecomesAvailable` reaches a state with real demand pending and no
action able to fire except an external `ForceFreshSession` - which nothing
inside `Listener.Run` or `MessageSessionClient.GetMessage` triggers on its
own. This is a disclosed, unfixed limitation of the protocol as vendored,
not a runnerscout logic bug and not something this pass attempts to patch -
the value here is turning an ad hoc, hour-long diagnostic (inferring
staleness from absence of ConfigMap changes) into a documented, named
property so the next occurrence is recognized immediately instead of
re-investigated from scratch.

Separately, and independently of the modeling gap: `runLeader` (operator.go)
constructed the listener with no `Logger` set, which `listener.Config`
defaults to a discard handler - every one of the listener's own diagnostic
lines (`TotalAssignedJobs` on each poll, message IDs) was silently thrown
away regardless of whether a TLA+ model existed for this subsystem or not.
That is a dependency-wiring omission, not a state-space question - no model,
however exhaustive, represents "does this Go constructor set this field."
It is fixed in the same change, because it is real, but it is a different
kind of gap than the one this section is about, and conflating the two would
overstate what modeling this subsystem actually closes.

A later adversarial review caught a real bug in the model itself:
`acquired` was set `TRUE` by `DeliverMessage` and never reset anywhere,
so `DeliverMessage`'s own `~acquired` guard could never be satisfied again
after the very first job in any behavior - a second real job, arriving to
a still-healthy `Fresh` session, would sit undelivered forever with nothing
distinguishing that state from the one this module exists to find, and
`NoSilentlyStrandedDemand` would not have caught it (it only ever checks
`session = "Stale"`). Fixed by resetting `acquired' = FALSE` as part of
`JobBecomesAvailable` - each new job starts its own delivery cycle
unacquired, regardless of a prior job's outcome, matching the real
protocol (`AcquireJobs` is called fresh per message, never a one-time
flag). The existing `stale_session_witness` config's result is unchanged -
this bug never affected the property that config actually checks.

## ExternalCallBudget.tla: extrapolating one incident to every call site it implicates

`ListenerSession.tla` (above) was triggered by one report of a scale set
stuck at zero admitted jobs. While investigating it, a second, more serious
report arrived from the same deployment: a leader pod that acquired its
lease and then made no progress at all - no log line, idle CPU, readyz
stuck at 503 - for 49+ minutes, reproduced identically within 2.5 minutes
of a restart. Read directly from the code, not guessed: `runLeader`'s own
`ctx` (from `WithLease`'s `context.WithCancel`) has no deadline, and lease
*renewal* is a separate goroutine that keeps succeeding as long as the
Kubernetes API is reachable - entirely independent of GitHub or cloud
connectivity. Nothing upstream ever cancels a stalled external call, so any
one of them, reachable from that `ctx` without its own timeout, hangs
forever - and so does everything sequenced after it.

The incident report pointed at one place: `runLeader`'s own scale-set
lookup and listener-session establishment, right after "Successfully
acquired lease" and before anything else logs. Fixing only that would have
repeated the exact shape of the `AdmissionSlot.tla`/`ListenerSession.tla`
misses above - patching the one instance a report happened to surface
instead of the class of bug it belongs to. Auditing every direct external
call in this package outside Step's own already-bounded per-allocation
goroutine (`tickStepBudget`/`tickCreateOrDeleteBudget` already wrap every
`Step` call, so those needed no change) found two more, unreported,
call sites with the identical shape: `pruneTerminalAllocations`'s
`DeregisterRunner` loop (one candidate stalling blocks every later one in
the same pass), and `refreshAWSPrices`/`refreshAzurePrices`/
`refreshGCPPrices`'s own per-offering `Observe` loops (a stalled price
observation blocks `HandleDesiredRunnerCount`, which blocks the listener's
own message loop - the same "admission stops entirely" symptom as the
lookup/session hang, via a different call path entirely).

`ExternalCallBudget.tla` models the call sites as one set rather than one
incident: `pre_fix` reproduces the original shape (nothing bounded),
`partial_fix_witness` proves that bounding only the two call sites the
incident report actually named is not sufficient - any of the remaining
sites left unbounded still permanently strands reconciliation - and
`post_fix` certifies the shipped state: every site wrapped in one of two
new budgets introduced together in this same change - `externalCallBudget`
for every site except runLeader's own two startup steps, which use the
separately-named `githubStartupBudget` (2 minutes, matching that a scale-set
lookup and session establishment legitimately need more room than a single
lightweight API call - see that var's own comment). Bounding a call does
not fix whatever external condition stalled it - the model's own
`Recover` action is deliberately silent about what happens next (a
Kubernetes restart on a fatal error, or the next loop iteration reached) -
it only prevents that stall from becoming permanent and invisible.

## Six call sites, then seven, then nine - completeness claims that kept being wrong

The module's own first version stopped at six call sites and said so in its
own header comment: "CallSites enumerates them exactly." Asked directly to
verify every TLA+ model against the current code with the explicit goal of
finding drift rather than confirming it looked fine, an independent pass
found a seventh, real, still-unbounded call site the first audit had missed:
`pollAzureInterruptions`'s single `AzureInterruptions.Poll` call
(azure_interruptions.go), reachable from the exact same unbounded `ctx`,
running once every `Tick` whenever `AzureInterruptionQueueURL` is
configured. It carried the identical risk as the other six - a stalled
Azure Storage Queue dequeue would have hung every subsequent `Tick`
indefinitely, the same incident shape this module exists to prevent - and
was not caught by the first pass despite that pass's own stated goal of
enumerating "exactly." Fixed the same way as the other six (wrapped in
`externalCallBudget`), and `CallSites` grew to seven members;
`partial_fix_witness` was updated to reflect the real historical state this
repository passed through (six bounded, `AzureInterruptionPoll` not yet
found) rather than an arbitrary hypothetical omission, since that state
genuinely existed for one commit.

The same verification pass also caught `ExternalFailureVisibility.tla`'s
`current.cfg` describing a state #182 had already fixed before the config
was ever checked against the code again - see that module's own section
above - and two issues in `RunnerRegistration.tla`, the very first module
this directory ever got: its `Claim`/`ToCreating` guards had the real
causal order backwards (the model required GitHub's registration before
the Creating phase could persist; Step's own code persists Creating first,
via Store.Save, before CreateWithResources - where the registration call
actually lives - is ever reached), and its `Prune` action silently dropped
`pruneTerminalAllocations`'s own additional `f.Released` gate for a Deleted
candidate without ever saying so. Neither changed what `NoOrphanedRegistration`/
`NoIrrecoverableOrphan` prove (re-run against all four existing configs,
same results), but the first was a genuine reachable-state omission (a
real crash window - Creating, not yet registered - the old guard made
structurally impossible to model) and the second was an unacknowledged
simplification the project's own convention (state what you abstract and
why) requires calling out explicitly rather than leaving implicit.

That pass's own report described its work as complete. It was not: a
second, independently dispatched adversarial review, run specifically
against the diff about to be released rather than against any one module in
isolation, found an eighth and ninth call site the first review had also
missed - `retry.go`'s `reconcilePendingRerun` (two `AttemptJobs` calls) and
`processInterruptionRetries` (one `AttemptJobs`, one `RerunFailedJobs`),
reachable from the same unbounded `ctx` one line away from the
already-fixed `pruneTerminalAllocations` call. This one was the most severe
of the nine: `internal/githubjobs.Client` falls back to `http.DefaultClient`
(`Timeout: 0`) when unconfigured, which is exactly how production
constructs it, so unlike the scaleset client's own 5-minute per-attempt
default, this site had no fallback bound at all before being wrapped.
`CallSites` grew to nine; `partial_fix_witness` was updated again to
reflect the newer real intermediate state (seven bounded, the two
`retry.go` sites not yet found). The same review also caught a comment on
`externalCallBudget` itself that said "applied everywhere" and then
enumerated a list missing `pollAzureInterruptions` - the exact site the
prior round had just added, pointing back at that same comment as its own
authority - a dangling cross-reference in a `.cfg` file's comment to a file
this same change deletes, and a background-doc claim that `githubStartupBudget`
"already existed" when it was introduced in this same unreleased diff.
`CallSites`'s own comment was rewritten afterward to stop asserting the
list is exhaustive - it documents the result of the most recent audit, not
a proof no tenth site exists, precisely because the second claim of
exhaustiveness was as wrong as the first.

None of these findings - across either review round - were found by any
model's or fix's own author re-reading their own work. All of them were
found by a fresh check whose only job was to disbelieve each claim until it
held up against the code as it stood at the moment of checking, not as it
stood when the model or comment was written - including disbelieving the
first review's own claim to have already done that.

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
