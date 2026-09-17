---- MODULE ExternalFailureVisibility ----
(***************************************************************************)
(* Model of the diagnosability gap behind issue #176, expanded across the *)
(* external side effects lifecycle.Step can hit: JIT preparation,        *)
(* WireGuard preparation, cloud create, cloud observe, and cloud delete. *)
(*                                                                       *)
(* Each Preserve* constant toggles whether that failure class records a  *)
(* class-specific Condition or collapses to the generic sentinel an      *)
(* earlier version of the code used. As of #182, the current code         *)
(* preserves every one of the five causes this module models - see        *)
(* ExternalFailureVisibility_post178_pre182.cfg for the real historical    *)
(* state between #178 (JIT only) and #182 (the remaining four), and        *)
(* ExternalFailureVisibility_post_all_fixes.cfg for what ships today.      *)
(*                                                                       *)
(* This module is deliberately broader than a step-for-step transcription *)
(* of Step; it keeps the phase ordering (Pending -> Creating -> Running   *)
(* -> Deleting -> Deleted) and the retry/timout shape, but abstracts      *)
(* real error text into distinct labels. See README.background.md.        *)
(***************************************************************************)
EXTENDS Naturals

CONSTANTS
  AllocIDs,
  MaxAttempts,
  EnableTimeout,
  PreserveJITCause,
  PreserveWireGuardCause,
  PreserveCloudCreateCause,
  PreserveObserveCause,
  PreserveDeleteCause

ASSUME AllocIDs # {}
ASSUME MaxAttempts \in Nat
ASSUME EnableTimeout \in BOOLEAN
ASSUME PreserveJITCause \in BOOLEAN
ASSUME PreserveWireGuardCause \in BOOLEAN
ASSUME PreserveCloudCreateCause \in BOOLEAN
ASSUME PreserveObserveCause \in BOOLEAN
ASSUME PreserveDeleteCause \in BOOLEAN

Phases == {"Pending", "Creating", "Running", "Deleting", "Deleted", "TimedOut"}
Conditions == {
  "None",
  "JITFailure",
  "WireGuardFailure",
  "CloudCreateFailure",
  "ObserveFailure",
  "DeleteFailure",
  "CreatePreparationFailed",
  "CreateCommitmentUnknown",
  "ObservationUnknown",
  "CleanupPending",
  "VMCreated",
  "CleanupConfirmed",
  "LocalProvisioningTimeout",
  "CapacityRejected",
  "ResourceAbsentInterruptionUnproven"
}
LastFailure == {"None", "JIT", "WireGuard", "CloudCreate", "Observe", "Delete"}

VARIABLES phase, attempts, condition, lastFailure

vars == <<phase, attempts, condition, lastFailure>>

TypeOK ==
  /\ phase \in [AllocIDs -> Phases]
  /\ attempts \in [AllocIDs -> 0..MaxAttempts]
  /\ condition \in [AllocIDs -> Conditions]
  /\ lastFailure \in [AllocIDs -> LastFailure]

Init ==
  /\ phase = [id \in AllocIDs |-> "Pending"]
  /\ attempts = [id \in AllocIDs |-> 0]
  /\ condition = [id \in AllocIDs |-> "None"]
  /\ lastFailure = [id \in AllocIDs |-> "None"]

Timeout(id) ==
  /\ phase[id] = "Pending"
  /\ EnableTimeout \/ attempts[id] >= MaxAttempts
  /\ phase' = [phase EXCEPT ![id] = "TimedOut"]
  /\ condition' = [condition EXCEPT ![id] = "LocalProvisioningTimeout"]
  /\ lastFailure' = [lastFailure EXCEPT ![id] = "None"]
  /\ UNCHANGED attempts

StartCreate(id) ==
  /\ phase[id] = "Pending"
  /\ attempts[id] < MaxAttempts
  /\ phase' = [phase EXCEPT ![id] = "Creating"]
  /\ attempts' = [attempts EXCEPT ![id] = attempts[id] + 1]
  /\ condition' = [condition EXCEPT ![id] = "CreateCommitmentUnknown"]
  /\ lastFailure' = [lastFailure EXCEPT ![id] = "None"]

JITFail(id) ==
  /\ phase[id] = "Creating"
  /\ phase' = [phase EXCEPT ![id] = "Pending"]
  /\ condition' = [condition EXCEPT ![id] =
       IF PreserveJITCause THEN "JITFailure" ELSE "CreatePreparationFailed"]
  /\ lastFailure' = [lastFailure EXCEPT ![id] = "JIT"]
  /\ UNCHANGED attempts

WireGuardFail(id) ==
  /\ phase[id] = "Creating"
  /\ phase' = [phase EXCEPT ![id] = "Creating"]
  /\ condition' = [condition EXCEPT ![id] =
       IF PreserveWireGuardCause THEN "WireGuardFailure" ELSE "CreateCommitmentUnknown"]
  /\ lastFailure' = [lastFailure EXCEPT ![id] = "WireGuard"]
  /\ UNCHANGED attempts

CloudCreateFail(id) ==
  /\ phase[id] = "Creating"
  /\ phase' = [phase EXCEPT ![id] = "Creating"]
  /\ condition' = [condition EXCEPT ![id] =
       IF PreserveCloudCreateCause THEN "CloudCreateFailure" ELSE "CreateCommitmentUnknown"]
  /\ lastFailure' = [lastFailure EXCEPT ![id] = "CloudCreate"]
  /\ UNCHANGED attempts

CapacityReject(id) ==
  /\ phase[id] = "Creating"
  /\ phase' = [phase EXCEPT ![id] = "Pending"]
  /\ condition' = [condition EXCEPT ![id] = "CapacityRejected"]
  /\ lastFailure' = [lastFailure EXCEPT ![id] = "None"]
  /\ UNCHANGED attempts

CloudCreateSucceed(id) ==
  /\ phase[id] = "Creating"
  /\ phase' = [phase EXCEPT ![id] = "Running"]
  /\ condition' = [condition EXCEPT ![id] = "VMCreated"]
  /\ lastFailure' = [lastFailure EXCEPT ![id] = "None"]
  /\ UNCHANGED attempts

ObserveFail(id) ==
  /\ phase[id] = "Running"
  /\ phase' = [phase EXCEPT ![id] = "Running"]
  /\ condition' = [condition EXCEPT ![id] =
       IF PreserveObserveCause THEN "ObserveFailure" ELSE "ObservationUnknown"]
  /\ lastFailure' = [lastFailure EXCEPT ![id] = "Observe"]
  /\ UNCHANGED attempts

ObserveAbsent(id) ==
  /\ phase[id] = "Running"
  /\ phase' = [phase EXCEPT ![id] = "Deleted"]
  /\ condition' = [condition EXCEPT ![id] = "ResourceAbsentInterruptionUnproven"]
  /\ lastFailure' = [lastFailure EXCEPT ![id] = "None"]
  /\ UNCHANGED attempts

Retire(id) ==
  /\ phase[id] = "Running"
  /\ phase' = [phase EXCEPT ![id] = "Deleting"]
  /\ condition' = [condition EXCEPT ![id] = "CleanupPending"]
  /\ lastFailure' = [lastFailure EXCEPT ![id] = "None"]
  /\ UNCHANGED attempts

DeleteFail(id) ==
  /\ phase[id] = "Deleting"
  /\ phase' = [phase EXCEPT ![id] = "Deleting"]
  /\ condition' = [condition EXCEPT ![id] =
       IF PreserveDeleteCause THEN "DeleteFailure" ELSE "CleanupPending"]
  /\ lastFailure' = [lastFailure EXCEPT ![id] = "Delete"]
  /\ UNCHANGED attempts

DeleteSucceed(id) ==
  /\ phase[id] = "Deleting"
  /\ phase' = [phase EXCEPT ![id] = "Deleted"]
  /\ condition' = [condition EXCEPT ![id] = "CleanupConfirmed"]
  /\ lastFailure' = [lastFailure EXCEPT ![id] = "None"]
  /\ UNCHANGED attempts

Next ==
  \E id \in AllocIDs :
    \/ Timeout(id)
    \/ StartCreate(id)
    \/ JITFail(id)
    \/ WireGuardFail(id)
    \/ CloudCreateFail(id)
    \/ CapacityReject(id)
    \/ CloudCreateSucceed(id)
    \/ ObserveFail(id)
    \/ ObserveAbsent(id)
    \/ Retire(id)
    \/ DeleteFail(id)
    \/ DeleteSucceed(id)

Spec == Init /\ [][Next]_vars

(* The observable Condition must name the subsystem that actually failed, *)
(* never collapse an external failure to a generic sentinel.             *)
FailureCauseVisible ==
  \A id \in AllocIDs :
    /\ lastFailure[id] = "JIT" => condition[id] = "JITFailure"
    /\ lastFailure[id] = "WireGuard" => condition[id] = "WireGuardFailure"
    /\ lastFailure[id] = "CloudCreate" => condition[id] = "CloudCreateFailure"
    /\ lastFailure[id] = "Observe" => condition[id] = "ObserveFailure"
    /\ lastFailure[id] = "Delete" => condition[id] = "DeleteFailure"

====
