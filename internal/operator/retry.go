package operator

import (
	"context"
	"errors"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/recovery"
)

// githubJobsClient is the REST surface internal/githubjobs.Client provides;
// declared locally so tests can substitute a fake without importing it.
type githubJobsClient interface {
	AttemptJobs(ctx context.Context, owner, repo string, runID int64, attempt int) ([]recovery.RESTJob, error)
	RerunFailedJobs(ctx context.Context, owner, repo string, runID int64) error
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
		retriesUsed := f.RetriesUsedByRun[a.RunID]
		attempt := retriesUsed + 1
		jobs, fetchErr := o.GitHubJobs.AttemptJobs(ctx, a.Owner, a.Repo, a.RunID, attempt)
		if fetchErr == nil {
			evidence := recovery.Evidence{ScaleSetJobID: a.ScaleSetJobID, RunnerName: a.ID, RunID: a.RunID, Attempt: attempt, ProviderInterrupted: true, Jobs: jobs, RetriesUsed: retriesUsed}
			if _, eligErr := recovery.Eligible(o.Config.Retry, evidence); eligErr == nil {
				if rerunErr := o.GitHubJobs.RerunFailedJobs(ctx, a.Owner, a.Repo, a.RunID); rerunErr == nil {
					f.RetriesUsedByRun[a.RunID] = attempt
				}
			}
		}
		a.RetryProcessed = true
		if _, saveErr := o.Store.Save(ctx, a, a.Revision); saveErr != nil && !errors.Is(saveErr, lifecycle.ErrConflict) {
			failures = append(failures, saveErr)
		}
	}
	if e := o.saveFleet(ctx, cm, f); e != nil {
		failures = append(failures, e)
	}
	return errors.Join(failures...)
}
