package operator

import (
	"time"

	"github.com/tsouza/runnerscout/internal/lifecycle"
)

// reservationMicros is the worst-case per-unit reservation: the admission-time
// requirements' price ceiling times the scale set's max lifetime, rounded up
// to never under-reserve. Price is USD-micros per hour.
func reservationMicros(maxPriceMicros int64, maxLifetimeSeconds int) int64 {
	return (maxPriceMicros*int64(maxLifetimeSeconds) + 3599) / 3600
}

// spentToday sums f.Reserved for every allocation admitted (per f.Created) on
// now's UTC day, excluding allocations proven TimedOut - lifecycle.Step
// proves a Pending allocation never holds cloud resources, so a TimedOut
// allocation's reservation never became real spend. An allocation ID with no
// matching record in allocs (not yet materialized into the Store, or already
// pruned) cannot be proven TimedOut and is included.
func spentToday(f fleet, allocs []lifecycle.Allocation, now time.Time) int64 {
	phase := make(map[string]lifecycle.Phase, len(allocs))
	for _, a := range allocs {
		phase[a.ID] = a.Phase
	}
	y1, m1, d1 := now.UTC().Date()
	var total int64
	for id, reserved := range f.Reserved {
		if p, known := phase[id]; known && p == lifecycle.TimedOut {
			continue
		}
		created, ok := f.Created[id]
		if !ok {
			continue
		}
		y2, m2, d2 := created.UTC().Date()
		if y1 == y2 && m1 == m2 && d1 == d2 {
			total += reserved
		}
	}
	return total
}
