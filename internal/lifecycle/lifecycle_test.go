package lifecycle_test

import (
	"context"
	"errors"
	l "github.com/tsouza/runnerscout/internal/lifecycle"
	p "github.com/tsouza/runnerscout/internal/placement"
	"strconv"
	"testing"
	"time"
)

type store struct {
	a        l.Allocation
	revision int
	fail     bool
}

func (s *store) Load(context.Context, string) (l.Allocation, error) { return s.a, nil }
func (s *store) Save(_ context.Context, a l.Allocation, version string) (l.Allocation, error) {
	if s.fail || version != s.a.Revision {
		return a, l.ErrConflict
	}
	s.revision++
	a.Revision = strconv.Itoa(s.revision)
	s.a = a
	return a, nil
}

type cloud struct {
	created, deleted                        int
	exists, unknown, loseResponse, capacity bool
}

func (c *cloud) Create(context.Context, l.Allocation) (string, error) {
	c.created++
	if c.capacity {
		return "", l.ErrCapacity
	}
	c.exists = true
	if c.loseResponse {
		return "", errors.New("connection lost")
	}
	return "vm-1", nil
}
func (c *cloud) Observe(context.Context, l.Allocation) (l.Observation, error) {
	return l.Observation{Known: !c.unknown, Exists: c.exists, ResourceID: "vm-1"}, nil
}
func (c *cloud) Delete(context.Context, l.Allocation) error { c.deleted++; return nil }
func setup() (*l.Controller, *store, *cloud, *time.Time) {
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	s := &store{a: l.Allocation{ID: "rs-test", Phase: l.Pending, Deadline: now.Add(time.Minute), MaxAttempts: 3, Requirements: p.Requirements{CPU: 1, MemoryMiB: 1024, Architecture: "amd64", MaxPriceMicros: 100, Providers: []string{"a"}, Regions: []string{"r"}, Policy: "lowest-price"}, Catalog: p.Catalog{Complete: map[string]bool{"a": true}, Offerings: []p.Offering{{ID: "pool", Provider: "a", Region: "r", Zone: "z", Machine: "m", Image: "i", CPU: 1, MemoryMiB: 1024, Architecture: "amd64", Spot: true, PriceMicros: 10, Currency: "USD", ObservedAt: now}}}}}
	cloud := &cloud{}
	c := &l.Controller{Store: s, Providers: map[string]l.Provider{"a": cloud}, Now: func() time.Time { return now }}
	return c, s, cloud, &now
}
func TestLostCreateResponseRestartAndCleanup(t *testing.T) {
	c, s, p, now := setup()
	p.loseResponse = true
	ctx := context.Background()
	if c.Step(ctx, "rs-test") == nil {
		t.Fatal("must expose uncertainty")
	}
	if s.a.Phase != l.Creating || p.created != 1 {
		t.Fatal(s.a)
	}
	// A new controller shares durable storage, not volatile state.
	c = &l.Controller{Store: s, Providers: map[string]l.Provider{"a": p}, Now: func() time.Time { return *now }}
	*now = now.Add(time.Minute)
	if e := c.Step(ctx, "rs-test"); e != nil {
		t.Fatal(e)
	}
	if p.created != 1 || s.a.Phase != l.Deleting {
		t.Fatal(s.a)
	}
	if e := c.Step(ctx, "rs-test"); e != nil {
		t.Fatal(e)
	}
	if s.a.Phase != l.Deleting || p.deleted != 1 {
		t.Fatal("delete acknowledgment must not mark absent")
	}
	p.exists = false
	if e := c.Step(ctx, "rs-test"); e != nil {
		t.Fatal(e)
	}
	if s.a.Phase != l.Deleted {
		t.Fatal(s.a)
	}
}
func TestIntentConflictPreventsCloudCall(t *testing.T) {
	c, s, p, _ := setup()
	s.fail = true
	if !errors.Is(c.Step(context.Background(), "rs-test"), l.ErrConflict) || p.created != 0 {
		t.Fatal("cloud side effect before durable ownership")
	}
}
func TestUnknownAbsenceNeverBlindlyCreates(t *testing.T) {
	c, s, p, now := setup()
	p.loseResponse = true
	_ = c.Step(context.Background(), "rs-test")
	p.exists = false
	*now = now.Add(10 * time.Minute)
	for range 5 {
		if e := c.Step(context.Background(), "rs-test"); e != nil {
			t.Fatal(e)
		}
	}
	if p.created != 1 || s.a.Phase != l.Creating || s.a.Condition != "LocalProvisioningTimeoutCommitmentUnknown" {
		t.Fatal(s.a)
	}
}
func TestDeadlineInclusiveNoCloudEffect(t *testing.T) {
	c, s, p, now := setup()
	*now = s.a.Deadline
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	if s.a.Phase != l.TimedOut || p.created != 0 {
		t.Fatal(s.a)
	}
}
func TestCapacityRejectionKeepsDeadline(t *testing.T) {
	c, s, p, _ := setup()
	deadline := s.a.Deadline
	p.capacity = true
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	if s.a.Phase != l.Pending || s.a.Attempts != 1 || s.a.Deadline != deadline || s.a.Outcomes["pool"] != p2Capacity() {
		t.Fatal(s.a)
	}
}
func p2Capacity() p.Outcome { return p.CapacityRejected }
