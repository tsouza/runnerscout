package operator

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/githubjobs"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/recovery"
	"github.com/tsouza/runnerscout/internal/state"
	"k8s.io/client-go/kubernetes/fake"
)

// attemptResponse overrides fakeGitHubJobs's AttemptJobs answer for one
// specific attempt number, so a test can make different attempts (e.g. the
// original attempt vs. a prospective rerun's attempt+1) behave differently -
// exactly what reconciliation needs to distinguish.
type attemptResponse struct {
	jobs []recovery.RESTJob
	err  error
}
type fakeGitHubJobs struct {
	jobs        []recovery.RESTJob
	fetchErr    error
	responses   map[int]attemptResponse
	reranOwner  string
	reranRepo   string
	reranRun    int64
	rerunCalled int
	rerunErr    error
	// hang, when set, makes AttemptJobs/RerunFailedJobs block until the
	// context they are called with is cancelled/expires - proving each
	// external call site in retry.go supplies its own bounded context
	// (externalCallBudget) rather than the bare ctx Tick itself was given,
	// mirroring fakeDeregistrar/fakeAzureInterruptions's own hang field.
	hang bool
}

func (f *fakeGitHubJobs) AttemptJobs(ctx context.Context, owner, repo string, runID int64, attempt int) ([]recovery.RESTJob, error) {
	if f.hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if r, ok := f.responses[attempt]; ok {
		return r.jobs, r.err
	}
	return f.jobs, f.fetchErr
}
func (f *fakeGitHubJobs) RerunFailedJobs(ctx context.Context, owner, repo string, runID int64) error {
	if f.hang {
		<-ctx.Done()
		return ctx.Err()
	}
	f.rerunCalled++
	f.reranOwner, f.reranRepo, f.reranRun = owner, repo, runID
	return f.rerunErr
}

func interruptedAllocation() lifecycle.Allocation {
	return lifecycle.Allocation{
		ID: "rs-test", Phase: lifecycle.Deleted, Condition: "ResourceAbsentConfirmedInterruption",
		Deadline: time.Now().Add(time.Minute), MaxAttempts: 3,
		RunID: 99, Owner: "acme", Repo: "widgets", ScaleSetJobID: "opaque-guid",
	}
}

// seedAllocation saves a with the fake clientset, then assigns it a
// non-empty resourceVersion the same way bumpResourceVersion does elsewhere -
// the fake clientset never assigns one on its own, which Kubernetes.Save's
// CAS otherwise needs to tell an update from a create.
func seedAllocation(t *testing.T, ctx context.Context, s *state.Kubernetes, a lifecycle.Allocation) {
	t.Helper()
	if _, e := s.Save(ctx, a, ""); e != nil {
		t.Fatal(e)
	}
	bumpResourceVersion(t, ctx, s, a.ID, "1")
}
func retryOperator(t *testing.T, gh *fakeGitHubJobs, policy recovery.Policy) (*Operator, *state.Kubernetes) {
	t.Helper()
	k := fake.NewClientset()
	s := &state.Kubernetes{Maps: k.CoreV1().ConfigMaps("test"), Owner: "test"}
	o := &Operator{Config: Config{Name: "test", Namespace: "test", Retry: policy}, Client: k, Store: s, GitHubJobs: gh}
	return o, s
}

func TestInterruptionRetryRerunsEligibleJobAndTracksCount(t *testing.T) {
	ctx := context.Background()
	gh := &fakeGitHubJobs{jobs: []recovery.RESTJob{{ID: 345, RunID: 99, Attempt: 1, RunnerName: "rs-test", Status: "completed", Conclusion: "failure"}}}
	policy := recovery.Policy{Enabled: true, MaxRetries: 2, AcknowledgeRepeatedEffects: true}
	o, s := retryOperator(t, gh, policy)
	seedAllocation(t, ctx, s, interruptedAllocation())
	if e := o.processInterruptionRetries(ctx); e != nil {
		t.Fatal(e)
	}
	if gh.rerunCalled != 1 || gh.reranOwner != "acme" || gh.reranRepo != "widgets" || gh.reranRun != 99 {
		t.Fatal("eligible interruption did not trigger a rerun", gh)
	}
	got, e := s.Load(ctx, "rs-test")
	if e != nil || !got.RetryProcessed {
		t.Fatal("allocation not marked processed", got, e)
	}
	_, f, e := o.loadFleet(ctx)
	if e != nil || f.RetriesUsedByRun[99] != 1 {
		t.Fatal("retry count not tracked by run ID", f, e)
	}
}
func TestInterruptionRetryDisabledPolicySkipsEntirely(t *testing.T) {
	ctx := context.Background()
	gh := &fakeGitHubJobs{jobs: []recovery.RESTJob{{ID: 345, RunID: 99, Attempt: 1, RunnerName: "rs-test", Status: "completed", Conclusion: "failure"}}}
	o, s := retryOperator(t, gh, recovery.Policy{})
	seedAllocation(t, ctx, s, interruptedAllocation())
	if e := o.processInterruptionRetries(ctx); e != nil {
		t.Fatal(e)
	}
	if gh.rerunCalled != 0 {
		t.Fatal("disabled policy triggered a rerun", gh)
	}
	got, e := s.Load(ctx, "rs-test")
	if e != nil || got.RetryProcessed {
		t.Fatal("disabled policy still marked the allocation processed", got, e)
	}
}
func TestInterruptionRetryIneligibleStillMarksProcessedOnce(t *testing.T) {
	ctx := context.Background()
	// No matching REST job for attempt 1: Eligible refuses.
	gh := &fakeGitHubJobs{jobs: nil}
	policy := recovery.Policy{Enabled: true, MaxRetries: 2, AcknowledgeRepeatedEffects: true}
	o, s := retryOperator(t, gh, policy)
	seedAllocation(t, ctx, s, interruptedAllocation())
	if e := o.processInterruptionRetries(ctx); e != nil {
		t.Fatal(e)
	}
	if gh.rerunCalled != 0 {
		t.Fatal("ineligible interruption triggered a rerun", gh)
	}
	got, e := s.Load(ctx, "rs-test")
	if e != nil || !got.RetryProcessed {
		t.Fatal("ineligible interruption was not marked processed", got, e)
	}
	// A second pass must not re-fetch or re-evaluate an already-processed allocation.
	gh.jobs = []recovery.RESTJob{{ID: 345, RunID: 99, Attempt: 1, RunnerName: "rs-test", Status: "completed", Conclusion: "failure"}}
	if e := o.processInterruptionRetries(ctx); e != nil {
		t.Fatal(e)
	}
	if gh.rerunCalled != 0 {
		t.Fatal("already-processed interruption was re-evaluated", gh)
	}
}
func TestInterruptionRetryIgnoresUnrelatedAllocations(t *testing.T) {
	ctx := context.Background()
	gh := &fakeGitHubJobs{}
	policy := recovery.Policy{Enabled: true, MaxRetries: 2, AcknowledgeRepeatedEffects: true}
	o, s := retryOperator(t, gh, policy)
	for _, mode := range []string{"wrong-phase", "wrong-condition", "no-run-id"} {
		a := interruptedAllocation()
		a.ID = "rs-" + mode
		switch mode {
		case "wrong-phase":
			a.Phase = lifecycle.Running
		case "wrong-condition":
			a.Condition = "CleanupConfirmed"
		case "no-run-id":
			a.RunID = 0
		}
		if _, e := s.Save(ctx, a, ""); e != nil {
			t.Fatal(e)
		}
	}
	if e := o.processInterruptionRetries(ctx); e != nil {
		t.Fatal(e)
	}
	if gh.rerunCalled != 0 {
		t.Fatal("unrelated allocations triggered a rerun", gh)
	}
}

// matchingTerminalJob is the one REST job whose identity/status combination
// satisfies recovery.Eligible for interruptedAllocation() at attempt 1.
var matchingTerminalJob = recovery.RESTJob{ID: 345, RunID: 99, Attempt: 1, RunnerName: "rs-test", Status: "completed", Conclusion: "failure"}

func TestInterruptionRetryAmbiguousRerunBlocksSecondAttemptUntilReconciled(t *testing.T) {
	ctx := context.Background()
	gh := &fakeGitHubJobs{jobs: []recovery.RESTJob{matchingTerminalJob}, rerunErr: errors.New("connection reset by peer")}
	policy := recovery.Policy{Enabled: true, MaxRetries: 2, AcknowledgeRepeatedEffects: true}
	o, s := retryOperator(t, gh, policy)
	seedAllocation(t, ctx, s, interruptedAllocation())

	if e := o.processInterruptionRetries(ctx); e != nil {
		t.Fatal(e)
	}
	if gh.rerunCalled != 1 {
		t.Fatal("expected exactly one rerun attempt", gh)
	}
	got, e := s.Load(ctx, "rs-test")
	if e != nil || got.RetryProcessed {
		t.Fatal("an ambiguous rerun outcome must not be marked processed", got, e)
	}
	_, f, e := o.loadFleet(ctx)
	if e != nil || f.RetriesUsedByRun[99] != 0 {
		t.Fatal("an ambiguous rerun outcome must not be counted as a used retry", f, e)
	}

	// A later pass, before reconciliation resolves anything (attempt+1
	// returns a plain ambiguous error, not a definitive "not found"): a
	// second rerun request for the same RunID must still be refused by
	// recovery.Eligible via RequestPending, not merely skipped.
	gh.responses = map[int]attemptResponse{2: {err: errors.New("still ambiguous")}}
	if e := o.processInterruptionRetries(ctx); e != nil {
		t.Fatal(e)
	}
	if gh.rerunCalled != 1 {
		t.Fatal("a second retry must not be requested while the first is unresolved", gh)
	}
}

func TestInterruptionRetryDefinitiveRejectionClosesOutImmediately(t *testing.T) {
	ctx := context.Background()
	gh := &fakeGitHubJobs{jobs: []recovery.RESTJob{matchingTerminalJob}, rerunErr: fmt.Errorf("%w: status 422", githubjobs.ErrRerunRejected)}
	policy := recovery.Policy{Enabled: true, MaxRetries: 2, AcknowledgeRepeatedEffects: true}
	o, s := retryOperator(t, gh, policy)
	seedAllocation(t, ctx, s, interruptedAllocation())

	if e := o.processInterruptionRetries(ctx); e != nil {
		t.Fatal(e)
	}
	if gh.rerunCalled != 1 {
		t.Fatal("expected exactly one rerun attempt", gh)
	}
	_, f, e := o.loadFleet(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := f.PendingReruns[99]; ok {
		t.Fatal("a definitive rejection must not be parked as ambiguous", f)
	}
	if f.RetriesUsedByRun[99] != 0 {
		t.Fatal("a definitively rejected rerun must not be counted as a used retry", f)
	}
	got, e := s.Load(ctx, "rs-test")
	if e != nil {
		t.Fatal(e)
	}
	// Unlike an ambiguous transport failure - which stays unresolved for
	// rerunReconciliationPolls passes before self-resolving - a definitive
	// rejection means GitHub already told us there is nothing to rerun, so
	// this interruption's evaluation is complete on the very first pass.
	if !got.RetryProcessed {
		t.Fatal("a definitive rejection must close out the interruption immediately, not wait on reconciliation", got)
	}

	// A closed-out allocation is never revisited: no further rerun request.
	if e := o.processInterruptionRetries(ctx); e != nil {
		t.Fatal(e)
	}
	if gh.rerunCalled != 1 {
		t.Fatal("a closed-out interruption must not be retried again", gh)
	}
}

func TestInterruptionRetryReconciliationConfirmsRerunHappened(t *testing.T) {
	ctx := context.Background()
	gh := &fakeGitHubJobs{jobs: []recovery.RESTJob{matchingTerminalJob}, rerunErr: errors.New("timeout")}
	policy := recovery.Policy{Enabled: true, MaxRetries: 2, AcknowledgeRepeatedEffects: true}
	o, s := retryOperator(t, gh, policy)
	seedAllocation(t, ctx, s, interruptedAllocation())

	if e := o.processInterruptionRetries(ctx); e != nil {
		t.Fatal(e)
	}

	// GitHub's rerun actually landed server-side despite the timeout: attempt
	// 2 now has jobs recorded.
	gh.responses = map[int]attemptResponse{2: {jobs: []recovery.RESTJob{{ID: 987, RunID: 99, Attempt: 2, RunnerName: "rs-test", Status: "queued"}}}}
	if e := o.processInterruptionRetries(ctx); e != nil {
		t.Fatal(e)
	}
	if gh.rerunCalled != 1 {
		t.Fatal("a confirmed rerun must not trigger a second request", gh)
	}
	got, e := s.Load(ctx, "rs-test")
	if e != nil || !got.RetryProcessed {
		t.Fatal("a confirmed rerun must close out the interruption", got, e)
	}
	_, f, e := o.loadFleet(ctx)
	if e != nil || f.RetriesUsedByRun[99] != 1 {
		t.Fatal("a confirmed rerun must count as the used retry", f, e)
	}
	if _, ok := f.PendingReruns[99]; ok {
		t.Fatal("pending state must be cleared once resolved", f)
	}
}

func TestInterruptionRetryReconciliationConfirmsRerunDidNotHappen(t *testing.T) {
	ctx := context.Background()
	gh := &fakeGitHubJobs{jobs: []recovery.RESTJob{matchingTerminalJob}, rerunErr: errors.New("connection reset by peer")}
	policy := recovery.Policy{Enabled: true, MaxRetries: 2, AcknowledgeRepeatedEffects: true}
	o, s := retryOperator(t, gh, policy)
	seedAllocation(t, ctx, s, interruptedAllocation())

	// Pass 1: the rerun request itself is ambiguous and gets parked.
	if e := o.processInterruptionRetries(ctx); e != nil {
		t.Fatal(e)
	}
	// GitHub definitively has no attempt 2 - it never created the rerun.
	// This same absence must be re-confirmed across several passes (the
	// original attempt's terminal job staying unchanged throughout) before
	// it counts as proof, rather than a single not-yet-materialized read.
	gh.responses = map[int]attemptResponse{2: {err: githubjobs.ErrAttemptNotFound}}
	for i := 0; i < rerunReconciliationPolls; i++ {
		if e := o.processInterruptionRetries(ctx); e != nil {
			t.Fatal(e)
		}
	}

	if gh.rerunCalled != 2 {
		t.Fatal("resolving 'did not happen' must allow a legitimate future retry attempt", gh)
	}
	_, f, e := o.loadFleet(ctx)
	if e != nil || f.RetriesUsedByRun[99] != 0 {
		t.Fatal("a rerun that never happened must never be counted as a used retry", f, e)
	}
	if _, ok := f.PendingReruns[99]; !ok {
		t.Fatal("the fresh retry attempt (itself still ambiguous) must be freshly parked", f)
	}
	got, e := s.Load(ctx, "rs-test")
	if e != nil || got.RetryProcessed {
		t.Fatal("the allocation remains unprocessed while the fresh retry is unresolved", got, e)
	}
}

// A real production incident: a leader pod stuck with idle CPU and no log
// line, because nothing bounded a sequential external call under Tick's own
// cancel-only ctx. internal/githubjobs.Client falls back to
// http.DefaultClient (Timeout: 0) when no HTTPClient is configured - exactly
// production's own construction (cmd/runnerscout/main.go never sets one) -
// so this call site had no fallback bound at all, unlike the scaleset
// client's own 5-minute default. This is the eighth such site, found only
// by auditing every remaining external call in the package after the first
// seven were already fixed and believed complete.
func TestProcessInterruptionRetriesBoundsAnUnresponsiveGitHubJobs(t *testing.T) {
	previous := externalCallBudget
	externalCallBudget = 100 * time.Millisecond
	defer func() { externalCallBudget = previous }()

	gh := &fakeGitHubJobs{hang: true}
	policy := recovery.Policy{Enabled: true, MaxRetries: 2, AcknowledgeRepeatedEffects: true}
	o, s := retryOperator(t, gh, policy)
	// ctx is cancel-only, deliberately with no deadline of its own -
	// matching Tick's own ctx in production. A test-owned deadline here
	// would let the test still pass even if processInterruptionRetries
	// stopped applying externalCallBudget itself.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seedAllocation(t, ctx, s, interruptedAllocation())

	done := make(chan error, 1)
	go func() { done <- o.processInterruptionRetries(ctx) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("processInterruptionRetries did not bound the stalled AttemptJobs call - it hung past externalCallBudget")
	}
}
