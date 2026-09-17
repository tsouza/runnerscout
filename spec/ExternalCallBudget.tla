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
(* CallSites below lists every external call outside Step's own already-   *)
(* bounded per-allocation goroutine (tickStepBudget/tickCreateOrDeleteBudget *)
(* already wrap every Step call, so those are not modeled here) that this  *)
(* module's own author could find as of this module's last revision:       *)
(* runLeader's own GetRunnerScaleSetByID and MessageSessionClient calls,    *)
(* the three Observe loops in prices.go, pruneTerminalAllocations's own     *)
(* DeregisterRunner loop, pollAzureInterruptions's single Poll call, and    *)
(* retry.go's AttemptJobs/RerunFailedJobs calls. This list has already      *)
(* been wrong twice - first stopping at six sites while the module's own    *)
(* header claimed "enumerates them exactly" (a completeness claim that had  *)
(* not actually been checked against the rest of the codebase - see        *)
(* README.background.md), then again at seven after an eighth,             *)
(* retry.go's own GitHub REST calls, was found by an adversarial review     *)
(* specifically checking this module against the code rather than trusting *)
(* its own prior claim of completeness. Given that history, this comment    *)
(* deliberately does not assert this list is exhaustive now either - it is *)
(* the result of the most recent audit, not a proof no ninth site exists.   *)
(* BoundedCallSites is the one constant that changes between configs,      *)
(* standing in for whether externalCallBudget (or githubStartupBudget, for *)
(* the two runLeader steps) actually wraps that call in the real code.     *)
(***************************************************************************)
EXTENDS Naturals

CallSites == {"ScaleSetLookup", "SessionEstablish", "PruneDeregister",
               "AWSPriceRefresh", "AzurePriceRefresh", "GCPPriceRefresh",
               "AzureInterruptionPoll", "RetryAttemptJobs", "RetryRerunFailedJobs"}

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
(* calls used the bare, deadline-less ctx) and against                     *)
(* BoundedCallSites = CallSites (the shipped fix - every one of them is now *)
(* wrapped in its own externalCallBudget/githubStartupBudget context).      *)
(*                                                                          *)
(* HONESTY NOTE: this models one call, not a loop of them. Four of the real *)
(* sites (PruneDeregister, the three price refreshes, RetryAttemptJobs/     *)
(* RetryRerunFailedJobs) sit inside a per-item loop that re-arms a fresh    *)
(* budget every iteration - N stalled items cost up to N times the budget   *)
(* sequentially, not the single bounded delay this action implies. Clean    *)
(* here means "no single stall is permanent," not "a large batch recovers   *)
(* quickly" - see externalCallBudget's own comment (operator.go) for the    *)
(* real cost of that gap.                                                   *)
NeverPermanentlyStuck == ~stuck

====
