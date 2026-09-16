// Package recovery validates an opt-in fresh-execution retry. Eligible
// matches identity purely on RunnerName, RunID and Attempt against the REST
// jobs list - Evidence.ScaleSetJobID is carried for callers but is not part
// of that match.
package recovery

import "errors"

type Policy struct {
	Enabled                    bool
	MaxRetries                 int
	AcknowledgeRepeatedEffects bool
}
type RESTJob struct {
	ID         int64
	RunID      int64
	Attempt    int
	RunnerName string
	Status     string
	Conclusion string
}
type Evidence struct {
	ScaleSetJobID       string
	RunnerName          string
	RunID               int64
	Attempt             int
	ProviderInterrupted bool
	Jobs                []RESTJob
	RetriesUsed         int
	RequestPending      bool
}

func Eligible(p Policy, e Evidence) (RESTJob, error) {
	if !p.Enabled {
		return RESTJob{}, errors.New("spot retries disabled")
	}
	if !p.AcknowledgeRepeatedEffects || p.MaxRetries < 1 || p.MaxRetries > 3 {
		return RESTJob{}, errors.New("retry policy requires acknowledgment and a 1..3 limit")
	}
	if e.RetriesUsed < 0 || e.RetriesUsed >= p.MaxRetries || e.RequestPending {
		return RESTJob{}, errors.New("retry budget exhausted or commitment unknown")
	}
	if !e.ProviderInterrupted || e.RunnerName == "" || e.RunID <= 0 || e.Attempt < 1 {
		return RESTJob{}, errors.New("interruption or identity evidence missing")
	}
	var matches []RESTJob
	for _, j := range e.Jobs {
		if j.RunnerName == e.RunnerName && j.RunID == e.RunID && j.Attempt == e.Attempt {
			matches = append(matches, j)
		}
	}
	if len(matches) != 1 {
		return RESTJob{}, errors.New("REST identity is not unique")
	}
	j := matches[0]
	if j.ID <= 0 || j.Status != "completed" || j.Conclusion != "failure" {
		return RESTJob{}, errors.New("job is not a terminal failure")
	}
	return j, nil
}
