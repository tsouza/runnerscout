---- MODULE ExternalFailureVisibility ----
(***************************************************************************)
(* Model of the diagnosability gap behind issue #176: an external         *)
(* preparation failure (GitHub JIT-config generation) was swallowed at    *)
(* several layers, so an allocation's observable Condition pointed at    *)
(* generic create preparation instead of the subsystem that actually     *)
(* failed. The same shape applies to any external side effect whose      *)
(* failure is collapsed to a fixed sentinel, so this module models two   *)
(* representatives: the JIT prep call and a cloud create call.           *)
(*                                                                       *)
(* PreserveJITCause = FALSE reproduces #176 before #178: a failed JIT    *)
(* request records "CreatePreparationFailed" rather than "JITFailure".  *)
(* PreserveCloudCause = FALSE is the extrapolated sibling: a failed      *)
(* cloud create records the same generic sentinel rather than            *)
(* "CloudCreateFailure".                                                  *)
(*                                                                       *)
(* The module checks only the observable-cause safety property; it does  *)
(* not model retry, deadlines, or the real HTTP status text. TLA+        *)
(* distinguishes the failure classes as labels, standing in for the      *)
(* real error text the Go code now threads through.                      *)
(***************************************************************************)
EXTENDS Naturals

CONSTANTS
  AllocIDs,
  PreserveJITCause,
  PreserveCloudCause

ASSUME AllocIDs # {}
ASSUME PreserveJITCause \in BOOLEAN
ASSUME PreserveCloudCause \in BOOLEAN

Phases == {"Ready", "CloudIssued", "PrepFailed", "CloudFailed", "Provisioned"}
Conditions == {"None", "JITFailure", "CloudCreateFailure", "CreatePreparationFailed"}

VARIABLES phase, condition

vars == <<phase, condition>>

TypeOK ==
  /\ phase \in [AllocIDs -> Phases]
  /\ condition \in [AllocIDs -> Conditions]

Init ==
  /\ phase = [id \in AllocIDs |-> "Ready"]
  /\ condition = [id \in AllocIDs |-> "None"]

(* JIT preparation succeeds; the allocation can proceed to cloud create. *)
PrepSucceed(id) ==
  /\ phase[id] = "Ready"
  /\ phase' = [phase EXCEPT ![id] = "CloudIssued"]
  /\ UNCHANGED condition

(* JIT preparation fails before any cloud effect. The observable cause   *)
(* is preserved or swallowed according to PreserveJITCause.              *)
PrepFail(id) ==
  /\ phase[id] = "Ready"
  /\ phase' = [phase EXCEPT ![id] = "PrepFailed"]
  /\ condition' = [condition EXCEPT ![id] =
       IF PreserveJITCause THEN "JITFailure" ELSE "CreatePreparationFailed"]

CloudSucceed(id) ==
  /\ phase[id] = "CloudIssued"
  /\ phase' = [phase EXCEPT ![id] = "Provisioned"]
  /\ UNCHANGED condition

(* The extrapolated sibling: a real cloud create failure is either        *)
(* preserved as CloudCreateFailure or collapsed to the same generic      *)
(* preparation sentinel that hid the JIT failure in #176.                *)
CloudFail(id) ==
  /\ phase[id] = "CloudIssued"
  /\ phase' = [phase EXCEPT ![id] = "CloudFailed"]
  /\ condition' = [condition EXCEPT ![id] =
       IF PreserveCloudCause THEN "CloudCreateFailure" ELSE "CreatePreparationFailed"]

Next ==
  \E id \in AllocIDs :
    \/ PrepSucceed(id)
    \/ PrepFail(id)
    \/ CloudSucceed(id)
    \/ CloudFail(id)

Spec == Init /\ [][Next]_vars

(* The observable cause must name the subsystem that actually failed,    *)
(* never collapse an external failure to the generic preparation         *)
(* sentinel. This is exactly the property #176 showed was absent.        *)
FailureCauseVisible ==
  \A id \in AllocIDs :
    /\ phase[id] = "PrepFailed" => condition[id] = "JITFailure"
    /\ phase[id] = "CloudFailed" => condition[id] = "CloudCreateFailure"

====
