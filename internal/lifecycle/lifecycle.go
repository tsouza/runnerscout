// Package lifecycle implements the durable allocation state machine. It has no
// GitHub cancellation operation: local provisioning expiry cannot cancel workflows.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"github.com/tsouza/runnerscout/internal/placement"
	"time"
)

type Phase string

const (
	Pending  Phase = "pending"
	Creating Phase = "creating"
	Running  Phase = "running"
	Deleting Phase = "deleting"
	Deleted  Phase = "deleted"
	TimedOut Phase = "timed-out"
)

type ResourceReference struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type Allocation struct {
	Resources    []ResourceReference          `json:"resources,omitempty"`
	RejectedAt   map[string]time.Time         `json:"rejectedAt,omitempty"`
	Completed    bool                         `json:"completed"`
	ID           string                       `json:"id"`
	Revision     string                       `json:"revision,omitempty"`
	Phase        Phase                        `json:"phase"`
	Deadline     time.Time                    `json:"deadline"`
	Attempts     int                          `json:"attempts"`
	MaxAttempts  int                          `json:"maxAttempts"`
	Offering     placement.Offering           `json:"offering"`
	Outcomes     map[string]placement.Outcome `json:"outcomes"`
	Catalog      placement.Catalog            `json:"catalog"`
	Requirements placement.Requirements       `json:"requirements"`
	ResourceID   string                       `json:"resourceID,omitempty"`
	Ready        bool                         `json:"ready"`
	Retire       bool                         `json:"retire"`
	Condition    string                       `json:"condition,omitempty"`
}

var ErrConflict = errors.New("state revision conflict")
var ErrCapacity = errors.New("definitive capacity rejection")

// ErrNoEffect is returned only when preparation failed before any cloud create
// request. It must never classify an ambiguous transport or provider response.
var ErrNoEffect = errors.New("create preparation failed without cloud effects")

type Observation struct {
	Resources  []ResourceReference
	Exists     bool
	Known      bool
	ResourceID string
}

// Creation retains identities returned by the create operation itself, before a
// later observation or interruption can hide its dependent resources.
type Creation struct {
	ResourceID string
	Resources  []ResourceReference
}
type ResourceCreator interface {
	CreateWithResources(context.Context, Allocation) (Creation, error)
}

// CreationReconciler may finish an already committed creation operation, but
// must never allocate a replacement. The controller fences it with a state CAS.
type CreationReconciler interface {
	ReconcileCreation(context.Context, Allocation) (Observation, error)
}

type Provider interface {
	Create(context.Context, Allocation) (string, error)
	Observe(context.Context, Allocation) (Observation, error)
	Delete(context.Context, Allocation) error
}
type Store interface {
	Load(context.Context, string) (Allocation, error)
	Save(context.Context, Allocation, string) (Allocation, error)
}
type Controller struct {
	Cooldowns map[string]time.Time
	Store     Store
	Providers map[string]Provider
	Now       func() time.Time
}

// Step makes at most one provider operation. A committed Creating intent survives
// crashes and must be observed; no blind create retry follows an ambiguous response.
func (c *Controller) Step(ctx context.Context, id string) error {
	a, err := c.Store.Load(ctx, id)
	if err != nil {
		return err
	}
	now := c.Now()
	save := func() error { _, e := c.Store.Save(ctx, a, a.Revision); return e }
	if a.ID == "" || a.Deadline.IsZero() || a.MaxAttempts < 1 || a.MaxAttempts > 10 {
		return errors.New("invalid persisted allocation")
	}
	expired := !now.Before(a.Deadline)
	if a.Phase == Deleted || a.Phase == TimedOut {
		return nil
	}
	if a.Phase == Pending {
		if a.ResourceID != "" || len(a.Resources) > 0 {
			return errors.New("pending allocation retains cloud resources")
		}
		if a.Retire || expired || a.Attempts >= a.MaxAttempts {
			a.Phase = TimedOut
			a.Condition = "LocalProvisioningTimeout"
			return save()
		}
		outcomes := make(map[string]placement.Outcome, len(a.Outcomes))
		for k, v := range a.Outcomes {
			outcomes[k] = v
		}
		for pool, until := range c.Cooldowns {
			if now.Before(until) && outcomes[pool] == "" {
				outcomes[pool] = placement.CoolingDown
			}
		}
		o, e := placement.Choose(now, a.Requirements, a.Catalog, outcomes)
		if e != nil {
			a.Condition = e.Error()
			return save()
		}
		p, ok := c.Providers[o.Provider]
		if !ok {
			return errors.New("provider configuration unavailable")
		}
		a.Offering = o
		a.Attempts++
		a.Phase = Creating
		a.Condition = "CreateCommitmentUnknown"
		a, err = c.Store.Save(ctx, a, a.Revision)
		if err != nil {
			return err
		}
		var creation Creation
		if creator, ok := p.(ResourceCreator); ok {
			creation, e = creator.CreateWithResources(ctx, a)
		} else {
			creation.ResourceID, e = p.Create(ctx, a)
		}
		empty := creation.ResourceID == "" && len(creation.Resources) == 0
		if errors.Is(e, ErrNoEffect) && empty {
			a.Phase = Pending
			a.Condition = "CreatePreparationFailed"
			return save()
		}
		if errors.Is(e, ErrCapacity) && empty {
			a.Phase = Pending
			if a.Outcomes == nil {
				a.Outcomes = map[string]placement.Outcome{}
			}
			a.Outcomes[o.ID] = placement.CapacityRejected
			if a.RejectedAt == nil {
				a.RejectedAt = map[string]time.Time{}
			}
			a.RejectedAt[o.ID] = now
			a.Condition = "CapacityRejected"
			return save()
		}
		if creation.ResourceID != "" {
			a.ResourceID = creation.ResourceID
		}
		resources, _, referenceError := MergeResources(a.Resources, creation.Resources)
		if referenceError != nil {
			if err := save(); err != nil {
				return err
			}
			return referenceError
		}
		a.Resources = resources
		if e != nil || creation.ResourceID == "" {
			if !empty {
				if err := save(); err != nil {
					return err
				}
			}
			return fmt.Errorf("create commitment unknown for %s", a.ID)
		}
		a.Phase = Running
		a.Condition = "VMCreated"
		return save()
	}
	p, ok := c.Providers[a.Offering.Provider]
	if !ok {
		return errors.New("provider configuration unavailable; cleanup retained")
	}
	if a.Phase == Creating {
		var ob Observation
		var e error
		if recovery, ok := p.(CreationReconciler); ok {
			a, err = c.Store.Save(ctx, a, a.Revision)
			if err != nil {
				return err
			}
			ob, e = recovery.ReconcileCreation(ctx, a)
		} else {
			ob, e = p.Observe(ctx, a)
		}
		if e != nil || !ob.Known {
			return errors.New("create reconciliation unknown")
		}
		if err := c.rememberResources(ctx, &a, ob.Resources); err != nil {
			return err
		}
		if !ob.Exists {
			a.Condition = "CreateAbsenceNotCommitmentProof"
			if expired {
				a.Condition = "LocalProvisioningTimeoutCommitmentUnknown"
			}
			return save()
		}
		a.ResourceID = ob.ResourceID
		a.Phase = Running
		if a.Retire || (expired && !a.Ready) {
			a.Phase = Deleting
			a.Condition = "LocalProvisioningTimeoutCleanupPending"
		}
		return save()
	}
	if a.Phase == Running {
		if a.Retire || (expired && !a.Ready) {
			a.Phase = Deleting
			a.Condition = "CleanupPending"
			return save()
		}
		ob, e := p.Observe(ctx, a)
		if e != nil || !ob.Known {
			return errors.New("resource observation unknown")
		}
		if err := c.rememberResources(ctx, &a, ob.Resources); err != nil {
			return err
		}
		if !ob.Exists {
			a.Phase = Deleted
			a.Condition = "ResourceAbsentInterruptionUnproven"
			return save()
		}
		return nil
	}
	if a.Phase == Deleting {
		ob, e := p.Observe(ctx, a)
		if e != nil || !ob.Known {
			return errors.New("cleanup observation unknown")
		}
		if err := c.rememberResources(ctx, &a, ob.Resources); err != nil {
			return err
		}
		if !ob.Exists {
			a.Phase = Deleted
			a.Condition = "CleanupConfirmed"
			return save()
		}
		if err := p.Delete(ctx, a); err != nil {
			return errors.New("delete not confirmed; cleanup retained")
		}
		return nil
	}
	return errors.New("unknown allocation phase")
}
