// Package placement selects from a finite, explicitly scoped price snapshot.
package placement

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

type Requirements struct {
	CPU            int      `json:"cpu"`
	MemoryMiB      int      `json:"memoryMiB"`
	Architecture   string   `json:"architecture"`
	Vendor         string   `json:"vendor,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
	Providers      []string `json:"providers"`
	Regions        []string `json:"regions"`
	MaxPriceMicros int64    `json:"maxPriceMicros"`
	AllowOnDemand  bool     `json:"allowOnDemand"`
	Policy         string   `json:"policy"`
}
type Offering struct {
	ID           string    `json:"id"`
	Provider     string    `json:"provider"`
	Region       string    `json:"region"`
	Zone         string    `json:"zone"`
	Machine      string    `json:"machine"`
	Image        string    `json:"image"`
	CPU          int       `json:"cpu"`
	MemoryMiB    int       `json:"memoryMiB"`
	Architecture string    `json:"architecture"`
	Vendor       string    `json:"vendor,omitempty"`
	Capabilities []string  `json:"capabilities,omitempty"`
	Spot         bool      `json:"spot"`
	PriceMicros  int64     `json:"priceMicros"`
	Currency     string    `json:"currency"`
	ObservedAt   time.Time `json:"observedAt"`
	// GCPSkuRefs pins this offering's compute-core and RAM charges to exact,
	// human-verified Cloud Billing Catalog SKU IDs - see
	// api/v1alpha1.Offering.GCPSkuRefs, which this field mirrors exactly
	// (internal/configapi/compile.go copies it through unchanged). nil (the
	// default) means this offering is never touched by
	// internal/operator.Operator.refreshGCPPrices, regardless of Provider
	// or internal/operator.Config.GCPPriceRefresh.
	GCPSkuRefs *GCPSkuRefs `json:"gcpSkuRefs,omitempty"`
}

// GCPSkuRefs is internal/prices.GCPSkuClient.Observe's exact input shape,
// carried on an Offering rather than passed as bare strings so a catalog's
// pinning stays attached to the offering it was verified against - see
// Offering.GCPSkuRefs.
type GCPSkuRefs struct {
	CoreSkuID string `json:"coreSkuId"`
	RamSkuID  string `json:"ramSkuId"`
}
type Catalog struct {
	Offerings []Offering      `json:"offerings"`
	Complete  map[string]bool `json:"complete"`
}
type Outcome string

const (
	CapacityRejected Outcome = "capacity-rejected"
	Unknown          Outcome = "unknown"
	CoolingDown      Outcome = "cooling-down"
)

var ErrIncomplete = errors.New("catalog or spot search incomplete")
var ErrExhausted = errors.New("eligible capacity exhausted")

const MaxPriceAge = 5 * time.Minute

// maxPriceMicrosCeiling mirrors api/v1alpha1's
// PlacementPolicy.MaxPriceMicros kubebuilder Maximum marker (keep both in
// sync by hand - kubebuilder markers can't reference a Go constant). It
// exists to keep reservationMicros's maxPriceMicros*maxLifetimeSeconds
// multiplication (maxLifetimeSeconds itself independently capped at 21600)
// well clear of int64 overflow, not merely to reject implausible prices.
const maxPriceMicrosCeiling = 100_000_000_000

func (r Requirements) Validate() error {
	if r.CPU < 1 || r.MemoryMiB < 1 || r.MaxPriceMicros < 1 || r.MaxPriceMicros > maxPriceMicrosCeiling {
		return fmt.Errorf("positive cpu, memoryMiB and maxPriceMicros required, maxPriceMicros must not exceed %d", int64(maxPriceMicrosCeiling))
	}
	if r.Architecture != "amd64" && r.Architecture != "arm64" {
		return errors.New("architecture must be amd64 or arm64")
	}
	if len(r.Providers) == 0 || len(r.Regions) == 0 {
		return errors.New("explicit provider and region scope required")
	}
	if r.Policy != "lowest-price" {
		return errors.New("unsupported policy")
	}
	return nil
}
func eligible(o Offering, r Requirements) bool {
	if !slices.Contains(r.Providers, o.Provider) || !slices.Contains(r.Regions, o.Region) || o.CPU < r.CPU || o.MemoryMiB < r.MemoryMiB || o.Architecture != r.Architecture || o.PriceMicros > r.MaxPriceMicros {
		return false
	}
	if r.Vendor != "" && o.Vendor != r.Vendor {
		return false
	}
	for _, c := range r.Capabilities {
		if !slices.Contains(o.Capabilities, c) {
			return false
		}
	}
	return true
}

// Choose never treats unknown, cooling down or unvisited spot pools as exhausted.
// Outcomes belong to this exact catalog snapshot and must not be reused after refresh.
func Choose(now time.Time, r Requirements, c Catalog, outcomes map[string]Outcome) (Offering, error) {
	if err := r.Validate(); err != nil {
		return Offering{}, err
	}
	for _, p := range r.Providers {
		if !c.Complete[p] {
			return Offering{}, ErrIncomplete
		}
	}
	seen := map[string]bool{}
	spot := []Offering{}
	ondemand := []Offering{}
	blocked := false
	onDemandBlocked := false
	for _, o := range c.Offerings {
		if o.ID == "" || seen[o.ID] {
			return Offering{}, errors.New("missing or duplicate pool identity")
		}
		seen[o.ID] = true
		if !slices.Contains(r.Providers, o.Provider) || !slices.Contains(r.Regions, o.Region) {
			continue
		}
		if o.CPU < 1 || o.MemoryMiB < 1 || o.Image == "" || o.Machine == "" || o.Zone == "" || o.PriceMicros < 0 || o.Currency != "USD" || o.ObservedAt.IsZero() || now.Before(o.ObservedAt) || now.Sub(o.ObservedAt) > MaxPriceAge {
			return Offering{}, fmt.Errorf("%w: invalid or stale pool %s", ErrIncomplete, o.ID)
		}
		if !eligible(o, r) {
			continue
		}
		if !o.Spot {
			switch outcomes[o.ID] {
			case "":
				ondemand = append(ondemand, o)
			case CapacityRejected:
			default:
				onDemandBlocked = true
			}
			continue
		}
		switch outcomes[o.ID] {
		case "":
			spot = append(spot, o)
		case CapacityRejected:
		default:
			blocked = true
		}
	}
	rank := func(a, b Offering) int {
		if a.PriceMicros < b.PriceMicros {
			return -1
		}
		if a.PriceMicros > b.PriceMicros {
			return 1
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	}
	if len(spot) > 0 {
		slices.SortFunc(spot, rank)
		return spot[0], nil
	}
	if blocked {
		return Offering{}, ErrIncomplete
	}
	if r.AllowOnDemand && len(ondemand) > 0 {
		slices.SortFunc(ondemand, rank)
		return ondemand[0], nil
	}
	if r.AllowOnDemand && onDemandBlocked {
		return Offering{}, ErrIncomplete
	}
	return Offering{}, ErrExhausted
}
