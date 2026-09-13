package lifecycle_test

import (
	"context"
	"errors"
	"testing"

	l "github.com/tsouza/runnerscout/internal/lifecycle"
)

type receiptCloud struct {
	*cloud
	failure error
}

func (p *receiptCloud) Create(context.Context, l.Allocation) (string, error) {
	p.created++
	return "vm-1", p.failure
}
func (p *receiptCloud) CreateWithResources(context.Context, l.Allocation) (l.Creation, error) {
	p.created++
	return l.Creation{ResourceID: "vm-1", Resources: []l.ResourceReference{{Kind: "aws-volume", ID: "vol-created"}}}, p.failure
}

func TestCreationReceiptPersistsDependenciesAndOverridesRetryClassification(t *testing.T) {
	for _, failure := range []error{nil, errors.New("completion uncertain"), l.ErrNoEffect, l.ErrCapacity} {
		name := "success"
		if failure != nil {
			name = failure.Error()
		}
		t.Run(name, func(t *testing.T) {
			controller, durable, base, _ := setup()
			provider := &receiptCloud{cloud: base, failure: failure}
			controller.Providers = map[string]l.Provider{"a": provider}
			err := controller.Step(context.Background(), durable.a.ID)
			if (err != nil) != (failure != nil) {
				t.Fatal("creation result misclassified", err)
			}
			if durable.a.ResourceID != "vm-1" || len(durable.a.Resources) != 1 || durable.a.Resources[0].ID != "vol-created" {
				t.Fatal("creation receipt was not persisted", durable.a.ResourceID, durable.a.Resources)
			}
			expected := l.Running
			if failure != nil {
				expected = l.Creating
			}
			if durable.a.Phase != expected || len(durable.a.Outcomes) != 0 || provider.created != 1 {
				t.Fatal("known effects authorized another pool or lost uncertainty", durable.a.Phase, durable.a.Outcomes, provider.created)
			}
		})
	}
}
