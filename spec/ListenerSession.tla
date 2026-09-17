---- MODULE ListenerSession ----
(***************************************************************************)
(* Models the scale-set listener's own demand-observation protocol         *)
(* (github.com/actions/scaleset's MessageSessionClient.GetMessage plus     *)
(* listener.Listener.Run, driving Operator.HandleDesiredRunnerCount via    *)
(* TotalAssignedJobs) - the one piece of runnerscout's state space every   *)
(* other module in this directory sits downstream of and none of them      *)
(* model. Confirmed in production: a "build" scale set stopped observing   *)
(* any GitHub demand at all, with no error anywhere in the controller's    *)
(* own logs, no CR-reactivity problem, and no admission bookkeeping        *)
(* problem - Tick and HandleDesiredRunnerCount behaved correctly on        *)
(* whatever demand they were told about, which was persistently zero. A    *)
(* suspend/resume cycle (forcing a fresh listener session) resolved it.    *)
(*                                                                          *)
(* Read directly from session_client.go's getMessage: a long-poll response *)
(* of 202 Accepted decodes to (nil, nil) - "no new message right now" -    *)
(* the exact same return value regardless of whether nothing is genuinely  *)
(* queued or the session has gone quietly orphaned on GitHub's own side.   *)
(* Only a 401 triggers this client's one built-in recovery path            *)
(* (refreshMessageSession, via GetMessage's own retry-once-on-expiry       *)
(* logic) - and a silently-orphaned session, by definition, does not       *)
(* return 401. That is not a bug in this codebase's own request handling;  *)
(* it is a real property of the protocol as implemented, made checkable    *)
(* here rather than left to be rediscovered by inference from ConfigMap    *)
(* state the next time it happens.                                        *)
(***************************************************************************)
EXTENDS Naturals

VARIABLES
  session,       \* "Fresh" or "Stale" - Stale is unobservable from any local signal
  demand,        \* BOOLEAN - GitHub genuinely has a queued job for this scale set
  acquired,      \* BOOLEAN - that job has been claimed via AcquireJobs
  totalAssignedJobs  \* the statistic HandleDesiredRunnerCount's count argument tracks

vars == <<session, demand, acquired, totalAssignedJobs>>

TypeOK ==
  /\ session \in {"Fresh", "Stale"}
  /\ demand \in BOOLEAN
  /\ acquired \in BOOLEAN
  /\ totalAssignedJobs \in Nat

Init ==
  /\ session = "Fresh"
  /\ demand = FALSE
  /\ acquired = FALSE
  /\ totalAssignedJobs = 0

(* A real GitHub-side event, entirely external to this listener: a workflow *)
(* job becomes queued and eligible for this scale set. acquired' = FALSE   *)
(* starts this job's own cycle unacquired, regardless of a prior job's     *)
(* outcome - an earlier version of this module left acquired unreset here, *)
(* which meant DeliverMessage's own ~acquired guard could never be         *)
(* satisfied again after the very first delivery, for the rest of any      *)
(* behavior: a second real job, arriving to a still-healthy Fresh session, *)
(* would sit undelivered forever with nothing to distinguish that state    *)
(* from the one this module exists to find, and NoSilentlyStrandedDemand   *)
(* would not catch it, since it only ever checks session = "Stale". A      *)
(* later review caught this; see README.background.md.                    *)
JobBecomesAvailable ==
  /\ ~demand
  /\ demand' = TRUE
  /\ acquired' = FALSE
  /\ UNCHANGED <<session, totalAssignedJobs>>

(* getMessage's 200 OK path: only reachable while session = Fresh, since a   *)
(* Stale session (by this module's own definition of the word) never        *)
(* delivers a real message - see the module header for why 202/nil covers   *)
(* both "nothing queued" and "orphaned" identically. *)
DeliverMessage ==
  /\ session = "Fresh"
  /\ demand
  /\ ~acquired
  /\ acquired' = TRUE
  /\ demand' = FALSE
  /\ totalAssignedJobs' = totalAssignedJobs + 1
  /\ UNCHANGED session

(* GitHub's own backend orphans this listener's session - no explicit       *)
(* signal reaches the client when this happens (that is the whole finding). *)
SessionGoesStale ==
  /\ session = "Fresh"
  /\ session' = "Stale"
  /\ UNCHANGED <<demand, acquired, totalAssignedJobs>>

(* The one recovery path confirmed in production: an operator-driven        *)
(* suspend/resume cycle (or an equivalent full listener restart), external  *)
(* to the listener's own protocol - nothing inside Listener.Run or          *)
(* MessageSessionClient.GetMessage does this on its own. *)
ForceFreshSession ==
  /\ session = "Stale"
  /\ session' = "Fresh"
  /\ UNCHANGED <<demand, acquired, totalAssignedJobs>>

Next == JobBecomesAvailable \/ DeliverMessage \/ SessionGoesStale \/ ForceFreshSession

Spec == Init /\ [][Next]_vars

(* The property that matters: once GitHub has real demand, it should not be *)
(* able to sit unacquired indefinitely while nothing in this module's own   *)
(* reachable states is stuck (deadlocked) - i.e. the session should not be  *)
(* able to remain silently orphaned with real demand pending. Expected to   *)
(* FAIL: SessionGoesStale then JobBecomesAvailable reaches exactly that     *)
(* state, and nothing in DeliverMessage's guard (session = "Fresh") can     *)
(* fire again without ForceFreshSession - an action this protocol has no    *)
(* internal trigger for. The counterexample is the production incident's   *)
(* own shape, made concrete. *)
NoSilentlyStrandedDemand == ~(session = "Stale" /\ demand /\ ~acquired)

====
