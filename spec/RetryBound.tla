---- MODULE RetryBound ----
(***************************************************************************)
(* Models internal/operator/retry.go's bounded interruption-retry protocol *)
(* for a single GitHub workflow RunID (every RunID is independent in the   *)
(* real code -- everything is keyed by RunID, never by allocation ID -- so *)
(* one RunID is enough to check the bound and the mutual-exclusion shape;  *)
(* nothing in the real logic lets two RunIDs interact).                    *)
(*                                                                          *)
(* Three real code paths follow a RerunFailedJobs POST: a synchronous       *)
(* success, a synchronous and definitive rejection (ErrRerunRejected -     *)
(* certainly did not land, budget untouched - modeled by neither variable  *)
(* changing, i.e. a stutter step already implied by [Next]_vars, so it has *)
(* no separate action here), or "ambiguous" (parks a pendingRerun, later   *)
(* resolved by reconcilePendingRerun once GitHub's own attempt-jobs        *)
(* evidence definitively confirms it either landed or never did).         *)
(*                                                                          *)
(* "At most one rerun request outstanding per RunID at a time" is not      *)
(* checked as a TLC invariant here: it holds by construction of `pending`  *)
(* being a single BOOLEAN (there is no state a second, concurrently        *)
(* outstanding request could occupy), the same way the real code's         *)
(* fleet.PendingReruns being a Go map keyed by RunID makes "at most one     *)
(* entry per RunID" true by the map's own data structure, not something    *)
(* worth a runtime check. An earlier draft of this file asserted           *)
(* `AtMostOneOutstandingRerun == TypeOK` and advertised it as a checked     *)
(* property - that was wrong: TypeOK's own `pending \in BOOLEAN` conjunct  *)
(* is trivially true for a BOOLEAN-typed variable, so TLC was proving       *)
(* nothing beyond what the type declaration already guarantees.            *)
(***************************************************************************)
EXTENDS Naturals

CONSTANTS MaxRetries
ASSUME MaxRetries \in 1..3

VARIABLES retriesUsed, pending

vars == <<retriesUsed, pending>>

TypeOK == retriesUsed \in 0..MaxRetries /\ pending \in BOOLEAN

Init == retriesUsed = 0 /\ pending = FALSE

(* recovery.Eligible's own guard: e.RetriesUsed < p.MaxRetries /\ ~e.RequestPending. *)
Eligible == retriesUsed < MaxRetries /\ ~pending

RequestSucceeds ==
  /\ Eligible
  /\ retriesUsed' = retriesUsed + 1
  /\ UNCHANGED pending

(* Transport failure after the POST: GitHub may have accepted it before    *)
(* the response was lost. Parked, never guessed either way.                *)
RequestAmbiguous ==
  /\ Eligible
  /\ pending' = TRUE
  /\ UNCHANGED retriesUsed

(* reconcilePendingRerun confirms, via GitHub's own next-attempt jobs, that *)
(* the ambiguous request did land -- counted exactly once, by assignment   *)
(* to the attempt number the request was made at (retriesUsed + 1 here),   *)
(* not by incrementing a second time on top of whatever RequestSucceeds    *)
(* already counted -- the real code's f.RetriesUsedByRun[id] = attempt is  *)
(* exactly this idempotent-by-assignment shape, mirrored here by the fact  *)
(* that only ONE of {RequestSucceeds, ReconcileConfirmedHappened} is ever  *)
(* reachable for the same request, gated by `pending`.                     *)
ReconcileConfirmedHappened ==
  /\ pending
  /\ retriesUsed' = retriesUsed + 1
  /\ pending' = FALSE

(* GitHub's own evidence proves the ambiguous request never actually       *)
(* landed -- the retry budget it might have spent was never really spent.  *)
ReconcileConfirmedDidNotHappen ==
  /\ pending
  /\ pending' = FALSE
  /\ UNCHANGED retriesUsed

Next ==
  \/ RequestSucceeds
  \/ RequestAmbiguous
  \/ ReconcileConfirmedHappened
  \/ ReconcileConfirmedDidNotHappen

Spec == Init /\ [][Next]_vars

(* The bound recovery.Policy.MaxRetries exists to enforce.                 *)
RetryBoundRespected == retriesUsed <= MaxRetries

====
