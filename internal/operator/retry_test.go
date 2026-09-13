package operator

import (
	"context"
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/recovery"
	"github.com/tsouza/runnerscout/internal/state"
	"k8s.io/client-go/kubernetes/fake"
)

type fakeGitHubJobs struct {
	jobs        []recovery.RESTJob
	fetchErr    error
	reranOwner  string
	reranRepo   string
	reranRun    int64
	rerunCalled int
	rerunErr    error
}

func (f *fakeGitHubJobs) AttemptJobs(ctx context.Context, owner, repo string, runID int64, attempt int) ([]recovery.RESTJob, error) {
	return f.jobs, f.fetchErr
}
func (f *fakeGitHubJobs) RerunFailedJobs(ctx context.Context, owner, repo string, runID int64) error {
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
