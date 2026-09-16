---- MODULE AdmissionSlot ----
(***************************************************************************)
(* Models internal/admission's per-allocation slot bookkeeping             *)
(* (admission.State.Admitted, fleet.Released) exactly as                   *)
(* HandleDesiredRunnerCount (operator.go) drives it: every terminal        *)
(* allocation (Deleted or TimedOut) is supposed to release the one         *)
(* Admitted slot it consumed, exactly once, via f.Released[id] as the      *)
(* idempotence guard. This is deliberately a different, narrower state     *)
(* machine than BudgetAdmission.tla in this same directory: that module    *)
(* models the aggregate spend ceiling (spentToday vs MaxBudgetMicros) and  *)
(* never represents Admitted/Released/per-allocation identity at all - it  *)
(* is a proxy for a different question. This module exists because that    *)
(* gap let issue #174 ship in v1.2.0 undetected by any of the other four   *)
(* modules here: a TimedOut allocation's slot was gated on the same        *)
(* zero-demand Cohort reset used for a full-fleet-idle rebaseline, so a    *)
(* batch of TimedOut allocations under sustained real demand (demand never *)
(* dropping to 0) permanently consumed their slots until a human deleted   *)
(* the fleet ConfigMap by hand.                                            *)
(*                                                                          *)
(* ReleasablePhases is the one constant that changes shape between the two *)
(* configs below, standing in for the one-line diff PR #175 actually       *)
(* shipped (operator.go's `a.Phase == lifecycle.Deleted` guard widened to  *)
(* `a.Phase == lifecycle.Deleted || a.Phase == lifecycle.TimedOut`).       *)
(*                                                                          *)
(* NOT modeled here, deliberately: the Cohort/ResetObserved full-fleet-    *)
(* idle reset (a separate, already-correct mechanism this bug never        *)
(* touched - see admission.go's own Reconcile), the budget-truncation      *)
(* adjustment in HandleDesiredRunnerCount (`Admitted -= n - affordable`,   *)
(* a same-call arithmetic correction with no persistence gap to model),    *)
(* and Pending-materialization ("represented" allocations counted as       *)
(* active before a Store record exists) - none of those paths gate a       *)
(* slot's release on which terminal phase produced it, which is the one    *)
(* axis this module isolates and checks.                                   *)
(***************************************************************************)
EXTENDS Naturals

CONSTANTS
  AllocIDs,          \* small finite set of allocation identities
  MaxAdmitted,        \* bound on admitted so the state space stays finite
  ReleasablePhases    \* the phases ReleaseSlot's own guard actually checks

ASSUME MaxAdmitted \in Nat \ {0}
ASSUME ReleasablePhases \subseteq {"Deleted", "TimedOut"}

VARIABLES
  phase,      \* [AllocIDs -> {"Unused", "Active", "Deleted", "TimedOut"}]
  admitted,   \* mirrors admission.State.Admitted
  released    \* [AllocIDs -> BOOLEAN], mirrors fleet.Released

vars == <<phase, admitted, released>>

TypeOK ==
  /\ phase \in [AllocIDs -> {"Unused", "Active", "Deleted", "TimedOut"}]
  /\ admitted \in 0..MaxAdmitted
  /\ released \in [AllocIDs -> BOOLEAN]

Init ==
  /\ phase = [id \in AllocIDs |-> "Unused"]
  /\ admitted = 0
  /\ released = [id \in AllocIDs |-> FALSE]

(* A fresh admission (admission.State.Reconcile granting a new slot) that   *)
(* starts an allocation's life. The real Reconcile's demand/active/limit    *)
(* arithmetic collapses here to a single MaxAdmitted ceiling - the release  *)
(* discipline this module checks does not depend on how the slot was       *)
(* granted, only on whether it is ever given back.                         *)
Claim(id) ==
  /\ phase[id] = "Unused"
  /\ admitted < MaxAdmitted
  /\ phase' = [phase EXCEPT ![id] = "Active"]
  /\ admitted' = admitted + 1
  /\ UNCHANGED released

(* lifecycle.Step reaching Deleted: cleanup confirmed, the cloud resource   *)
(* is provably gone (spot interruption, MaxLifetimeSeconds expiry, or a     *)
(* drain before pickup).                                                   *)
ToDeleted(id) ==
  /\ phase[id] = "Active"
  /\ phase' = [phase EXCEPT ![id] = "Deleted"]
  /\ UNCHANGED <<admitted, released>>

(* lifecycle.Step reaching TimedOut: the allocation never held cloud        *)
(* resources at all (Pending's own invariant), so it is just as provably    *)
(* inert as a Deleted one - the fact #174's fix rests on.                   *)
ToTimedOut(id) ==
  /\ phase[id] = "Active"
  /\ phase' = [phase EXCEPT ![id] = "TimedOut"]
  /\ UNCHANGED <<admitted, released>>

(* HandleDesiredRunnerCount's per-allocation release loop, run on every     *)
(* call regardless of demand: guarded only by phase membership and the      *)
(* idempotence flag, exactly matching the real code's shape (including the  *)
(* real code's own `if Admitted > 0` guard against decrementing below 0).  *)
ReleaseSlot(id) ==
  /\ phase[id] \in ReleasablePhases
  /\ ~released[id]
  /\ admitted' = IF admitted > 0 THEN admitted - 1 ELSE admitted
  /\ released' = [released EXCEPT ![id] = TRUE]
  /\ UNCHANGED phase

Next == \E id \in AllocIDs: Claim(id) \/ ToDeleted(id) \/ ToTimedOut(id) \/ ReleaseSlot(id)

Spec == Init /\ [][Next]_vars

(* The property #174 actually violated. Unlike BudgetNeverExceeded          *)
(* (BudgetAdmission.tla) or MuReleasedOnlyWhenIdle's fixed config           *)
(* (TickConcurrency.tla), this is not a restatement of ReleaseSlot's own    *)
(* guard: it compares ReleasablePhases (a model parameter standing in for   *)
(* what the code's release guard actually checks) against the full         *)
(* terminal set {"Deleted", "TimedOut"} (what "provably inert, should       *)
(* release" actually means) - two independently-parameterized sets that    *)
(* #174 let drift apart. Every terminal-but-unreleased allocation must      *)
(* have a real path back to a usable slot: either it is already released,  *)
(* or releasing it is presently possible.                                  *)
EveryTerminalIsReleasable ==
  \A id \in AllocIDs:
    phase[id] \in {"Deleted", "TimedOut"} => (released[id] \/ ENABLED ReleaseSlot(id))

====
