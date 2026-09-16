package operator

import (
	"context"

	"github.com/actions/scaleset"
)

// runnerDeregistrar implements lifecycle.RunnerDeregistrar against the same
// *scaleset.Client already used for JIT runner bootstrap - the scale-set
// listener protocol's own agent endpoints, not the public REST API, so no
// owner/repo pairing is required (an allocation's Owner/Repo are only known
// once its job has started, which is not guaranteed for an allocation that
// never got past Pending).
type runnerDeregistrar struct {
	client *scaleset.Client
}

// DeregisterRunner looks up the claimed runner registration by name (the
// allocation ID) and removes it. A nil client is a complete no-op - New
// never actually constructs a runnerDeregistrar around a nil client (it
// leaves Controller.Runners nil instead - see New's own comment), so this
// case is unreachable through that path today, but the guard is kept as
// defense in depth for any future or test-only construction of this type
// directly.
func (d *runnerDeregistrar) DeregisterRunner(ctx context.Context, id string) error {
	if d.client == nil {
		return nil
	}
	ref, err := d.client.GetRunnerByName(ctx, id)
	if err != nil {
		return err
	}
	if ref == nil {
		return nil
	}
	return d.client.RemoveRunner(ctx, int64(ref.ID))
}
