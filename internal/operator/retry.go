package operator

import (
	"context"
	"errors"

	"github.com/tsouza/runnerscout/internal/githubjobs"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/recovery"
)

// githubJobsClient is the REST surface internal/githubjobs.Client provides;
// declared locally so tests can substitute a fake without importing it.
type githubJobsClient interface {
	AttemptJobs(ctx context.Context, owner, repo string, runID int64, attempt int) ([]recovery.RESTJob, error)
	RerunFailedJobs(ctx context.Context, owner, repo string, runID int64) error
}

// pendingRerun records a RerunFailedJobs request whose outcome GitHub never
// confirmed (timeout, 5xx, connection reset after the POST may already have
// been accepted server-side). It lives in fleet.PendingReruns, keyed by
// RunID for the same reason RetriesUsedByRun is: a rerun keeps the same
// RunID but is reassigned to a brand-new, otherwise unrelated allocation, so
// the block on a second request must follow the RunID, not any one
// allocation. recovery.Evidence.RequestPending is how that block is
// enforced: Eligible already refuses whenever it is true.
type pendingRerun struct {
	// Attempt is the attempt number that was current (i.e. about to be
	// superseded) when the ambiguous request was made. A rerun that actually
	// lands always creates GitHub's next attempt at Attempt+1 - that is the
	// only real signal available to resolve the ambiguity later.
	Attempt int `json:"attempt"`
	// JobID is the REST ID of the terminal-failure job that justified the
	// request at Attempt. Reconciliation re-reads it to confirm the run's
	// own recorded history is unchanged before trusting an "attempt+1
	// doesn't exist" read as meaningful, rather than a stale coincidence.
	JobID int64 `json:"jobID"`
	// NotFoundPolls counts consecutive reconciliation passes where GitHub
	// definitively reported attempt+1 does not exist (ErrAttemptNotFound,
	// never a generic error) with JobID's terminal failure unchanged. See
	// rerunReconciliationPolls for why more than one poll is required.
	NotFoundPolls int `json:"notFoundPolls,omitempty"`
}

// rerunReconciliationPolls is how many consecutive reconciliation passes must
// agree - GitHub reporting no such attempt, with the original terminal
// failure unchanged - before that absence is treated as proof the ambiguous
// rerun request never landed. A single such read is not enough: GitHub can
// take a little time to materialize a newly accepted rerun's next attempt
// and its jobs, so one absent read only means "not yet materialized", not
// "never happened". This is a fixed internal reconciliation constant, not a
// user-facing knob - like Eligible's 1..3 MaxRetries bound, it is chosen
// once here rather than exposed for tuning.
const rerunReconciliationPolls = 3

// rerunOutcome is reconcilePendingRerun's classification of an ambiguous
// rerun request, using only GitHub's own REST attempt-jobs state. It is
// never a guess: rerunStillAmbiguous is returned whenever the evidence does
// not yet definitively support either of the other two outcomes.
type rerunOutcome int

const (
	rerunStillAmbiguous rerunOutcome = iota
	rerunConfirmedHappened
	rerunConfirmedDidNotHappen
)

// reconcilePendingRerun resolves one pendingRerun against GitHub's own
// attempt-jobs REST state - the only real signal available, since no new
// external API is introduced. A rerun that actually happened always creates
// a new attempt (pr.Attempt+1) with its own jobs, so their existence is
// definitive proof it landed regardless of what the original POST observed.
// GitHub reporting that attempt+1 does not exist (githubjobs.ErrAttemptNotFound,
// specifically - not just any error) is the one call whose absence is itself
// informative; even then it is only trusted once it recurs across
// rerunReconciliationPolls consecutive passes with the original failure
// evidence (JobID) read back unchanged, ruling out both "not yet
// materialized" and "the run's own history moved underneath us".
func (o *Operator) reconcilePendingRerun(ctx context.Context, owner, repo string, runID int64, pr pendingRerun) (rerunOutcome, pendingRerun) {
	nextJobs, nextErr := o.GitHubJobs.AttemptJobs(ctx, owner, repo, runID, pr.Attempt+1)
	if nextErr == nil && len(nextJobs) > 0 {
		return rerunConfirmedHappened, pr
	}
	if !errors.Is(nextErr, githubjobs.ErrAttemptNotFound) {
		// A transport error, or a 200 with no jobs recorded yet, tells us
		// nothing definitive either way. Stay parked; try again next pass.
		return rerunStillAmbiguous, pr
	}
	originalJobs, origErr := o.GitHubJobs.AttemptJobs(ctx, owner, repo, runID, pr.Attempt)
	if origErr != nil {
		return rerunStillAmbiguous, pr
	}
	for _, j := range originalJobs {
		if j.ID == pr.JobID && j.Status == "completed" && j.Conclusion == "failure" {
			pr.NotFoundPolls++
			if pr.NotFoundPolls >= rerunReconciliationPolls {
				return rerunConfirmedDidNotHappen, pr
			}
			return rerunStillAmbiguous, pr
		}
	}
	// The very evidence that justified the original request no longer reads
	// back the same way. Never guess from a moving picture: reset instead of
	// compounding an uncertain count against a run whose recorded state just
	// changed in some unexpected way.
	pr.NotFoundPolls = 0
	return rerunStillAmbiguous, pr
}

// processInterruptionRetries evaluates each allocation that reached Deleted
// via a confirmed provider interruption exactly once, requesting a bounded
// GitHub rerun when internal/recovery.Eligible admits it. A rerun keeps the
// same workflow run ID but is reassigned as a brand new allocation with no
// link back to this one, so the retry count it must respect is tracked here,
// keyed by RunID, never by allocation ID.
func (o *Operator) processInterruptionRetries(ctx context.Context) error {
	if o.GitHubJobs == nil || !o.Config.Retry.Enabled {
		return nil
	}
	allocs, e := o.Store.List(ctx)
	if e != nil {
		return e
	}
	var pending []lifecycle.Allocation
	for _, a := range allocs {
		if a.Phase == lifecycle.Deleted && a.Condition == "ResourceAbsentConfirmedInterruption" && !a.RetryProcessed && a.RunID != 0 {
			pending = append(pending, a)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	cm, f, e := o.loadFleet(ctx)
	if e != nil {
		return e
	}
	var failures []error
	for _, a := range pending {
		if pr, ok := f.PendingReruns[a.RunID]; ok {
			switch outcome, resolved := o.reconcilePendingRerun(ctx, a.Owner, a.Repo, a.RunID, pr); outcome {
			case rerunConfirmedHappened:
				// GitHub's own next attempt now has jobs: the earlier
				// ambiguous request did land. Count the retry it always
				// represented and close out this interruption for good - it
				// must never be re-evaluated as if unretried.
				f.RetriesUsedByRun[a.RunID] = resolved.Attempt
				delete(f.PendingReruns, a.RunID)
				a.RetryProcessed = true
				if _, saveErr := o.Store.Save(ctx, a, a.Revision); saveErr != nil && !errors.Is(saveErr, lifecycle.ErrConflict) {
					failures = append(failures, saveErr)
				}
				continue
			case rerunConfirmedDidNotHappen:
				// GitHub never created the rerun: the retry budget was never
				// actually spent. Clear the block and fall through below to
				// evaluate this interruption fresh, exactly as if the
				// original request had failed outright rather than gone
				// ambiguous - this is what "allows a legitimate future
				// retry" for a request that truly never landed.
				delete(f.PendingReruns, a.RunID)
			default:
				f.PendingReruns[a.RunID] = resolved
			}
		}
		retriesUsed := f.RetriesUsedByRun[a.RunID]
		attempt := retriesUsed + 1
		_, stillPending := f.PendingReruns[a.RunID]
		jobs, fetchErr := o.GitHubJobs.AttemptJobs(ctx, a.Owner, a.Repo, a.RunID, attempt)
		if fetchErr == nil {
			evidence := recovery.Evidence{ScaleSetJobID: a.ScaleSetJobID, RunnerName: a.ID, RunID: a.RunID, Attempt: attempt, ProviderInterrupted: true, Jobs: jobs, RetriesUsed: retriesUsed, RequestPending: stillPending}
			if j, eligErr := recovery.Eligible(o.Config.Retry, evidence); eligErr == nil {
				if rerunErr := o.GitHubJobs.RerunFailedJobs(ctx, a.Owner, a.Repo, a.RunID); rerunErr == nil {
					f.RetriesUsedByRun[a.RunID] = attempt
				} else {
					// Ambiguous: GitHub may have accepted this POST before
					// the transport failed. Guessing either way here would
					// violate this codebase's definitive-classification
					// discipline, so park it for reconciliation instead of
					// counting it as used or discarding it as failed.
					f.PendingReruns[a.RunID] = pendingRerun{Attempt: attempt, JobID: j.ID}
				}
			}
		}
		// RetryProcessed means this interruption's evaluation is complete.
		// That is never true while a rerun request for its RunID remains
		// unresolved - the next pass must revisit this same allocation.
		if _, stillPending := f.PendingReruns[a.RunID]; !stillPending {
			a.RetryProcessed = true
		}
		if _, saveErr := o.Store.Save(ctx, a, a.Revision); saveErr != nil && !errors.Is(saveErr, lifecycle.ErrConflict) {
			failures = append(failures, saveErr)
		}
	}
	if e := o.saveFleet(ctx, cm, f); e != nil {
		failures = append(failures, e)
	}
	return errors.Join(failures...)
}
