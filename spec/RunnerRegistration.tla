---- MODULE RunnerRegistration ----
(***************************************************************************)
(* Model of runnerscout's Allocation phase machine plus GitHub's own,      *)
(* independently-evolving runner-registration state -- the shadow state   *)
(* machine identified in the state-space research as the highest-value    *)
(* target: it is written to (Claim) but only sometimes read back and      *)
(* reconciled (Deregister) on the way into a terminal phase.               *)
(*                                                                        *)
(* Two layers:                                                            *)
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
(* known transitions). EnableReconciliation adds a periodic, phase-       *)
(* transition-independent action representing the bulk list-and-diff      *)
(* capability the report's finding #3 says does not exist in the vendored *)
(* scaleset client today. PruneRequiresChecked toggles between today's    *)
(* real #161 pruning design (gated on terminal phase + age alone) and a   *)
(* recommended alternative (also gated on a confirmed registration check) *)
(* -- this is what closes the compounding gap the report calls finding #2.*)
(*                                                                        *)
(* Configs (see the four .cfg files):                                    *)
(*   pending_only    -- PR #163's fix for #162 alone. Violates            *)
(*                       NoOrphanedRegistration in 3 steps.               *)
(*   all_four        -- PR #168's fix for #167. No violation: TLC         *)
(*                       exhausts the state space clean.                  *)
(*   unknown_leak    -- all_four PLUS an unpatched fifth path PLUS        *)
(*                       today's real (age-only) pruning design. Proves   *)
(*                       patching the four known sites is NOT enough: an  *)
(*                       unknown fifth leak still produces a permanently  *)
(*                       unrecoverable orphan once pruned.                *)
(*   reconciliation  -- same unpatched fifth path, but with the checked-  *)
(*                       gated pruning design AND a reconciliation action *)
(*                       available. Proves that recommendation actually   *)
(*                       closes the gap the previous config found, given  *)
(*                       reconciliation capability exists at all.         *)
(***************************************************************************)
EXTENDS Naturals

CONSTANTS
  AllocIDs,
  DeregOnPendingTimedOut,
  DeregOnCreatingDeleted,
  DeregOnRunningDeleted,
  DeregOnDeletingDeleted,
  EnableUnknownLeak,
  EnableReconciliation,
  PruneRequiresChecked

ASSUME DeregOnPendingTimedOut \in BOOLEAN
ASSUME DeregOnCreatingDeleted \in BOOLEAN
ASSUME DeregOnRunningDeleted \in BOOLEAN
ASSUME DeregOnDeletingDeleted \in BOOLEAN
ASSUME EnableUnknownLeak \in BOOLEAN
ASSUME EnableReconciliation \in BOOLEAN
ASSUME PruneRequiresChecked \in BOOLEAN

Phases == {"Pending", "Creating", "Running", "Deleting", "Deleted", "TimedOut"}
RegStates == {"NotRegistered", "Registered", "Deregistered"}

VARIABLES phase, githubReg, pruned, checked

vars == <<phase, githubReg, pruned, checked>>

TypeOK ==
  /\ phase \in [AllocIDs -> Phases]
  /\ githubReg \in [AllocIDs -> RegStates]
  /\ pruned \in [AllocIDs -> BOOLEAN]
  /\ checked \in [AllocIDs -> BOOLEAN]

Init ==
  /\ phase = [id \in AllocIDs |-> "Pending"]
  /\ githubReg = [id \in AllocIDs |-> "NotRegistered"]
  /\ pruned = [id \in AllocIDs |-> FALSE]
  /\ checked = [id \in AllocIDs |-> FALSE]

(* Idempotent, matching RunnerDeregistrar's contract: no-ops when the      *)
(* registration is already gone or was never made.                        *)
Deregister(id) == IF githubReg[id] = "Registered" THEN "Deregistered" ELSE githubReg[id]

(* GitHub's scale-set listener protocol claims the job and registers a    *)
(* runner name via GenerateJitRunnerConfig at or before Creating -- an    *)
(* event runnerscout does not itself choose to trigger (E8 in the         *)
(* research report), not tied to any one Step call succeeding.            *)
Claim(id) ==
  /\ phase[id] = "Pending"
  /\ githubReg[id] = "NotRegistered"
  /\ githubReg' = [githubReg EXCEPT ![id] = "Registered"]
  /\ UNCHANGED <<phase, pruned, checked>>

ToCreating(id) ==
  /\ phase[id] = "Pending"
  /\ githubReg[id] = "Registered"
  /\ phase' = [phase EXCEPT ![id] = "Creating"]
  /\ UNCHANGED <<githubReg, pruned, checked>>

PendingTimedOut(id) ==
  /\ phase[id] = "Pending"
  /\ phase' = [phase EXCEPT ![id] = "TimedOut"]
  /\ githubReg' = [githubReg EXCEPT ![id] =
       IF DeregOnPendingTimedOut THEN Deregister(id) ELSE githubReg[id]]
  /\ checked' = [checked EXCEPT ![id] = checked[id] \/ DeregOnPendingTimedOut]
  /\ UNCHANGED pruned

CreateConfirmedAbsent(id) ==
  /\ phase[id] = "Creating"
  /\ phase' = [phase EXCEPT ![id] = "Deleted"]
  /\ githubReg' = [githubReg EXCEPT ![id] =
       IF DeregOnCreatingDeleted THEN Deregister(id) ELSE githubReg[id]]
  /\ checked' = [checked EXCEPT ![id] = checked[id] \/ DeregOnCreatingDeleted]
  /\ UNCHANGED pruned

ToRunning(id) ==
  /\ phase[id] = "Creating"
  /\ phase' = [phase EXCEPT ![id] = "Running"]
  /\ UNCHANGED <<githubReg, pruned, checked>>

(* Provider-external: the cloud can interrupt a Running VM at any time,   *)
(* which never gives the runner process a graceful shutdown to           *)
(* self-deregister the way a normally-completed ephemeral job would.     *)
SpotInterruption(id) ==
  /\ phase[id] = "Running"
  /\ phase' = [phase EXCEPT ![id] = "Deleted"]
  /\ githubReg' = [githubReg EXCEPT ![id] =
       IF DeregOnRunningDeleted THEN Deregister(id) ELSE githubReg[id]]
  /\ checked' = [checked EXCEPT ![id] = checked[id] \/ DeregOnRunningDeleted]
  /\ UNCHANGED pruned

ToDeleting(id) ==
  /\ phase[id] = "Running"
  /\ phase' = [phase EXCEPT ![id] = "Deleting"]
  /\ UNCHANGED <<githubReg, pruned, checked>>

DeletingConfirmed(id) ==
  /\ phase[id] = "Deleting"
  /\ phase' = [phase EXCEPT ![id] = "Deleted"]
  /\ githubReg' = [githubReg EXCEPT ![id] =
       IF DeregOnDeletingDeleted THEN Deregister(id) ELSE githubReg[id]]
  /\ checked' = [checked EXCEPT ![id] = checked[id] \/ DeregOnDeletingDeleted]
  /\ UNCHANGED pruned

(* LAYER 2 -------------------------------------------------------------- *)

(* Stands in for "a not-yet-imagined fifth code path reaches Deleted      *)
(* without ever calling Runners.DeregisterRunner" -- deliberately never   *)
(* touches githubReg or checked, by construction, the same way a missed   *)
(* call site in real Go code would leave both untouched. Only enabled     *)
(* when EnableUnknownLeak is TRUE, so the "known-transitions-only" configs *)
(* never exercise it.                                                     *)
UnknownLeak(id) ==
  /\ EnableUnknownLeak
  /\ phase[id] \notin {"Deleted", "TimedOut"}
  /\ phase' = [phase EXCEPT ![id] = "Deleted"]
  /\ UNCHANGED <<githubReg, pruned, checked>>

(* A periodic, phase-transition-independent bulk list-and-diff against    *)
(* GitHub's own registration list -- the capability finding #3 says the   *)
(* vendored scaleset client does not expose today. Unlike the four known  *)
(* sites, this does not depend on which transition produced the           *)
(* divergence: it fires whenever a terminal allocation's registration     *)
(* has not yet been confirmed gone, regardless of cause.                  *)
Reconcile(id) ==
  /\ EnableReconciliation
  /\ phase[id] \in {"Deleted", "TimedOut"}
  /\ githubReg[id] = "Registered"
  /\ githubReg' = [githubReg EXCEPT ![id] = "Deregistered"]
  /\ checked' = [checked EXCEPT ![id] = TRUE]
  /\ UNCHANGED <<phase, pruned>>

(* internal/operator's terminal-record GC (issue #161). Age/retention is  *)
(* abstracted away as "eventually enabled", which is sound for a safety   *)
(* property: if pruning is ever unsafe, it is unsafe regardless of how    *)
(* long the retention window is.                                          *)
Prune(id) ==
  /\ phase[id] \in {"Deleted", "TimedOut"}
  /\ ~pruned[id]
  /\ (PruneRequiresChecked => checked[id])
  /\ pruned' = [pruned EXCEPT ![id] = TRUE]
  /\ UNCHANGED <<phase, githubReg, checked>>

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
    \/ Reconcile(id)
    \/ Prune(id)

Spec == Init /\ [][Next]_vars

(* The invariant a complete registration-reconciliation model must hold:  *)
(* an allocation must never sit in a terminal phase while GitHub still    *)
(* believes its runner is registered. A momentary violation immediately   *)
(* after an UnknownLeak step is expected and not itself the point (no     *)
(* single-transition patch can prevent an as-yet-unwritten bug from       *)
(* skipping deregistration) -- NoIrrecoverableOrphan below is the         *)
(* property that actually matters once EnableUnknownLeak is TRUE.         *)
NoOrphanedRegistration ==
  \A id \in AllocIDs :
    phase[id] \in {"Deleted", "TimedOut"} => githubReg[id] # "Registered"

(* The property that matters once an unknown leak is in play: local       *)
(* evidence must never be discarded (pruned) while GitHub still believes  *)
(* the runner is registered, because once pruned there is no way for      *)
(* anything in this system to ever discover or correct that divergence    *)
(* again -- this is finding #2 (the #161 GC fix compounding an            *)
(* undetected #162-class leak) made checkable.                            *)
NoIrrecoverableOrphan ==
  \A id \in AllocIDs :
    pruned[id] => githubReg[id] # "Registered"

====
