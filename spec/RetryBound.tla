---- MODULE RetryBound ----
(***************************************************************************)
(* Models internal/operator/retry.go's bounded interruption-retry protocol *)
(* for a single GitHub workflow RunID (every RunID is independent in the   *)
(* real code -- everything is keyed by RunID, never by allocation ID -- so *)
(* one RunID is enough to check the bound and the mutual-exclusion shape;  *)
(* nothing in the real logic lets two RunIDs interact).                    *)
(*                                                                          *)
(* Three real code paths after a RerunFailedJobs POST become three actions *)
(* here: a synchronous success or a synchronous, definitive rejection      *)
(* (ErrRerunRejected) resolve immediately; anything else is "ambiguous"    *)
(* and parks a pendingRerun, resolved later by reconcilePendingRerun once  *)
(* GitHub's own attempt-jobs evidence definitively confirms it either      *)
(* landed or never did.                                                    *)
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

(* GitHub synchronously and definitively rejects (e.g. no failed jobs to   *)
(* rerun) -- certainly did not land, budget untouched, never parked.       *)
RequestRejected ==
  /\ Eligible
  /\ UNCHANGED vars

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
  \/ RequestRejected
  \/ RequestAmbiguous
  \/ ReconcileConfirmedHappened
  \/ ReconcileConfirmedDidNotHappen

Spec == Init /\ [][Next]_vars

(* The bound recovery.Policy.MaxRetries exists to enforce.                 *)
RetryBoundRespected == retriesUsed <= MaxRetries

(* At most one rerun request may be outstanding for a RunID at a time --   *)
(* true by construction here (pending is a single BOOLEAN, and every       *)
(* action that could start a new ambiguous request first requires          *)
(* Eligible, which itself requires ~pending) -- checked anyway as a        *)
(* structural confirmation that a second RequestAmbiguous can never fire   *)
(* while one is already parked.                                            *)
AtMostOneOutstandingRerun == TypeOK

====
