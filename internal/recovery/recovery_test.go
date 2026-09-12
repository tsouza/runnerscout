package recovery

import "testing"

func valid() Evidence {
	return Evidence{ScaleSetJobID: "opaque-guid", RunnerName: "rs-a", RunID: 99, Attempt: 2, ProviderInterrupted: true, Jobs: []RESTJob{{ID: 345, RunID: 99, Attempt: 2, RunnerName: "rs-a", Status: "completed", Conclusion: "failure"}}}
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
