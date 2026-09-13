package lifecycle_test

import (
	"context"
	"errors"
	"testing"
	"time"

	l "github.com/tsouza/runnerscout/internal/lifecycle"
)

type creationRecoveryCloud struct {
	*cloud
	durable             *store
	calls, observations int
	failure             error
	resources           []l.ResourceReference
	t                   *testing.T
}

func (p *creationRecoveryCloud) Observe(ctx context.Context, a l.Allocation) (l.Observation, error) {
	p.observations++
	return p.cloud.Observe(ctx, a)
}
func (p *creationRecoveryCloud) ReconcileCreation(_ context.Context, a l.Allocation) (l.Observation, error) {
	p.calls++
	if a.Phase != l.Creating || a.Revision == "" || a.Revision != p.durable.a.Revision {
		p.t.Error("recovery ran without the persisted creation fence")
	}
	return l.Observation{Known: p.failure == nil, Exists: true, ResourceID: "vm-1", Resources: p.resources}, p.failure
}

func TestCreationRecoveryKeepsProvenIdentitiesOnUnknownOutcome(t *testing.T) {
	c, s, base, _ := setup()
	s.a.Phase = l.Creating
	s.a.Offering = s.a.Catalog.Offerings[0]
	identity := l.ResourceReference{Kind: "azure-disk", ID: "/owned-disk", UID: "11111111-1111-4111-8111-111111111111"}
	p := &creationRecoveryCloud{cloud: base, durable: s, t: t, failure: errors.New("tag update response unknown"), resources: []l.ResourceReference{identity}}
	c.Providers = map[string]l.Provider{"a": p}
	deadline := s.a.Deadline
	if err := c.Step(context.Background(), s.a.ID); err == nil {
		t.Fatal("unknown recovery reported success")
	}
	if s.a.Phase != l.Creating || len(s.a.Resources) != 1 || s.a.Resources[0] != identity || s.a.Deadline != deadline || p.created != 0 {
		t.Fatal("partial recovery lost proven ownership or changed admission", s.a)
	}
	p.resources[0].UID = "22222222-2222-4222-8222-222222222222"
	if err := c.Step(context.Background(), s.a.ID); err == nil || s.a.Resources[0] != identity {
		t.Fatal("changed generation replaced original evidence", s.a, err)
	}
	p.resources[0] = identity
	p.failure = nil
	if err := c.Step(context.Background(), s.a.ID); err != nil || s.a.Phase != l.Running || s.a.Resources[0] != identity || s.a.Deadline != deadline || p.created != 0 {
		t.Fatal("recovery did not retain original generation", s.a, err)
	}
}
func TestCreationRecoveryRequiresCheckpointAndNeverCreatesReplacement(t *testing.T) {
	for _, mode := range []string{"success", "conflict", "unknown", "expired"} {
		t.Run(mode, func(t *testing.T) {
			c, s, base, now := setup()
			s.a.Phase = l.Creating
			s.a.Offering = s.a.Catalog.Offerings[0]
			p := &creationRecoveryCloud{cloud: base, durable: s, t: t}
			c.Providers = map[string]l.Provider{"a": p}
			deadline := s.a.Deadline
			switch mode {
			case "conflict":
				s.fail = true
			case "unknown":
				p.failure = errors.New("unfinished creation")
			case "expired":
				*now = deadline.Add(time.Second)
			}
			err := c.Step(context.Background(), s.a.ID)
			if p.created != 0 || p.observations != 0 {
				t.Fatal("recovery used creation or bypassed its fenced operation")
			}
			if mode == "conflict" {
				if !errors.Is(err, l.ErrConflict) || p.calls != 0 {
					t.Fatal("checkpoint conflict allowed effects", p.calls, err)
				}
				return
			}
			if p.calls != 1 || s.a.Deadline != deadline {
				t.Fatal("recovery moved deadline or repeated operation", p.calls)
			}
			if mode == "unknown" {
				if err == nil || s.a.Phase != l.Creating {
					t.Fatal("unknown recovery retired obligation", s.a, err)
				}
				return
			}
			want := l.Running
			if mode == "expired" {
				want = l.Deleting
			}
			if err != nil || s.a.Phase != want || s.a.ResourceID != "vm-1" {
				t.Fatal("recovery result not persisted", s.a, err)
			}
		})
	}
}
