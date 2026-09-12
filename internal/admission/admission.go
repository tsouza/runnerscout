// Package admission converts absolute demand to bounded durable admission slots.
package admission

import "errors"

type State struct {
	Cohort        uint64 `json:"cohort"`
	Admitted      int    `json:"admitted"`
	ResetObserved bool   `json:"resetObserved"`
}

// Reconcile returns new slots. Failed/expired admissions remain consumed until a
// zero-demand reset and confirmed cleanup, preventing infinite deadline rearming.
func (s *State) Reconcile(demand, active, limit int, cleanupConfirmed bool) (int, error) {
	if demand < 0 || active < 0 || limit < 1 || limit > 100 || active > limit || s.Admitted < 0 || s.Admitted > limit {
		return 0, errors.New("invalid admission bounds")
	}
	if demand == 0 {
		s.ResetObserved = true
	}
	if s.ResetObserved && cleanupConfirmed && active == 0 {
		s.Cohort++
		s.Admitted = 0
		s.ResetObserved = false
	}
	desired := min(demand, limit)
	n := min(max(0, desired-s.Admitted), limit-active)
	s.Admitted += n
	return n, nil
}
