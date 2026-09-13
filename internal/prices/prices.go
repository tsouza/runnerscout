// Package prices observes real-time cloud capacity prices. It produces a
// placement.Offering-compatible price and freshness timestamp but does not
// depend on package placement or on any catalog-loading, admission or reload
// path - those remain a separate design decision.
package prices

import (
	"time"
)

// Quote mirrors placement.Offering's price fields exactly, so a caller can
// copy it directly onto an Offering without conversion.
type Quote struct {
	PriceMicros int64
	Currency    string
	ObservedAt  time.Time
}
