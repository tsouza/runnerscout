---- MODULE BudgetAdmission ----
(***************************************************************************)
(* Models internal/operator/budget.go's admission-gating arithmetic         *)
(* (spentToday, the "affordable" check in HandleDesiredRunnerCount) and     *)
(* checks a reachable-state witness for research finding #6: spentToday     *)
(* excludes only TimedOut, never an early-interrupted Deleted allocation,   *)
(* so the daily ceiling can be "fully spent" by allocations that in         *)
(* reality cost almost nothing, refusing new admissions for the rest of     *)
(* the day while genuinely idle.                                           *)
(*                                                                          *)
(* This module does NOT attempt to model D5 (evicting a Running            *)
(* allocation when a cheaper offering appears): a meaningful check of       *)
(* whether a candidate eviction policy thrashes needs per-allocation        *)
(* identity (does the SAME slot get evicted repeatedly as prices keep       *)
(* moving?), not the aggregate counts this module uses -- an aggregate      *)
(* model cannot distinguish "many different allocations each evicted once"  *)
(* from "one allocation evicted over and over," so it cannot actually       *)
(* demonstrate or rule out thrashing. That remains open design work, not    *)
(* something this module resolves.                                         *)
(***************************************************************************)
EXTENDS Naturals

CONSTANTS
  MaxBudgetMicros,     \* the daily ceiling, in reservation units for simplicity
  ReservationMicros,   \* worst-case per-allocation reservation (uniform, one unit)
  MaxAdmitted          \* bound on admittedCount so the state space stays finite

ASSUME MaxBudgetMicros \in Nat
ASSUME ReservationMicros \in Nat \ {0}
ASSUME MaxAdmitted \in Nat \ {0}

VARIABLES
  admittedCount,     \* allocations admitted today
  timedOutCount,     \* of those, proven TimedOut (excluded from spend, matches budget.go)
  interruptedCount   \* of those, Deleted via early spot interruption (NOT excluded)

vars == <<admittedCount, timedOutCount, interruptedCount>>

TypeOK ==
  /\ admittedCount \in 0..MaxAdmitted
  /\ timedOutCount \in 0..MaxAdmitted
  /\ interruptedCount \in 0..MaxAdmitted
  /\ timedOutCount + interruptedCount <= admittedCount

Init ==
  /\ admittedCount = 0
  /\ timedOutCount = 0
  /\ interruptedCount = 0

(* budget.go's spentToday: sums the full worst-case reservation for every   *)
(* admitted-today allocation except a proven TimedOut one. An interrupted   *)
(* one still counts in full -- that asymmetry is the point being modeled.   *)
SpentToday == (admittedCount - timedOutCount) * ReservationMicros

(* HandleDesiredRunnerCount's own gate: admit only while the next unit's    *)
(* reservation still fits under the ceiling.                                *)
Admit ==
  /\ admittedCount < MaxAdmitted
  /\ SpentToday + ReservationMicros <= MaxBudgetMicros
  /\ admittedCount' = admittedCount + 1
  /\ UNCHANGED <<timedOutCount, interruptedCount>>

(* A Pending allocation exhausts its local retries before ever creating a   *)
(* real cloud resource -- lifecycle.Step proves this, which is exactly why  *)
(* budget.go excludes it.                                                   *)
BecomeTimedOut ==
  /\ timedOutCount + interruptedCount < admittedCount
  /\ timedOutCount' = timedOutCount + 1
  /\ UNCHANGED <<admittedCount, interruptedCount>>

(* A Running allocation is confirmed spot-interrupted -- real committed     *)
(* spend for it is now far below its worst-case reservation, but            *)
(* spentToday does not know that.                                          *)
BecomeInterrupted ==
  /\ timedOutCount + interruptedCount < admittedCount
  /\ interruptedCount' = interruptedCount + 1
  /\ UNCHANGED <<admittedCount, timedOutCount>>

Next == Admit \/ BecomeTimedOut \/ BecomeInterrupted

Spec == Init /\ [][Next]_vars

(* Safety: the ceiling itself is never exceeded. Confirms the gating        *)
(* arithmetic is sound.                                                    *)
BudgetNeverExceeded == SpentToday <= MaxBudgetMicros

(* Finding #6, made checkable: once every admitted allocation today has     *)
(* been interrupted (real utilization is zero), admission can still be     *)
(* fully refused for the rest of the day, because spentToday never         *)
(* discounts an interruption the way it discounts a TimedOut. Stated as an *)
(* invariant deliberately expected to FAIL -- TLC's counterexample is the  *)
(* witness that this trade-off is real and reachable, not hypothetical.    *)
NeverFullyLockedOutWhileIdle ==
  (admittedCount > 0 /\ interruptedCount = admittedCount) =>
    SpentToday < MaxBudgetMicros

====
