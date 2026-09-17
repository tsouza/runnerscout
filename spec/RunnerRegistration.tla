---- MODULE RunnerRegistration ----
(***************************************************************************)
(* Model of runnerscout's Allocation phase machine plus GitHub's own,      *)
(* independently-evolving runner-registration state -- the shadow state   *)
(* machine identified in the state-space research as the highest-value    *)
(* target: it is written to (Claim) but only sometimes read back and      *)
(* reconciled (Deregister) on the way into a terminal phase.               *)
(*                                                                        *)
(* BOUNDEDNESS NOTE (read before trusting any "clean" result below): TLC   *)
(* checks every invariant over exactly the fixed AllocIDs/constants each   *)
(* .cfg supplies -- it proves the property for those bounds, not for all   *)
(* N. This module has zero coupling between distinct AllocIDs (no shared   *)
(* variable, no guard referencing another id's state, no MaxRunners        *)
(* ceiling) -- it is a Cartesian product of |AllocIDs| independent copies  *)
(* of a single-allocation machine, confirmed by distinct-state counts of   *)
(* 11, 121, 1331 for |AllocIDs| = 1, 2, 3 (exactly 11^N). Concretely this   *)
(* means: (a) a per-allocation safety property like NoOrphanedRegistration *)
(* below needs only one AllocID to be exhaustive -- a second buys nothing  *)
(* except confirming no accidental cross-id interference exists, which is  *)
(* worth doing once, not evidence of anything scaling with N; (b) this     *)
(* module cannot express or check the aggregate property that actually     *)
(* motivated #162 -- "a stranded registration permanently eats a           *)
(* maxRunners slot at GitHub's level" is a claim about a shared ceiling     *)
(* this module has no variable for. NoOrphanedRegistration proves no       *)
(* individual terminal allocation ever holds a live registration, which    *)
(* implies (by summing an absent-orphan property over every id) that the   *)
(* aggregate count of terminal-but-still-registered runners is always      *)
(* zero -- but that is a proof about the CAUSE, not a model of the         *)
(* maxRunners-starvation EFFECT itself, which would need its own ceiling   *)
(* variable to show directly.                                              *)
(*                                                                        *)
(* LAYER 1 -- the four known transitions. Each DeregOn* constant toggles  *)
(* whether that one Deleted/TimedOut site deregisters, mirroring          *)
(* lifecycle.Controller.Step's four such sites exactly:                  *)
(*   DeregOnPendingTimedOut  -- Pending    -> TimedOut  (issue #162)      *)
(*   DeregOnCreatingDeleted  -- Creating   -> Deleted   (CreateConfirmedAbsent) *)
(*   DeregOnRunningDeleted   -- Running    -> Deleted   (spot interruption)     *)
(*   DeregOnDeletingDeleted  -- Deleting   -> Deleted   (normal expiry/drain)   *)
(*                                                                        *)
(* LAYER 2 -- is patching the four known transitions actually COMPLETE?    *)
(* EnableUnknownLeak adds a fifth, deliberately-unpatched transition      *)
(* representing "some not-yet-imagined code path reaches Deleted without  *)
(* deregistering" (event E31 in the research report: GitHub's registration *)
(* diverges from local state for a reason other than any of the four      *)
(* known transitions).                                                    *)
(*                                                                        *)
(* PruneReVerifies toggles between internal/operator's terminal-record GC  *)
(* (issue #161) as it originally shipped (deletes a terminal record once   *)
(* it is old enough, with no dependency on registration state at all) and  *)
(* the current shipped design (Operator.pruneTerminalAllocations calls     *)
(* Runners.DeregisterRunner immediately before deleting the record, not    *)
(* by trusting a flag some earlier Step call may or may not have set).     *)
(* That re-verification is a single action here, Prune itself, not a      *)
(* separate periodic reconciliation step gating a second, independent      *)
(* prune action -- an earlier draft of this model had exactly that split   *)
(* (a `checked` flag set by transitions or a standalone `Reconcile`        *)
(* action, with Prune gated on it) and it was wrong: it could leave        *)
(* `checked` false forever for a leak whose registration was never         *)
(* `"Registered"` in the first place (UnknownLeak fires from any           *)
(* non-terminal phase, including one where `Claim` never ran), which       *)
(* blocked Prune permanently and reproduced, inside the model meant to     *)
(* catch this class of bug, the exact kind of stuck state the model exists *)
(* to find. Folding the confirm step into Prune itself removes that        *)
(* failure mode by construction: there is nothing left to get permanently  *)
(* stuck waiting on. A true periodic reconciliation against GitHub's own   *)
(* registration list independent of any one allocation's lifecycle -- the  *)
(* capability research finding #3 says the vendored scaleset client does   *)
(* not expose -- would still improve promptness (catching a leak before    *)
(* terminalRetention elapses) but is not modeled here: that is a liveness  *)
(* argument (bounded time-to-fix), and this module only checks safety      *)
(* invariants.                                                             *)
(*                                                                        *)
(* Configs (see the .cfg files):                                          *)
(*   pending_only    -- PR #163 exactly as it shipped: only Pending ->     *)
(*                       TimedOut deregisters at transition time, AND      *)
(*                       pruning is age-only (PruneReVerifies=FALSE - #161  *)
(*                       had not yet been reworked at that point in the    *)
(*                       repo's history). Violates NoOrphanedRegistration   *)
(*                       in 3 steps (the transition-time gap #167/#168      *)
(*                       later fixed) - and, checked separately, also       *)
(*                       violates NoIrrecoverableOrphan, since age-only     *)
(*                       pruning cannot catch what the transition missed.   *)
(*                       Only PruneReVerifies=TRUE (all_four,               *)
(*                       reverifying_prune below) closes that.              *)
(*   all_four        -- PR #168's fix for #167, PruneReVerifies=TRUE. No   *)
(*                       violation of either invariant.                    *)
(*   unknown_leak    -- all_four PLUS an unpatched fifth path PLUS         *)
(*                       PruneReVerifies=FALSE (#161 as it originally      *)
(*                       shipped). Proves age-only pruning is unsafe        *)
(*                       against any leak a transition-site patch does     *)
(*                       not cover, known or not: a permanently             *)
(*                       unrecoverable orphan.                              *)
(*   reverifying_prune -- same unpatched fifth path, PruneReVerifies=TRUE   *)
(*                       (what actually shipped). NoIrrecoverableOrphan     *)
(*                       holds even against an unpatched/unknown leak       *)
(*                       path, because Prune's own re-verification covers  *)
(*                       every cause, not just the four known transitions. *)
(***************************************************************************)
EXTENDS Naturals

CONSTANTS
  AllocIDs,
  DeregOnPendingTimedOut,
  DeregOnCreatingDeleted,
  DeregOnRunningDeleted,
  DeregOnDeletingDeleted,
  EnableUnknownLeak,
  PruneReVerifies

ASSUME DeregOnPendingTimedOut \in BOOLEAN
ASSUME DeregOnCreatingDeleted \in BOOLEAN
ASSUME DeregOnRunningDeleted \in BOOLEAN
ASSUME DeregOnDeletingDeleted \in BOOLEAN
ASSUME EnableUnknownLeak \in BOOLEAN
ASSUME PruneReVerifies \in BOOLEAN

Phases == {"Pending", "Creating", "Running", "Deleting", "Deleted", "TimedOut"}
RegStates == {"NotRegistered", "Registered", "Deregistered"}

VARIABLES phase, githubReg, pruned

vars == <<phase, githubReg, pruned>>

TypeOK ==
  /\ phase \in [AllocIDs -> Phases]
  /\ githubReg \in [AllocIDs -> RegStates]
  /\ pruned \in [AllocIDs -> BOOLEAN]

Init ==
  /\ phase = [id \in AllocIDs |-> "Pending"]
  /\ githubReg = [id \in AllocIDs |-> "NotRegistered"]
  /\ pruned = [id \in AllocIDs |-> FALSE]

(* Idempotent, matching RunnerDeregistrar's contract: no-ops when the      *)
(* registration is already gone or was never made.                        *)
Deregister(id) == IF githubReg[id] = "Registered" THEN "Deregistered" ELSE githubReg[id]

(* Pending -> Creating is Step's own Store.Save, persisted before          *)
(* CreateWithResources is ever called - unconditional on githubReg.        *)
ToCreating(id) ==
  /\ phase[id] = "Pending"
  /\ phase' = [phase EXCEPT ![id] = "Creating"]
  /\ UNCHANGED <<githubReg, pruned>>

(* GitHub's scale-set listener protocol claims the job and registers a    *)
(* runner name via GenerateJitRunnerConfig only once Creating (that call  *)
(* lives inside CreateWithResources, itself only ever reached from the    *)
(* Creating branch of Step, after the phase transition above has already  *)
(* been durably persisted) -- an event runnerscout does not itself choose *)
(* to trigger the timing of (E8 in the research report), not tied to any  *)
(* one Step call succeeding. This ordering (persist Creating, then        *)
(* attempt registration - never the reverse) was corrected from an        *)
(* earlier version of this module that had Claim gated on Pending and a   *)
(* precondition of ToCreating, backwards from what Step actually does;    *)
(* see README.background.md for how that was found. The correction        *)
(* widens reachable states (Creating with NotRegistered is now reachable, *)
(* matching a real crash window between the Store.Save and the            *)
(* CreateWithResources call) without changing NoOrphanedRegistration/      *)
(* NoIrrecoverableOrphan's results - CreateConfirmedAbsent's own guard      *)
(* never depended on githubReg's value either way.                         *)
Claim(id) ==
  /\ phase[id] = "Creating"
  /\ githubReg[id] = "NotRegistered"
  /\ githubReg' = [githubReg EXCEPT ![id] = "Registered"]
  /\ UNCHANGED <<phase, pruned>>

(* Since Claim's own guard above now requires Creating, githubReg[id] is    *)
(* always "NotRegistered" for any id still in Pending - meaning            *)
(* DeregOnPendingTimedOut has no observable effect in ANY config, not just *)
(* one: Deregister(id) is a no-op whenever githubReg[id] isn't "Registered" *)
(* already, by its own definition above. Kept as a constant (rather than   *)
(* removed) because it still faithfully models PR #163's own real          *)
(* configuration - that PR's dereg call at this exact transition genuinely *)
(* exists in Step - even though nothing in this state space can any longer *)
(* reach a state where firing it changes anything. See                    *)
(* RunnerRegistration_pending_only.cfg's own comment for what its          *)
(* counterexample actually exercises instead.                              *)
PendingTimedOut(id) ==
  /\ phase[id] = "Pending"
  /\ phase' = [phase EXCEPT ![id] = "TimedOut"]
  /\ githubReg' = [githubReg EXCEPT ![id] =
       IF DeregOnPendingTimedOut THEN Deregister(id) ELSE githubReg[id]]
  /\ UNCHANGED pruned

CreateConfirmedAbsent(id) ==
  /\ phase[id] = "Creating"
  /\ phase' = [phase EXCEPT ![id] = "Deleted"]
  /\ githubReg' = [githubReg EXCEPT ![id] =
       IF DeregOnCreatingDeleted THEN Deregister(id) ELSE githubReg[id]]
  /\ UNCHANGED pruned

ToRunning(id) ==
  /\ phase[id] = "Creating"
  /\ phase' = [phase EXCEPT ![id] = "Running"]
  /\ UNCHANGED <<githubReg, pruned>>

(* Provider-external: the cloud can interrupt a Running VM at any time,   *)
(* which never gives the runner process a graceful shutdown to           *)
(* self-deregister the way a normally-completed ephemeral job would.     *)
SpotInterruption(id) ==
  /\ phase[id] = "Running"
  /\ phase' = [phase EXCEPT ![id] = "Deleted"]
  /\ githubReg' = [githubReg EXCEPT ![id] =
       IF DeregOnRunningDeleted THEN Deregister(id) ELSE githubReg[id]]
  /\ UNCHANGED pruned

ToDeleting(id) ==
  /\ phase[id] = "Running"
  /\ phase' = [phase EXCEPT ![id] = "Deleting"]
  /\ UNCHANGED <<githubReg, pruned>>

DeletingConfirmed(id) ==
  /\ phase[id] = "Deleting"
  /\ phase' = [phase EXCEPT ![id] = "Deleted"]
  /\ githubReg' = [githubReg EXCEPT ![id] =
       IF DeregOnDeletingDeleted THEN Deregister(id) ELSE githubReg[id]]
  /\ UNCHANGED pruned

(* LAYER 2 -------------------------------------------------------------- *)

(* Stands in for "a not-yet-imagined fifth code path reaches Deleted      *)
(* without ever calling Runners.DeregisterRunner" -- deliberately never   *)
(* touches githubReg, by construction, the same way a missed call site in *)
(* real Go code would leave it untouched. Fires from ANY non-terminal     *)
(* phase (including Pending before Claim has even run), not only from     *)
(* states where a registration exists, so it also covers "no registration *)
(* ever existed and pruning must not assume one always does." Only        *)
(* enabled when EnableUnknownLeak is TRUE, so the known-transitions-only   *)
(* configs never exercise it.                                             *)
UnknownLeak(id) ==
  /\ EnableUnknownLeak
  /\ phase[id] \notin {"Deleted", "TimedOut"}
  /\ phase' = [phase EXCEPT ![id] = "Deleted"]
  /\ UNCHANGED <<githubReg, pruned>>

(* internal/operator's terminal-record GC (issue #161). Age/retention is  *)
(* abstracted away as "eventually enabled", which is sound for a safety   *)
(* property: if pruning is ever unsafe, it is unsafe regardless of how    *)
(* long the retention window is. PruneReVerifies=TRUE is what actually    *)
(* shipped: the confirm-or-clear call happens as part of the same action  *)
(* that marks the record pruned, so it cannot be skipped or left          *)
(* permanently pending the way a separately-gated flag could be (see the  *)
(* module header). PruneReVerifies=FALSE is #161 as it originally         *)
(* shipped, kept here specifically to demonstrate why that design was     *)
(* unsafe.                                                                 *)
(*                                                                         *)
(* Acknowledged simplification: the real pruneTerminalAllocations also     *)
(* requires f.Released[id] before a Deleted (not TimedOut) candidate is    *)
(* eligible at all (see AdmissionSlot.tla, a separate module, for that     *)
(* admission-bookkeeping gate on its own terms) - this module has no       *)
(* variable for it and Prune fires for any non-terminal-to-terminal        *)
(* candidate regardless. That gate only delays when Prune can fire, never  *)
(* weakens the re-verification this module actually checks, so it does    *)
(* not change what NoOrphanedRegistration/NoIrrecoverableOrphan prove -    *)
(* but it is a real omission from this action's guard, not a claim this    *)
(* module makes and keeps, so it is stated here rather than left implicit. *)
Prune(id) ==
  /\ phase[id] \in {"Deleted", "TimedOut"}
  /\ ~pruned[id]
  /\ pruned' = [pruned EXCEPT ![id] = TRUE]
  /\ githubReg' = [githubReg EXCEPT ![id] =
       IF PruneReVerifies THEN Deregister(id) ELSE githubReg[id]]
  /\ UNCHANGED phase

Next ==
  \E id \in AllocIDs :
    \/ Claim(id)
    \/ ToCreating(id)
    \/ PendingTimedOut(id)
    \/ CreateConfirmedAbsent(id)
    \/ ToRunning(id)
    \/ SpotInterruption(id)
    \/ ToDeleting(id)
    \/ DeletingConfirmed(id)
    \/ UnknownLeak(id)
    \/ Prune(id)

Spec == Init /\ [][Next]_vars

(* An allocation must never sit in a terminal phase while GitHub still    *)
(* believes its runner is registered. A momentary violation right after a *)
(* transition that skips deregistration (pending_only.cfg; or an          *)
(* UnknownLeak step) is expected and not itself the point -- no single     *)
(* transition-site patch prevents that moment from existing at all;       *)
(* NoIrrecoverableOrphan below is the property that actually matters.     *)
NoOrphanedRegistration ==
  \A id \in AllocIDs :
    phase[id] \in {"Deleted", "TimedOut"} => githubReg[id] # "Registered"

(* The property that matters once a transition can skip deregistration    *)
(* (whether patched with DeregOn*=FALSE, or via UnknownLeak): local        *)
(* evidence must never be discarded (pruned) while GitHub still believes   *)
(* the runner is registered, because once pruned there is no way for       *)
(* anything in this system to ever discover or correct that divergence     *)
(* again -- this is finding #2 (the #161 GC fix compounding an undetected  *)
(* #162-class leak) made checkable.                                        *)
NoIrrecoverableOrphan ==
  \A id \in AllocIDs :
    pruned[id] => githubReg[id] # "Registered"

====
