---- MODULE ExternalFailureVisibility ----
(***************************************************************************)
(* Model of the diagnosability gap behind issue #176, kept in sync with   *)
(* lifecycle.Step's actual shape: a JIT preparation failure returns an    *)
(* allocation to Pending and records a Condition; a cloud-create failure  *)
(* leaves it in Creating and currently records only the pre-call          *)
(* "CreateCommitmentUnknown" sentinel.                                    *)
(*                                                                       *)
(* PreserveJITCause = FALSE reproduces #176 before #178: a failed JIT     *)
(* request records "CreatePreparationFailed" instead of "JITFailure".    *)
(* PreserveCloudCreateCause = FALSE is the current code: a failed cloud   *)
(* create records "CreateCommitmentUnknown", not "CloudCreateFailure".   *)
(* PreserveCloudCreateCause = TRUE is the extrapolated fix: the cloud     *)
(* failure's own cause becomes observable.                                *)
(*                                                                       *)
(* The module checks only the observable-cause safety property; it does   *)
(* not model retries, deadlines, or real HTTP status text. TLA+ uses      *)
(* distinct labels in place of the real error strings the Go code threads *)
(* through.                                                               *)
(***************************************************************************)
EXTENDS Naturals

CONSTANTS
  AllocIDs,
  PreserveJITCause,
  PreserveCloudCreateCause

ASSUME AllocIDs # {}
ASSUME PreserveJITCause \in BOOLEAN
ASSUME PreserveCloudCreateCause \in BOOLEAN

Phases == {"Pending", "Creating", "Provisioned"}
Conditions == {"None", "JITFailure", "CloudCreateFailure", "CreatePreparationFailed", "CreateCommitmentUnknown", "VMCreated"}
LastFailure == {"None", "JIT", "Cloud"}

VARIABLES phase, condition, lastFailure

vars == <<phase, condition, lastFailure>>

TypeOK ==
  /\ phase \in [AllocIDs -> Phases]
  /\ condition \in [AllocIDs -> Conditions]
  /\ lastFailure \in [AllocIDs -> LastFailure]

Init ==
  /\ phase = [id \in AllocIDs |-> "Pending"]
  /\ condition = [id \in AllocIDs |-> "None"]
  /\ lastFailure = [id \in AllocIDs |-> "None"]

(* JIT preparation succeeds. Step moves Pending -> Creating and records  *)
(* the pre-create commitment sentinel before any cloud effect.          *)
PrepSucceed(id) ==
  /\ phase[id] = "Pending"
  /\ phase' = [phase EXCEPT ![id] = "Creating"]
  /\ condition' = [condition EXCEPT ![id] = "CreateCommitmentUnknown"]
  /\ lastFailure' = [lastFailure EXCEPT ![id] = "None"]

(* JIT preparation fails before any cloud effect. Step returns to       *)
(* Pending and records either the real JIT cause or the pre-#178         *)
(* generic preparation sentinel.                                        *)
PrepFail(id) ==
  /\ phase[id] = "Pending"
  /\ phase' = [phase EXCEPT ![id] = "Pending"]
  /\ condition' = [condition EXCEPT ![id] =
       IF PreserveJITCause THEN "JITFailure" ELSE "CreatePreparationFailed"]
  /\ lastFailure' = [lastFailure EXCEPT ![id] = "JIT"]

CloudSucceed(id) ==
  /\ phase[id] = "Creating"
  /\ phase' = [phase EXCEPT ![id] = "Provisioned"]
  /\ condition' = [condition EXCEPT ![id] = "VMCreated"]
  /\ lastFailure' = [lastFailure EXCEPT ![id] = "None"]

(* The current code leaves a cloud-create failure in Creating with the   *)
(* same pre-call "CreateCommitmentUnknown" sentinel. The extrapolated    *)
(* fix would record "CloudCreateFailure" instead.                        *)
CloudFail(id) ==
  /\ phase[id] = "Creating"
  /\ phase' = [phase EXCEPT ![id] = "Creating"]
  /\ condition' = [condition EXCEPT ![id] =
       IF PreserveCloudCreateCause THEN "CloudCreateFailure" ELSE "CreateCommitmentUnknown"]
  /\ lastFailure' = [lastFailure EXCEPT ![id] = "Cloud"]

Next ==
  \E id \in AllocIDs :
    \/ PrepSucceed(id)
    \/ PrepFail(id)
    \/ CloudSucceed(id)
    \/ CloudFail(id)

Spec == Init /\ [][Next]_vars

(* The observable Condition must name the subsystem that actually failed, *)
(* never collapse an external failure to a sentinel that hides it.       *)
FailureCauseVisible ==
  \A id \in AllocIDs :
    /\ lastFailure[id] = "JIT" => condition[id] = "JITFailure"
    /\ lastFailure[id] = "Cloud" => condition[id] = "CloudCreateFailure"

====
