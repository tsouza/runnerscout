package operator

import (
	"context"

	"github.com/tsouza/runnerscout/internal/azurequeue"
)

// azureInterruptionObserver is the surface internal/azurequeue.Client
// provides. It is declared here, unread by anything yet, purely as the
// dependency-injection point a future change would fill in - mirroring how
// awsPriceObserver/azurePriceObserver (prices.go) are declared next to the
// Operator fields they back. See docs/azure-interruption-delivery.md for
// why no refresh/consumption function exists alongside it yet: correlating
// a azurequeue.Result's resource ID against a specific allocation, and the
// exact internal/provider/azure.go call site that would consume it, are
// real remaining implementation decisions that document deliberately does
// not make.
type azureInterruptionObserver interface {
	Poll(ctx context.Context) ([]azurequeue.Result, error)
}
