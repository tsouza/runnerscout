package operator

import (
	"context"
	"errors"
	"time"

	"github.com/tsouza/runnerscout/internal/lifecycle"
)

// SameBinding identifies changes that the current durable fleet can adopt.
// Catalog refresh and admission limits may change; resource ownership may not.
func SameBinding(a, b Config) bool {
	return (&Operator{Config: a}).binding() == (&Operator{Config: b}).binding()
}

// RunCleanup requires the caller's scale-set Lease and never opens a GitHub
// session. Cloud cleanup must remain possible when GitHub credentials are gone.
func (o *Operator) RunCleanup(ctx context.Context) error {
	o.Drain()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		_ = o.Tick(ctx) // Unknown results remain persisted and are retried below.
		if done, err := o.Drained(ctx); err == nil && done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// RunRecovery reconciles accepted work while configuration or GitHub credentials
// are unavailable. Existing deadlines remain authoritative; no new demand is
// accepted and an existing job is not retired merely because recovery started.
func (o *Operator) RunRecovery(ctx context.Context) error {
	o.PauseAdmissions(true)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		_ = o.Tick(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// PauseAdmissions preserves accepted allocations and their original deadlines.
// Reconciliation and cleanup continue while the configuration is suspended.
func (o *Operator) PauseAdmissions(paused bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.paused = paused
}

// Drain is monotonic for this worker. A deleting CRD must not restart admissions
// if a later read accidentally presents a non-deleting configuration snapshot.
func (o *Operator) Drain() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.draining = true
	o.paused = true
}

// Drained requires persisted terminal states. An accepted delete request,
// ambiguous create result or missing pending-allocation record is insufficient.
func (o *Operator) Drained(ctx context.Context) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, fleet, err := o.readFleet(ctx, false)
	if err != nil {
		return false, err
	}
	if len(fleet.Pending) != 0 {
		return false, nil
	}
	allocations, err := o.Store.List(ctx)
	if err != nil {
		return false, err
	}
	recorded := make(map[string]bool, len(allocations))
	for _, allocation := range allocations {
		if _, exists := fleet.Created[allocation.ID]; !exists {
			return false, errors.New("allocation has no durable lifetime origin")
		}
		recorded[allocation.ID] = true
		if allocation.Phase != lifecycle.Deleted && allocation.Phase != lifecycle.TimedOut {
			return false, nil
		}
	}
	for id := range fleet.Created {
		if !recorded[id] && !fleet.Pruned[id] {
			return false, nil
		}
	}
	return true, nil
}
