package lifecycle_test

import (
	"context"
	"errors"
	"fmt"
	l "github.com/tsouza/runnerscout/internal/lifecycle"
	p "github.com/tsouza/runnerscout/internal/placement"
	"strconv"
	"strings"
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
	created, deleted                                     int
	exists, unknown, loseResponse, capacity, interrupted bool
	noEffect                                             error
	observeErr                                           error
	deleteErr                                            error
}

func (c *cloud) Create(context.Context, l.Allocation) (string, error) {
	c.created++
	if c.capacity {
		return "", l.ErrCapacity
	}
	if c.noEffect != nil {
		return "", c.noEffect
	}
	c.exists = true
	if c.loseResponse {
		return "", errors.New("connection lost")
	}
	return "vm-1", nil
}
func (c *cloud) Observe(context.Context, l.Allocation) (l.Observation, error) {
	if c.observeErr != nil {
		return l.Observation{}, c.observeErr
	}
	return l.Observation{Known: !c.unknown, Exists: c.exists, Interrupted: c.interrupted, ResourceID: "vm-1"}, nil
}
func (c *cloud) Delete(context.Context, l.Allocation) error { c.deleted++; return c.deleteErr }
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

// A resource that stays unobservable (Known: false) must never make the
// controller blindly retry creation for the same allocation on its own -
// it just keeps surfacing an error and waiting. Confirmed absence (Known:
// true, Exists: false) is a different, more resolved case - see
// TestConfirmedAbsenceDuringCreatingReleasesTheAllocation below.
func TestUnknownAbsenceNeverBlindlyCreates(t *testing.T) {
	c, s, p, now := setup()
	p.loseResponse = true
	_ = c.Step(context.Background(), "rs-test")
	p.unknown = true
	*now = now.Add(10 * time.Minute)
	for range 5 {
		if e := c.Step(context.Background(), "rs-test"); e == nil {
			t.Fatal("an unknown observation must surface as an error, not silently succeed")
		}
	}
	if p.created != 1 || s.a.Phase != l.Creating {
		t.Fatal(s.a)
	}
}

// A create attempt that fails before any cloud effect (ErrNoEffect) must
// surface its real underlying cause through Condition, not a bare fixed
// string - a provider wrapping a specific reason (e.g. "GitHub JIT request
// failed: unexpected status code: 429") under ErrNoEffect must have that
// reason readable from the allocation's own status, not discarded. Issue
// #176: a real GitHub-side JIT failure was indistinguishable from any other
// preparation failure, including a GCP-provider one, until this was fixed.
func TestCreatePreparationFailureSurfacesItsRealCause(t *testing.T) {
	c, s, cloud, _ := setup()
	cloud.noEffect = fmt.Errorf("%w: GitHub JIT request failed: unexpected status code: 429", l.ErrNoEffect)
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	if s.a.Phase != l.Pending {
		t.Fatal("a preparation failure with no cloud effect must remain retryable", s.a)
	}
	if !strings.Contains(s.a.Condition, "GitHub JIT request failed: unexpected status code: 429") {
		t.Fatal("Condition must surface the real underlying cause, not a bare fixed string", s.a.Condition)
	}
}

// A cloud-create failure that returns an empty receipt must leave the real
// provider error readable from Condition, not only from the returned error.
func TestCloudCreateFailureSurfacesItsRealCause(t *testing.T) {
	c, s, cloud, _ := setup()
	cloud.loseResponse = true
	if e := c.Step(context.Background(), "rs-test"); e == nil {
		t.Fatal("ambiguous create must surface as an error")
	}
	if s.a.Phase != l.Creating {
		t.Fatal("expected ambiguous create to leave the allocation in Creating", s.a)
	}
	if !strings.Contains(s.a.Condition, "CreateCommitmentUnknown: connection lost") {
		t.Fatal("Condition must surface the real cloud-create failure cause", s.a.Condition)
	}
}

// Observation failures in Creating and Running must also preserve their real
// cause in Condition, rather than only returning a fixed error string.
func TestObservationFailuresSurfaceTheirRealCause(t *testing.T) {
	c, s, cloud, _ := setup()
	cloud.loseResponse = true
	_ = c.Step(context.Background(), "rs-test")

	cloud.observeErr = errors.New("create observe failed")
	if e := c.Step(context.Background(), "rs-test"); e == nil {
		t.Fatal("unknown create observation must surface as an error")
	}
	if !strings.Contains(s.a.Condition, "CreateReconciliationUnknown: create observe failed") {
		t.Fatal("Condition must surface the create-observation failure cause", s.a.Condition)
	}

	cloud.loseResponse = false
	cloud.observeErr = nil
	cloud.exists = true
	cloud.unknown = false
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	if s.a.Phase != l.Running {
		t.Fatal("expected allocation to reach Running", s.a)
	}

	cloud.observeErr = errors.New("running observe failed")
	if e := c.Step(context.Background(), "rs-test"); e == nil {
		t.Fatal("unknown running observation must surface as an error")
	}
	if !strings.Contains(s.a.Condition, "ResourceObservationUnknown: running observe failed") {
		t.Fatal("Condition must surface the running-observation failure cause", s.a.Condition)
	}

}

// A delete failure must preserve its real cause in Condition too, mirroring
// the JIT/cloud-create/observation fixes.
func TestDeleteFailureSurfacesItsRealCause(t *testing.T) {
	c, s, cloud, now := setup()
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	if s.a.Phase != l.Running {
		t.Fatal("expected allocation to reach Running", s.a)
	}
	*now = now.Add(10 * time.Minute)
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	if s.a.Phase != l.Deleting {
		t.Fatal("expected expired allocation to move to Deleting", s.a)
	}

	cloud.deleteErr = errors.New("delete failed")
	if e := c.Step(context.Background(), "rs-test"); e == nil {
		t.Fatal("failed delete must surface as an error")
	}
	if !strings.Contains(s.a.Condition, "CleanupUnconfirmed: delete failed") {
		t.Fatal("Condition must surface the delete failure cause", s.a.Condition)
	}
}

// Once Create()'s own attempt is ambiguous (any error other than
// ErrNoEffect/ErrCapacity with an empty result), the allocation is
// committed to Creating - but a later Observe can still definitively
// confirm the resource never existed at all (e.g. a client-side rejection
// that never reached the cloud API). That confirmation must not park the
// allocation in Creating forever with no way back: Deleted already means
// exactly this ("cloud resource confirmed gone"), regardless of Retire, so
// the allocation releases and a fresh admission cycle can replace it.
func TestConfirmedAbsenceDuringCreatingReleasesTheAllocation(t *testing.T) {
	c, s, cloud, _ := setup()
	cloud.loseResponse = true
	if e := c.Step(context.Background(), "rs-test"); e == nil {
		t.Fatal("ambiguous create must surface as an error")
	}
	if s.a.Phase != l.Creating || cloud.created != 1 {
		t.Fatal("expected ambiguous create to leave the allocation in Creating", s.a)
	}
	// The create call never actually reached the provider - Observe now
	// confirms the resource genuinely never existed, well within the
	// deadline (not an expiry-driven path).
	cloud.exists = false
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	if s.a.Phase != l.Deleted {
		t.Fatal("confirmed absence during Creating must not park the allocation forever", s.a)
	}
	if cloud.created != 1 {
		t.Fatal("must never blindly retry creation for the same allocation", cloud.created)
	}
}

// The same recovery must apply after the local provisioning deadline has
// also passed - confirmed absence during Creating is not itself a
// deadline-driven case, so expiry must not change the outcome.
func TestConfirmedAbsenceDuringCreatingReleasesTheAllocationAfterExpiry(t *testing.T) {
	c, s, cloud, now := setup()
	cloud.loseResponse = true
	_ = c.Step(context.Background(), "rs-test")
	cloud.exists = false
	*now = now.Add(10 * time.Minute)
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	if s.a.Phase != l.Deleted {
		t.Fatal("confirmed absence during Creating must release even past the deadline", s.a)
	}
	if cloud.created != 1 {
		t.Fatal("must never blindly retry creation for the same allocation", cloud.created)
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
	if s.a.TerminalAt != *now {
		t.Fatal("TerminalAt must be set on the TimedOut transition", s.a)
	}
}

type deregistrar struct {
	calls int
	id    string
	fail  bool
}

func (d *deregistrar) DeregisterRunner(_ context.Context, id string) error {
	d.calls++
	d.id = id
	if d.fail {
		return errors.New("deregistration failed")
	}
	return nil
}

func TestTimedOutDeregistersClaimedRunner(t *testing.T) {
	c, s, _, now := setup()
	d := &deregistrar{}
	c.Runners = d
	*now = s.a.Deadline
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	if d.calls != 1 || d.id != "rs-test" {
		t.Fatal("must deregister the claimed runner before persisting TimedOut", d)
	}
	if s.a.Phase != l.TimedOut {
		t.Fatal(s.a)
	}
}

// A failed deregistration attempt must never be masked by persisting TimedOut
// anyway - that would strand the orphaned GitHub runner registration forever,
// since Step treats TimedOut as a permanent no-op. The allocation must stay
// Pending so a later Step call retries.
func TestTimedOutRetriesDeregistrationUntilConfirmed(t *testing.T) {
	c, s, _, now := setup()
	d := &deregistrar{fail: true}
	c.Runners = d
	*now = s.a.Deadline
	if e := c.Step(context.Background(), "rs-test"); e == nil {
		t.Fatal("a failed deregistration must surface as an error")
	}
	if s.a.Phase != l.Pending || d.calls != 1 {
		t.Fatal("must not persist TimedOut over an unconfirmed orphaned registration", s.a)
	}
	d.fail = false
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	if s.a.Phase != l.TimedOut || d.calls != 2 {
		t.Fatal("must retry deregistration on the next Step and then persist TimedOut", s.a, d)
	}
}

// Deregistration must fire on every path into Deleted/TimedOut, not just
// Pending -> TimedOut: GitHub's scale-set listener registers a claimed
// runner name at JIT-config time, at or before Creating, so a hard spot
// interruption or a forced cloud Delete - which never gives the runner
// process a graceful shutdown to self-deregister - can equally strand a
// registration.
func TestSpotInterruptionDeregistersClaimedRunner(t *testing.T) {
	c, s, cloud, now := setup()
	d := &deregistrar{}
	c.Runners = d
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	if s.a.Phase != l.Running {
		t.Fatal("expected Running before interruption", s.a)
	}
	cloud.exists = false
	cloud.interrupted = true
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	if d.calls != 1 || d.id != "rs-test" {
		t.Fatal("must deregister the claimed runner on a confirmed interruption", d)
	}
	if s.a.Phase != l.Deleted {
		t.Fatal(s.a)
	}
	if s.a.TerminalAt != *now {
		t.Fatal("TerminalAt must be set on the Running->Deleted transition", s.a)
	}
}

func TestSpotInterruptionRetriesDeregistrationUntilConfirmed(t *testing.T) {
	c, s, cloud, _ := setup()
	d := &deregistrar{fail: true}
	c.Runners = d
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	cloud.exists = false
	if e := c.Step(context.Background(), "rs-test"); e == nil {
		t.Fatal("a failed deregistration must surface as an error")
	}
	if s.a.Phase != l.Running || d.calls != 1 {
		t.Fatal("must not persist Deleted over an unconfirmed orphaned registration", s.a)
	}
	d.fail = false
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	if s.a.Phase != l.Deleted || d.calls != 2 {
		t.Fatal("must retry deregistration on the next Step and then persist Deleted", s.a, d)
	}
}

func TestDeletingCleanupConfirmedDeregistersClaimedRunner(t *testing.T) {
	c, s, cloud, now := setup()
	d := &deregistrar{}
	c.Runners = d
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	s.a.Retire = true
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	if s.a.Phase != l.Deleting {
		t.Fatal("expected Deleting before cleanup confirmation", s.a)
	}
	cloud.exists = false
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	if d.calls != 1 || d.id != "rs-test" {
		t.Fatal("must deregister the claimed runner once normal cleanup is confirmed", d)
	}
	if s.a.TerminalAt != *now {
		t.Fatal("TerminalAt must be set on the Deleting->Deleted transition", s.a)
	}
	if s.a.Phase != l.Deleted {
		t.Fatal(s.a)
	}
}

func TestCreateConfirmedAbsentDeregistersClaimedRunner(t *testing.T) {
	c, s, cloud, now := setup()
	d := &deregistrar{}
	c.Runners = d
	cloud.loseResponse = true
	if e := c.Step(context.Background(), "rs-test"); e == nil {
		t.Fatal("ambiguous create must surface as an error")
	}
	if s.a.Phase != l.Creating {
		t.Fatal(s.a)
	}
	cloud.exists = false
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	if d.calls != 1 || d.id != "rs-test" {
		t.Fatal("must deregister the claimed runner even on a confirmed-absent create", d)
	}
	if s.a.Phase != l.Deleted {
		t.Fatal(s.a)
	}
	if s.a.TerminalAt != *now {
		t.Fatal("TerminalAt must be set on the Creating->Deleted transition", s.a)
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

func TestCooldownRetainsUnknownSearch(t *testing.T) {
	c, s, cloud, now := setup()
	c.Cooldowns = map[string]time.Time{"pool": now.Add(5 * time.Minute)}
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	if cloud.created != 0 || s.a.Phase != l.Pending || s.a.Condition != p.ErrIncomplete.Error() {
		t.Fatal(s.a)
	}
}
func TestUnreadyVMExpiresButStartedJobDoesNot(t *testing.T) {
	for _, ready := range []bool{false, true} {
		c, s, _, now := setup()
		if e := c.Step(context.Background(), "rs-test"); e != nil {
			t.Fatal(e)
		}
		s.a.Ready = ready
		*now = s.a.Deadline
		if e := c.Step(context.Background(), "rs-test"); e != nil {
			t.Fatal(e)
		}
		if (!ready && s.a.Phase != l.Deleting) || (ready && s.a.Phase != l.Running) {
			t.Fatal(ready, s.a)
		}
	}
}

func TestConfirmedInterruptionDistinguishesFromUnprovenAbsence(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		c, s, cloud, _ := setup()
		if e := c.Step(context.Background(), "rs-test"); e != nil {
			t.Fatal(e)
		}
		if s.a.Phase != l.Running {
			t.Fatal("expected running before absence", s.a)
		}
		cloud.exists, cloud.interrupted = false, interrupted
		if e := c.Step(context.Background(), "rs-test"); e != nil {
			t.Fatal(e)
		}
		want := "ResourceAbsentInterruptionUnproven"
		if interrupted {
			want = "ResourceAbsentConfirmedInterruption"
		}
		if s.a.Phase != l.Deleted || s.a.Condition != want {
			t.Fatal(interrupted, s.a)
		}
	}
}

func TestUnknownCreateWithStartedJobSurvivesProvisioningDeadline(t *testing.T) {
	c, s, cloud, now := setup()
	cloud.loseResponse = true
	if e := c.Step(context.Background(), "rs-test"); e == nil {
		t.Fatal("expected unknown create")
	}
	// A GitHub job-start observation proves readiness even while the create
	// response remains unreconciled. Provisioning expiry must not retire that job.
	s.a.Ready = true
	*now = s.a.Deadline
	if e := c.Step(context.Background(), "rs-test"); e != nil {
		t.Fatal(e)
	}
	if s.a.Phase != l.Running || cloud.created != 1 || cloud.deleted != 0 {
		t.Fatal(s.a)
	}
}
