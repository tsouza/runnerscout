package recovery

import "testing"

func valid() Evidence {
	return Evidence{ScaleSetJobID: "opaque-guid", RunnerName: "rs-a", RunID: 99, Attempt: 2, ProviderInterrupted: true, Jobs: []RESTJob{{ID: 345, RunID: 99, Attempt: 2, RunnerName: "rs-a", Status: "completed", Conclusion: "failure"}}}
}

// validWithSibling adds a second, already-succeeded job from the same run:
// GitHub's rerun-failed-jobs is run-scoped and also reruns needs:-dependent
// jobs like this one, which is what AcknowledgeRepeatedEffects gates.
func validWithSibling() Evidence {
	e := valid()
	e.Jobs = append(e.Jobs, RESTJob{ID: 678, RunID: 99, Attempt: 2, RunnerName: "rs-b", Status: "completed", Conclusion: "success"})
	return e
}

func TestMultiJobRunScopeAndAcknowledgeRequired(t *testing.T) {
	p := Policy{Enabled: true, MaxRetries: 1, AcknowledgeRepeatedEffects: true}
	got, e := Eligible(p, validWithSibling())
	if e != nil || got.ID != 345 {
		t.Fatal(got, e)
	}
	p.AcknowledgeRepeatedEffects = false
	if _, err := Eligible(p, validWithSibling()); err == nil {
		t.Fatal("admitted unsafe rerun despite sibling job in same run", validWithSibling())
	}
}

func TestDisabledAndExactRESTIdentity(t *testing.T) {
	if _, e := Eligible(Policy{}, valid()); e == nil {
		t.Fatal("default retry")
	}
	p := Policy{Enabled: true, MaxRetries: 1, AcknowledgeRepeatedEffects: true}
	got, e := Eligible(p, valid())
	if e != nil || got.ID != 345 {
		t.Fatal(got, e)
	}
	for _, mutate := range []func(*Evidence){func(e *Evidence) { e.Jobs = append(e.Jobs, e.Jobs[0]) }, func(e *Evidence) { e.ProviderInterrupted = false }, func(e *Evidence) { e.RequestPending = true }, func(e *Evidence) { e.RetriesUsed = 1 }, func(e *Evidence) { e.Jobs[0].Attempt = 1 }} {
		e := valid()
		mutate(&e)
		if _, err := Eligible(p, e); err == nil {
			t.Fatal("admitted unsafe rerun", e)
		}
	}
}
