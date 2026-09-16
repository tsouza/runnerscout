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
	// UID distinguishes resource generations when a provider reuses resource IDs.
	UID string `json:"uid,omitempty"`
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
	// RunID, Owner, Repo and ScaleSetJobID are captured once, from the job's
	// first start event, and never overwritten - each allocation runs exactly
	// one GitHub Actions job. They durably identify that job for later REST
	// evidence lookups; RunID is zero until captured.
	RunID         int64  `json:"runID,omitempty"`
	Owner         string `json:"owner,omitempty"`
	Repo          string `json:"repo,omitempty"`
	ScaleSetJobID string `json:"scaleSetJobID,omitempty"`
	// RetryProcessed marks a confirmed interruption as already evaluated for a
	// bounded rerun, whatever the outcome, so it is never re-evaluated.
	RetryProcessed bool `json:"retryProcessed,omitempty"`
	// NetworkProfile names the NetworkProfile this allocation's overlay
	// membership belongs to. It is only ever non-empty for a "wireguard" mode
	// NetworkProfile; internal/configapi/compile.go's network() rejects every
	// other NetworkProfileSpec.Mode, and "separate" mode itself never sets
	// this field on an allocation (it has no peer overlay to join).
	// WireGuardPublicKey and WireGuardOverlayAddress are that allocation's
	// checkpointed overlay identity: generated once in controller memory at
	// the Pending->Creating transition and persisted here exactly like
	// ResourceID/Resources already are. The private key itself is never
	// checkpointed - it is consumed once into cloud-init and discarded
	// (docs/networking-peer-model.md, "Peer trust" and "Secret shape").
	NetworkProfile          string `json:"networkProfile,omitempty"`
	WireGuardPublicKey      []byte `json:"wireGuardPublicKey,omitempty"`
	WireGuardOverlayAddress string `json:"wireGuardOverlayAddress,omitempty"`
	// WireGuardEndpoint is this allocation's outer, dialable "host:port"
	// address for its WireGuard UDP socket - distinct from
	// WireGuardOverlayAddress, which is the peer's INNER address routed
	// inside the tunnel via AllowedIPs. It is the resolution to the gap
	// internal/wireguard/tunnel.Peer's own doc comment and
	// docs/networking-peer-model.md's "What this document does not decide"
	// section left open: within the single directly-routable private
	// network this codebase already requires an allocation's NetworkMapping
	// to provision (the same Subnet/SecurityGroup/NetworkID a NetworkProfile
	// already references), a peer's outer endpoint can simply be that VM's
	// already-cloud-assigned private IP on internal/wireguard.DefaultListenPort
	// - every VM in one such mapping can already reach every other directly,
	// with no NAT traversal or "roaming" needed. Populated once, from data
	// each provider's create call already receives for its own other
	// purposes (see Creation.WireGuardEndpoint's doc comment for exactly
	// what each provider captures today). Left empty (never inferred, never
	// synthesized) when unknown - Snapshot below just omits it from that
	// peer's entry, which leaves the field unset in a Peer, not the peer
	// excluded; a still-empty Endpoint falls back to WireGuard's own
	// unset-endpoint "roaming" behavior, so a peer with an as-yet-uncaptured
	// endpoint is not otherwise broken. This does NOT resolve reachability
	// across two different NetworkMappings (a NetworkProfile may reference
	// mappings on different providers or regions - api/v1alpha1.NetworkMapping
	// is a list, MaxItems=32, precisely to allow that): private IPs in
	// different clouds'/regions' subnets are not mutually routable without a
	// VPN/peering this codebase's own design deliberately never provisions
	// (docs/networking-control-plane.md: "never authorizes the controller to
	// create a paid gateway"). Making WireGuard work across mappings is a
	// separate, larger networking-topology decision this field does not
	// make; it is safe as far as it goes because compile.go's network()
	// rejects any wireguard-mode NetworkProfile referencing more than one
	// NetworkMapping, so no allocation this codebase can produce exercises a
	// multi-mapping profile.
	WireGuardEndpoint string `json:"wireGuardEndpoint,omitempty"`
	// WireGuardPollTokenHash is the SHA-256 hash
	// (internal/wireguard.HashPollToken) of the bearer token this
	// allocation's VM presents to the controller's WireGuard peer-poll
	// endpoint (docs/networking-peer-model.md's "Revocation" section). Only
	// the hash is checkpointed here, never the raw token: this codebase's
	// checkpoint layer (internal/state.Kubernetes) persists Allocation as a
	// plain Kubernetes ConfigMap, not a Secret, and its existing
	// Secret/ConfigMap split already treats ConfigMap-readable data as
	// materially less protected than Secret-readable data (see
	// internal/configapi/compile.go's secret() helper and
	// docs/networking-peer-model.md's "Secret shape" section) - persisting
	// the raw bearer credential here would hand read access to it to anyone
	// with the (typically much broader) RBAC permission to list ConfigMaps.
	// The raw token itself is generated once and consumed immediately into
	// cloud-init, exactly like WireGuardPublicKey's private-key counterpart.
	WireGuardPollTokenHash []byte `json:"wireGuardPollTokenHash,omitempty"`
	// TerminalAt is set once, the moment Phase first becomes Deleted or
	// TimedOut, and never touched again - it is not a general last-transition
	// timestamp for every phase change. It exists so a caller (see
	// internal/operator's terminal-record pruning) can tell how long an
	// allocation has been definitively inert without inferring that from
	// Deadline, which keeps its original Pending-phase meaning even after a
	// later terminal transition.
	TerminalAt time.Time `json:"terminalAt,omitempty"`
}

var ErrConflict = errors.New("state revision conflict")
var ErrCapacity = errors.New("definitive capacity rejection")

// ErrNoEffect is returned only when preparation failed before any cloud create
// request. It must never classify an ambiguous transport or provider response.
var ErrNoEffect = errors.New("create preparation failed without cloud effects")

type Observation struct {
	// Resources contains independently proven identities, including partial
	// evidence that must survive an uncertain creation-recovery outcome.
	Resources []ResourceReference
	Exists    bool
	Known     bool
	// Interrupted is set only when the provider definitively confirms the
	// resource's absence was a spot interruption, never inferred from a bare
	// disappearance. It carries no meaning when Exists is true.
	Interrupted bool
	ResourceID  string
}

// Creation retains identities returned by the create operation itself, before a
// later observation or interruption can hide its dependent resources.
type Creation struct {
	ResourceID string
	Resources  []ResourceReference
	// WireGuardPublicKey carries a freshly minted overlay public key back to
	// Step for checkpointing onto Allocation.WireGuardPublicKey, the same way
	// ResourceID/Resources already flow from a create attempt into the
	// durable Allocation record. No provider sets this today - it stays
	// empty for every allocation until NetworkProfile becomes reachable (see
	// Allocation.NetworkProfile).
	WireGuardPublicKey []byte
	// WireGuardPollTokenHash carries a freshly minted poll token's SHA-256
	// hash back to Step for checkpointing onto
	// Allocation.WireGuardPollTokenHash, the same way WireGuardPublicKey
	// above does for the public key. No provider sets this today for the
	// same reachability reason WireGuardPublicKey isn't (see
	// Allocation.NetworkProfile).
	WireGuardPollTokenHash []byte
	// WireGuardEndpoint carries this allocation's own outer "host:port"
	// dial address back to Step for checkpointing onto
	// Allocation.WireGuardEndpoint - see that field's doc comment for what
	// it means and its known limits. Unlike WireGuardPublicKey/
	// WireGuardPollTokenHash (minted once in controller memory before the
	// cloud create call is even made), this value only exists once the
	// cloud provider has actually assigned the VM a private IP, so it is
	// captured from the create call's own response rather than generated:
	// internal/provider's AWS adapter sets it (the private IP is already
	// present in the exact RunInstances response it already parses for
	// other fields); the Azure adapter sets it too (the NIC's private IP -
	// properties.ipConfigurations[].properties.privateIPAddress - is
	// already present in the exact network interface GET response
	// azureInventory already performs for ownership/UID verification, so
	// this costs no new call either). The GCP adapter sets it too, but
	// differently from its two siblings: its creation path
	// (Instances.Insert, then polling ZoneOperations.Get/List) never
	// receives a compute.Instance response at all - only compute.Operation
	// ones, which carry no NetworkInterfaces field - so capturing this for
	// GCP requires one genuinely new post-create Instances.Get call
	// (gcpCaptureWireGuardEndpoint). That call is made only when
	// a.NetworkProfile != "", the same central gate this field's capture
	// already sits behind for every provider (see Command.
	// CreateWithResources's clearing of this field), so it costs literally
	// nothing for any allocation this codebase's configuration path can
	// produce today; a failed or incomplete Get is best-effort and never
	// fails the creation, matching how AWS/Azure already tolerate their own
	// equivalent edge cases (an unexpected NetworkInterfaces count).
	// gcpInventory's own separate Instances.Get (used during observe/
	// delete) also carries NetworkInterfaces[].NetworkIP, but is not reused
	// here - it performs a second Disks.Get this capture does not need. No
	// allocation this codebase can produce today reaches any of this
	// regardless (see Allocation.NetworkProfile).
	WireGuardEndpoint string
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

// RunnerDeregistrar deregisters a claimed GitHub Actions runner registration by
// allocation ID (the runner name GitHub pre-registered when it claimed the
// job), the moment an allocation reaches TimedOut with no cloud resource ever
// confirmed created. It must no-op when the registration is already gone
// (picked up by something else, never actually claimed, or already removed by
// a prior attempt) - only a definitive removal or confirmed absence may
// return nil; any other outcome must return an error so Step leaves the
// allocation in Pending for a later retry rather than persisting TimedOut
// over an unconfirmed orphaned registration.
type RunnerDeregistrar interface {
	DeregisterRunner(ctx context.Context, id string) error
}

type Controller struct {
	Cooldowns map[string]time.Time
	Store     Store
	Providers map[string]Provider
	Now       func() time.Time
	// Runners deregisters an allocation's claimed GitHub runner registration
	// before Step persists any Deleted/TimedOut transition (see
	// RunnerDeregistrar and deregister below). nil is a complete no-op,
	// matching every other optional integration point in this codebase (e.g.
	// operator.Operator's AzureInterruptions) - no allocation's transition is
	// otherwise affected.
	Runners RunnerDeregistrar
}

// deregister calls Runners.DeregisterRunner, if configured, before Step
// persists a Deleted/TimedOut transition. GitHub's scale-set listener
// protocol registers a claimed runner name at GenerateJitRunnerConfig time -
// on or before every path that can reach Creating - so every one of Step's
// four Deleted/TimedOut transitions can be leaving behind a claimed
// registration, not just the Pending -> TimedOut path: a hard spot
// interruption or a forced cloud Delete never gives the runner process a
// graceful shutdown to self-deregister, and even a confirmed-absent Creating
// outcome can follow a successful registration if the cloud create call
// itself failed after Bootstrap already succeeded. DeregisterRunner is
// required to no-op when the registration is already gone, so calling it
// unconditionally here - regardless of which path is certain to have
// registered one - costs nothing beyond one extra lookup on an already-rare
// terminal transition. Every caller must return this error unsaved on
// failure: persisting the phase transition anyway would strand the orphaned
// registration permanently, since Step treats Deleted/TimedOut as permanent
// no-ops from then on.
func (c *Controller) deregister(ctx context.Context, id string) error {
	if c.Runners == nil {
		return nil
	}
	return c.Runners.DeregisterRunner(ctx, id)
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
			if err := c.deregister(ctx, a.ID); err != nil {
				return err
			}
			a.Phase = TimedOut
			a.Condition = "LocalProvisioningTimeout"
			a.TerminalAt = now
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
		if len(creation.WireGuardPublicKey) > 0 {
			a.WireGuardPublicKey = creation.WireGuardPublicKey
		}
		if len(creation.WireGuardPollTokenHash) > 0 {
			a.WireGuardPollTokenHash = creation.WireGuardPollTokenHash
		}
		if creation.WireGuardEndpoint != "" {
			a.WireGuardEndpoint = creation.WireGuardEndpoint
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
		// Recovery can return proven identities even when a later operation is
		// uncertain. Persist those obligations before reporting the uncertainty.
		if err := c.rememberResources(ctx, &a, ob.Resources); err != nil {
			return err
		}
		if e != nil || !ob.Known {
			return errors.New("create reconciliation unknown")
		}
		if !ob.Exists {
			// Confirmed absence (Known, !Exists) is a more resolved outcome
			// than the ambiguity that put this allocation in Creating in
			// the first place: the resource provably never existed, so
			// there is nothing to clean up. Deleted already means exactly
			// this elsewhere in this method - release unconditionally
			// (independent of Retire or expiry) so a fresh admission cycle
			// can replace this allocation instead of parking it here
			// forever with no recovery path.
			if err := c.deregister(ctx, a.ID); err != nil {
				return err
			}
			a.Phase = Deleted
			a.Condition = "CreateConfirmedAbsent"
			a.TerminalAt = now
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
			if err := c.deregister(ctx, a.ID); err != nil {
				return err
			}
			a.Phase = Deleted
			a.Condition = "ResourceAbsentInterruptionUnproven"
			if ob.Interrupted {
				a.Condition = "ResourceAbsentConfirmedInterruption"
			}
			a.TerminalAt = now
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
			if err := c.deregister(ctx, a.ID); err != nil {
				return err
			}
			a.Phase = Deleted
			a.Condition = "CleanupConfirmed"
			a.TerminalAt = now
			return save()
		}
		if err := p.Delete(ctx, a); err != nil {
			return errors.New("delete not confirmed; cleanup retained")
		}
		return nil
	}
	return errors.New("unknown allocation phase")
}
