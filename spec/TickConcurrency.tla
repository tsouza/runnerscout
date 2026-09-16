---- MODULE TickConcurrency ----
(***************************************************************************)
(* Models Operator.Tick's own concurrency contract (operator.go): o.mu is  *)
(* held for Tick's whole duration, but each allocation's Step call runs on *)
(* its own goroutine underneath that same call. The safety property that  *)
(* actually matters is not "does mu provide mutual exclusion" (trivially   *)
(* true by construction of a mutex) but "can mu ever be released while a   *)
(* goroutine Tick itself spawned is still running" -- if so, a subsequent  *)
(* Tick or HandleDesiredRunnerCount can start concurrently with orphaned   *)
(* work still mutating shared state (e.g. Controller.Cooldowns), which is  *)
(* exactly PR #157's bug: a bare `return` inside the per-allocation loop   *)
(* (when an allocation had no f.Created entry) skipped every goroutine     *)
(* already spawned for earlier allocations in the same loop, releasing    *)
(* o.mu via Tick's own deferred Unlock while they were still running.      *)
(*                                                                        *)
(* EarlyReleaseEnabled = TRUE models the pre-#157 code (any goroutine       *)
(* outstanding at the moment of an early return remains unjoined).          *)
(* EarlyReleaseEnabled = FALSE models the current code (PR #157's fix):     *)
(* the missing-origin case is folded into `failures` and the loop           *)
(* continues, so wg.Wait() always runs on every path out of Tick.           *)
(***************************************************************************)
EXTENDS Naturals

CONSTANTS MaxInflight, EarlyReleaseEnabled

ASSUME MaxInflight \in Nat \ {0}
ASSUME EarlyReleaseEnabled \in BOOLEAN

VARIABLES muHeld, inflight

vars == <<muHeld, inflight>>

TypeOK == muHeld \in BOOLEAN /\ inflight \in 0..MaxInflight

Init == muHeld = FALSE /\ inflight = 0

TickStart ==
  /\ ~muHeld
  /\ muHeld' = TRUE
  /\ UNCHANGED inflight

(* One allocation's Step call is dispatched onto its own goroutine, still  *)
(* logically part of the Tick call that spawned it (o.mu is held), but no  *)
(* longer synchronous with it.                                             *)
Spawn ==
  /\ muHeld
  /\ inflight < MaxInflight
  /\ inflight' = inflight + 1
  /\ UNCHANGED muHeld

(* wg.Wait() progressively joining one already-finished goroutine.         *)
Join ==
  /\ muHeld
  /\ inflight > 0
  /\ inflight' = inflight - 1
  /\ UNCHANGED muHeld

(* The correct exit: Tick's deferred Unlock only actually runs after every *)
(* spawned goroutine has been joined.                                      *)
ReleaseNormally ==
  /\ muHeld
  /\ inflight = 0
  /\ muHeld' = FALSE
  /\ UNCHANGED inflight

(* PR #157's bug: an early `return` (e.g. on a missing f.Created origin)   *)
(* let Tick's deferred Unlock fire regardless of how many goroutines it    *)
(* had already spawned for earlier allocations in the same loop.           *)
ReleaseEarly ==
  /\ EarlyReleaseEnabled
  /\ muHeld
  /\ muHeld' = FALSE
  /\ UNCHANGED inflight

Next == TickStart \/ Spawn \/ Join \/ ReleaseNormally \/ ReleaseEarly

Spec == Init /\ [][Next]_vars

(* The property #157 actually violated: releasing o.mu is only safe once   *)
(* no goroutine Tick spawned is still outstanding, since nothing else      *)
(* stops a subsequent Tick/HandleDesiredRunnerCount from acquiring o.mu    *)
(* and mutating shared state a still-running Step call is also reading or  *)
(* writing (Controller.Cooldowns; two concurrent Step calls against the    *)
(* same still-Pending allocation, each able to reach a real Create call).  *)
MuReleasedOnlyWhenIdle == ~muHeld => inflight = 0

====
