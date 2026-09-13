package lifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	l "github.com/tsouza/runnerscout/internal/lifecycle"
)

type resourceCloud struct {
	*cloud
	store *store
	refs  []l.ResourceReference
	t     *testing.T
}

func (p *resourceCloud) Observe(context.Context, l.Allocation) (l.Observation, error) {
	return l.Observation{Known: true, Exists: p.exists, ResourceID: "vm-1", Resources: p.refs}, nil
}
func (p *resourceCloud) Delete(_ context.Context, a l.Allocation) error {
	if len(a.Resources) != 1 || len(p.store.a.Resources) != 1 || a.Resources[0].ID != "volume-1" || p.store.a.Resources[0].ID != "volume-1" {
		p.t.Error("delete ran before its dependency was durably recorded")
	}
	p.deleted++
	return nil
}

func TestCloudDependenciesPersistBeforeDeleteAndSurviveRestart(t *testing.T) {
	for _, phase := range []l.Phase{l.Creating, l.Running, l.Deleting} {
		t.Run(string(phase), func(t *testing.T) {
			controller, durable, base, now := setup()
			durable.a.Phase = phase
			durable.a.Offering.Provider = "a"
			durable.a.Ready = true
			base.exists = true
			provider := &resourceCloud{cloud: base, store: durable, t: t, refs: []l.ResourceReference{{Kind: "aws-volume", ID: "volume-1"}}}
			controller.Providers = map[string]l.Provider{"a": provider}
			if err := controller.Step(context.Background(), durable.a.ID); err != nil {
				t.Fatal(err)
			}
			if len(durable.a.Resources) != 1 {
				t.Fatal("observed dependency was not persisted")
			}
			// The cloud no longer returns discovery tags after a restart. The
			// deletion must still receive the previously recorded dependency.
			provider.refs = nil
			durable.a.Phase = l.Deleting
			controller = &l.Controller{Store: durable, Providers: map[string]l.Provider{"a": provider}, Now: func() time.Time { return *now }}
			if err := controller.Step(context.Background(), durable.a.ID); err != nil {
				t.Fatal(err)
			}
			if len(durable.a.Resources) != 1 || provider.deleted == 0 {
				t.Fatal("restart forgot dependency or did not reconcile deletion")
			}
		})
	}
}

func TestCloudDependencyCheckpointConflictPreventsDeletion(t *testing.T) {
	controller, durable, base, _ := setup()
	durable.a.Phase = l.Deleting
	durable.a.Offering.Provider = "a"
	durable.fail = true
	base.exists = true
	provider := &resourceCloud{cloud: base, store: durable, t: t, refs: []l.ResourceReference{{Kind: "aws-volume", ID: "volume-1"}}}
	controller.Providers = map[string]l.Provider{"a": provider}
	if err := controller.Step(context.Background(), durable.a.ID); !errors.Is(err, l.ErrConflict) {
		t.Fatal("dependency checkpoint conflict was ignored", err)
	}
	if provider.deleted != 0 || len(durable.a.Resources) != 0 {
		t.Fatal("failed checkpoint allowed deletion or mutated stored dependencies")
	}
}

func TestInvalidCloudDependenciesPreventDeletion(t *testing.T) {
	tooMany := make([]l.ResourceReference, 65)
	for i := range tooMany {
		tooMany[i] = l.ResourceReference{Kind: "aws-volume", ID: fmt.Sprintf("vol-%d", i)}
	}
	for _, refs := range [][]l.ResourceReference{{{Kind: "", ID: "volume-1"}}, {{Kind: "aws-volume", ID: ""}}, tooMany} {
		controller, durable, base, _ := setup()
		durable.a.Phase = l.Deleting
		durable.a.Offering.Provider = "a"
		base.exists = true
		provider := &resourceCloud{cloud: base, store: durable, t: t, refs: refs}
		controller.Providers = map[string]l.Provider{"a": provider}
		if err := controller.Step(context.Background(), durable.a.ID); err == nil || provider.deleted != 0 || len(durable.a.Resources) != 0 {
			t.Fatal("invalid dependencies permitted effects", err)
		}
	}
}
