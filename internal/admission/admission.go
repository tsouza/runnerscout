// Package admission converts absolute demand to bounded durable admission slots.
package admission

import "errors"

type State struct {
	Cohort        uint64 `json:"cohort"`
	Admitted      int    `json:"admitted"`
	ResetObserved bool   `json:"resetObserved"`
}

// Reconcile returns new slots. Admitted itself is decremented per allocation
// by the caller (internal/operator's HandleDesiredRunnerCount) as soon as
// that allocation's own outcome is confirmed terminal (Deleted or TimedOut -
// see issue #174 for why TimedOut releases immediately too, not only via
// the reset below), not by this function. The Cohort/full-reset below is a
// separate, coarser mechanism: a clean generational rebaseline once demand
// has genuinely dropped to zero and every remaining allocation is
// confirmed cleaned up, independent of whether any individual allocation
// already released its own slot.
func (s *State) Reconcile(demand, active, limit int, cleanupConfirmed bool) (int, error) {
	if demand < 0 || active < 0 || limit < 1 || limit > 100 || active > 100 || s.Admitted < 0 || s.Admitted > 100 {
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
	n := min(max(0, desired-s.Admitted), max(0, max(0, limit-active)))
	s.Admitted += n
	return n, nil
}
