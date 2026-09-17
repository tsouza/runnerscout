---- MODULE JITRequest ----
(***************************************************************************)
(* Model of the GitHub JIT-config request spacing added for issue #176:   *)
(* GenerateJitRunnerConfig is a shared external side effect, regardless   *)
(* of which cloud provider an allocation will later target. A rate        *)
(* limiter with burst 1 spaces successive Bootstrap calls apart, so a     *)
(* concurrently-admitted batch cannot fire several requests in the same  *)
(* instant.                                                              *)
(*                                                                       *)
(* SpacingEnabled = FALSE models the code as it shipped before the        *)
(* mitigation: every allocation may issue a JIT request immediately.     *)
(* SpacingEnabled = TRUE models the mitigation: a request consumes a     *)
(* single token, and JITAdvance recharges it, standing in for the        *)
(* interval passing.                                                     *)
(*                                                                       *)
(* HONESTY NOTE: under SpacingEnabled=TRUE, NoJitBurst is a restatement  *)
(* of the token guard that produces it -- the same "sanity check, not an *)
(* independent discovery" shape documented for TickConcurrency.tla and  *)
(* BudgetAdmission.tla. The result that carries signal is the disabled   *)
(* config's counterexample: it reaches two JIT requests started in the   *)
(* same clock slot.                                                      *)
(***************************************************************************)
EXTENDS Naturals

CONSTANTS
  AllocIDs,
  MaxAttempts,
  MaxClock,
  SpacingEnabled,
  EnableWaitCancellation

ASSUME AllocIDs # {}
ASSUME MaxAttempts \in Nat
ASSUME MaxClock \in Nat
ASSUME SpacingEnabled \in BOOLEAN
ASSUME EnableWaitCancellation \in BOOLEAN

Phases == {"Pending", "JITIssued", "Provisioned", "TimedOut"}

VARIABLES phase, started, jitToken, clock, attempts

vars == <<phase, started, jitToken, clock, attempts>>

TypeOK ==
  /\ phase \in [AllocIDs -> Phases]
  /\ started \in [AllocIDs -> 0..MaxClock]
  /\ jitToken \in 0..1
  /\ clock \in 0..MaxClock
  /\ attempts \in [AllocIDs -> 0..MaxAttempts]

Init ==
  /\ phase = [id \in AllocIDs |-> "Pending"]
  /\ started = [id \in AllocIDs |-> 0]
  /\ jitToken = 1
  /\ clock = 0
  /\ attempts = [id \in AllocIDs |-> 0]

(* A cancelled/invalidated context fails the limiter wait before a JIT    *)
(* request is ever issued; the allocation stays Pending and retries.      *)
WaitCanceled(id) ==
  /\ EnableWaitCancellation
  /\ phase[id] = "Pending"
  /\ attempts[id] < MaxAttempts
  /\ phase' = phase
  /\ attempts' = [attempts EXCEPT ![id] = attempts[id] + 1]
  /\ UNCHANGED <<started, jitToken, clock>>

(* Retry exhaustion sends the allocation to the terminal timeout phase.  *)
Timeout(id) ==
  /\ phase[id] = "Pending"
  /\ attempts[id] >= MaxAttempts
  /\ phase' = [phase EXCEPT ![id] = "TimedOut"]
  /\ UNCHANGED <<started, jitToken, clock, attempts>>

(* A Bootstrap call begins. When spacing is enabled, it requires the    *)
(* single token and consumes it; when disabled, it ignores the token    *)
(* entirely, so an arbitrary batch can begin in the same clock slot.    *)
JITRequest(id) ==
  /\ phase[id] = "Pending"
  /\ attempts[id] < MaxAttempts
  /\ (SpacingEnabled => jitToken = 1)
  /\ phase' = [phase EXCEPT ![id] = "JITIssued"]
  /\ started' = [started EXCEPT ![id] = clock]
  /\ jitToken' = IF SpacingEnabled THEN 0 ELSE jitToken
  /\ UNCHANGED <<clock, attempts>>

(* The spacing interval elapses and the limiter refills one token.      *)
JITAdvance ==
  /\ SpacingEnabled
  /\ jitToken = 0
  /\ clock < MaxClock
  /\ clock' = clock + 1
  /\ jitToken' = 1
  /\ UNCHANGED <<phase, started, attempts>>

JITSucceed(id) ==
  /\ phase[id] = "JITIssued"
  /\ phase' = [phase EXCEPT ![id] = "Provisioned"]
  /\ UNCHANGED <<started, jitToken, clock, attempts>>

JITFail(id) ==
  /\ phase[id] = "JITIssued"
  /\ phase' = [phase EXCEPT ![id] = "Pending"]
  /\ attempts' = [attempts EXCEPT ![id] = attempts[id] + 1]
  /\ UNCHANGED <<started, jitToken, clock>>

Next ==
  \/ JITAdvance
  \/ \E id \in AllocIDs :
       \/ WaitCanceled(id)
       \/ Timeout(id)
       \/ JITRequest(id)
       \/ JITSucceed(id)
       \/ JITFail(id)

Spec == Init /\ [][Next]_vars

(* No two allocations may have a JIT request in flight that started in  *)
(* the same clock slot. Two requests started in different slots (i.e.   *)
(* after JITAdvance) are allowed, matching a rate limiter's behavior.   *)
NoJitBurst ==
  \A i \in AllocIDs, j \in AllocIDs :
    i # j /\ phase[i] = "JITIssued" /\ phase[j] = "JITIssued"
      => started[i] # started[j]

====
