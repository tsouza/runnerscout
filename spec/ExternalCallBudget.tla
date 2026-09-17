---- MODULE ExternalCallBudget ----
(***************************************************************************)
(* Models the class of bug behind a real production incident: a leader pod *)
(* that acquired its lease, then made no further progress at all - no log  *)
(* line, idle CPU, readyz stuck at 503 - for 49+ minutes, reproduced       *)
(* identically on restart. Root cause: leaderCtx (runLeader's own ctx,     *)
(* from WithLease's context.WithCancel) is cancel-only, no deadline - and  *)
(* lease RENEWAL is a separate goroutine inside client-go's own            *)
(* leaderelection package that keeps succeeding as long as the Kubernetes  *)
(* API is reachable, entirely independent of external (GitHub/cloud)       *)
(* connectivity. So a stalled external call, anywhere reachable from that  *)
(* ctx without its own per-call timeout, is never cancelled by anything    *)
(* upstream - it hangs forever, and so does everything sequenced after it. *)
(*                                                                          *)
(* Seven real call sites in this package make an external call outside     *)
(* Step's own already-bounded per-allocation goroutine (tickStepBudget/     *)
(* tickCreateOrDeleteBudget already wrap every Step call, so those are not *)
(* modeled here - this module covers only what those two budgets do not).  *)
(* CallSites enumerates them exactly: runLeader's own GetRunnerScaleSetByID *)
(* and MessageSessionClient calls (the two steps of establishing GitHub    *)
(* connectivity before the listener can even start), the three Observe     *)
(* loops in prices.go (refreshAWSPrices/refreshAzurePrices/refreshGCPPrices, *)
(* one call site each despite the shared shape - each is an independent    *)
(* function with its own nil-gate), pruneTerminalAllocations's own         *)
(* DeregisterRunner loop, and pollAzureInterruptions's single Poll call     *)
(* (azure_interruptions.go) - found only by auditing every external call   *)
(* in this package after the first six were fixed, not by the incident     *)
(* report itself, which named only the two runLeader steps. That the first *)
(* version of this module stopped at six - after already stating its whole *)
(* purpose was extrapolating past the one incident-named pair - is exactly *)
(* the same shape of miss AdmissionSlot.tla's own background section       *)
(* documents: a completeness claim ("enumerates them exactly") that had    *)
(* not actually been checked against the rest of the codebase. It has now. *)
(* BoundedCallSites is the one constant that changes between configs,      *)
(* standing in for whether externalCallBudget (or githubStartupBudget, for *)
(* the two runLeader steps) actually wraps that call in the real code.     *)
(***************************************************************************)
EXTENDS Naturals

CallSites == {"ScaleSetLookup", "SessionEstablish", "PruneDeregister",
               "AWSPriceRefresh", "AzurePriceRefresh", "GCPPriceRefresh",
               "AzureInterruptionPoll"}

CONSTANT BoundedCallSites
ASSUME BoundedCallSites \subseteq CallSites

VARIABLES
  stalledSite,  \* the call site currently stalled, or "None"
  stuck         \* TRUE once a stall at an unbounded site has been reached

vars == <<stalledSite, stuck>>

TypeOK ==
  /\ stalledSite \in CallSites \cup {"None"}
  /\ stuck \in BOOLEAN

Init ==
  /\ stalledSite = "None"
  /\ stuck = FALSE

(* External connectivity stalls at some call site - a silently dropped     *)
(* connection, no RST, no data, no error: the exact shape confirmed by     *)
(* reproducing the incident, not merely hypothesized. A site with its own  *)
(* timeout recovers on its own (Recover below); a site with none never     *)
(* returns control to its caller at all, which is what "stuck" means here. *)
CallSiteStalls(site) ==
  /\ stalledSite = "None"
  /\ stalledSite' = site
  /\ stuck' = (site \notin BoundedCallSites)

(* A bounded site's own context deadline fires, the call returns (an       *)
(* error, not a value), and whatever sequence it was part of continues -   *)
(* Kubernetes restarts the pod on a fatal error, or the next allocation in  *)
(* a loop is still reached. Not available once stuck: nothing recovers a   *)
(* call with no timeout of its own. *)
Recover ==
  /\ stalledSite # "None"
  /\ ~stuck
  /\ stalledSite' = "None"
  /\ UNCHANGED stuck

Next == (\E site \in CallSites: CallSiteStalls(site)) \/ Recover

Spec == Init /\ [][Next]_vars

(* The property the incident violated: reconciliation must never become    *)
(* permanently stuck just because one external call stalled. Checked       *)
(* against BoundedCallSites = {} (the pre-fix shape - every one of these    *)
(* seven calls used the bare, deadline-less ctx) and against                *)
(* BoundedCallSites = CallSites (the shipped fix - every one of them is now *)
(* wrapped in its own externalCallBudget/githubStartupBudget context).      *)
NeverPermanentlyStuck == ~stuck

====
