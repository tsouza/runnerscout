package operator

import (
	"context"

	"github.com/tsouza/runnerscout/internal/azurequeue"
	"github.com/tsouza/runnerscout/internal/provider"
)

// azureInterruptionObserver is the surface internal/azurequeue.Client
// provides; declared locally, mirroring awsPriceObserver/azurePriceObserver
// (prices.go), so tests can substitute a fake without importing
// internal/azurequeue's own construction machinery.
type azureInterruptionObserver interface {
	Poll(ctx context.Context) ([]azurequeue.Result, error)
}

// pollAzureInterruptions runs at most once per Tick cycle (never per
// allocation - see Tick's own call site) and returns a resourceID ->
// confirmed-preempted snapshot built from exactly one
// internal/azurequeue.Client.Poll call, mirroring refreshAWSPrices/
// refreshAzurePrices's single-poll-per-Tick shape (prices.go): compute
// once, thread the result through, never re-query per allocation.
//
// A nil AzureInterruptions (the default) or a failed poll both return a nil
// map - nil-is-inert, exactly like AWSPrices/AzurePrices: no Azure
// allocation's observation is ever affected this cycle. A message this
// poll could not classify (Result.Err set) is skipped, never treated as a
// confirmed preemption; a duplicate resourceID across messages in the same
// poll only ever strengthens false to true, never the reverse, since
// Storage Queue's at-least-once redelivery can legitimately repeat the same
// confirmed fact.
func (o *Operator) pollAzureInterruptions(ctx context.Context) map[string]bool {
	if o.AzureInterruptions == nil {
		return nil
	}
	// Bounded like every other external call reachable from Tick's own ctx
	// outside Step's per-allocation goroutine budget - see externalCallBudget's
	// own comment (operator.go) for why: this runs on runLeader's cancel-only,
	// no-deadline ctx, and lease renewal is independent of Azure connectivity,
	// so a stalled Storage Queue dequeue here would otherwise hang every
	// subsequent Tick indefinitely, the same incident shape externalCallBudget
	// exists to prevent.
	callCtx, cancel := context.WithTimeout(ctx, externalCallBudget)
	results, err := o.AzureInterruptions.Poll(callCtx)
	cancel()
	if err != nil {
		return nil
	}
	m := make(map[string]bool, len(results))
	for _, r := range results {
		if r.Err != nil || r.ResourceID == "" {
			continue
		}
		m[r.ResourceID] = m[r.ResourceID] || r.Preempted
	}
	return m
}

// applyAzureInterruptions threads this Tick's single interruption snapshot
// down to every azure-kind provider.Command already stored in
// Controller.Providers, mirroring credentials.go's NewWithCredentials
// type-assertion pattern for mutating a *provider.Command in that same map.
// It must run once per Tick, before Controller.Step's per-allocation loop -
// never per-allocation, since a azurequeue poll drains every
// currently-visible message in one call (internal/azurequeue.Client.Poll's
// own doc comment), not just the ones relevant to any one allocation.
//
// It runs unconditionally, even when m is nil: an operator that never opts
// into Azure interruption delivery must clear any stale snapshot exactly as
// reliably as one that does, so every azure-kind Command's
// AzureInterrupted always reflects only the most recent Tick's poll (or
// nothing at all), never a lingering earlier one.
func (o *Operator) applyAzureInterruptions(m map[string]bool) {
	for name, cfg := range o.Config.Providers {
		if cfg.Kind != "azure" {
			continue
		}
		if command, ok := o.Controller.Providers[name].(*provider.Command); ok {
			command.AzureInterrupted = m
		}
	}
}
