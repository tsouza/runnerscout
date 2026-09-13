package operator

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/placement"
	"github.com/tsouza/runnerscout/internal/provider"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
)

type controlledCloud struct {
	exists  bool
	unknown bool
	creates int
	deletes int
}

func TestRecoveryWithoutGitHubPreservesAcceptedJobsAndDeadlines(t *testing.T) {
	op, cloud := controlledOperator()
	ctx := context.Background()
	if _, err := op.HandleDesiredRunnerCount(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := op.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := op.Store.List(ctx)
	if err != nil || len(before) != 1 {
		t.Fatal("missing admitted allocation", err)
	}
	bounded, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := op.RunRecovery(bounded); err != context.DeadlineExceeded {
		t.Fatal("recovery did not stop on cancellation", err)
	}
	after, err := op.Store.List(ctx)
	if err != nil || len(after) != 1 || after[0].Retire || after[0].Deadline != before[0].Deadline || cloud.deletes != 0 {
		t.Fatal("recovery retired accepted work or changed its deadline", err)
	}
	if !op.paused || op.draining {
		t.Fatal("recovery did not preserve paused admission semantics")
	}
	// The real provider bootstrap must fail safely before cloud creation when
	// recovery has no GitHub client; it must not dereference a nil client.
	command := op.Controller.Providers["z-gcp"].(*provider.Command)
	if material, err := command.Bootstrap(ctx, "recovery-pending"); err == nil || material != "" {
		t.Fatal("offline recovery produced GitHub bootstrap material")
	}
}

func (p *controlledCloud) Create(context.Context, lifecycle.Allocation) (string, error) {
	p.creates++
	p.exists = true
	return "fixture-vm", nil
}
func (p *controlledCloud) Observe(context.Context, lifecycle.Allocation) (lifecycle.Observation, error) {
	return lifecycle.Observation{Known: !p.unknown, Exists: p.exists, ResourceID: "fixture-vm"}, nil
}
func (p *controlledCloud) Delete(context.Context, lifecycle.Allocation) error {
	p.deletes++
	return nil // A successful request does not make the VM absent.
}

func controlledOperator() (*Operator, *controlledCloud) {
	cfg := namedCredentialConfig()
	cfg.Catalog = placement.Catalog{Complete: map[string]bool{"a-aws": true, "z-gcp": true}, Offerings: []placement.Offering{{ID: "pool", Provider: "a-aws", Region: "r", Zone: "z", Machine: "machine", Image: "image", CPU: 1, MemoryMiB: 1, Architecture: "amd64", Spot: true, PriceMicros: 1, Currency: "USD", ObservedAt: time.Now()}}}
	client := fake.NewClientset()
	// The default tracker omits resourceVersion. Assign versions so this
	// sequential control test uses the real store's update path. Conflict
	// rejection is independently qualified against a real API server.
	version := 0
	client.PrependReactor("*", "configmaps", func(action kt.Action) (bool, runtime.Object, error) {
		var object metav1.Object
		switch action := action.(type) {
		case kt.CreateAction:
			object = action.GetObject().(metav1.Object)
		case kt.UpdateAction:
			object = action.GetObject().(metav1.Object)
		default:
			return false, nil, nil
		}
		version++
		object.SetResourceVersion(strconv.Itoa(version))
		return false, nil, nil
	})
	op := New(cfg, client, nil)
	cloud := &controlledCloud{}
	op.Controller.Providers["a-aws"] = cloud
	return op, cloud
}

func TestSuspensionPreservesAcceptedAdmissionsAndDeadlines(t *testing.T) {
	ctx := context.Background()
	op, cloud := controlledOperator()
	if count, err := op.HandleDesiredRunnerCount(ctx, 1); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	_, before, err := op.loadFleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	var deadline time.Time
	for key, allocation := range before.Pending {
		id, deadline = key, allocation.Deadline
	}
	op.PauseAdmissions(true)
	if count, err := op.HandleDesiredRunnerCount(ctx, 2); err != nil || count != 1 {
		t.Fatal("suspension admitted new capacity", count, err)
	}
	if err := op.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if cloud.creates != 1 {
		t.Fatal("suspension lost previously accepted allocation")
	}
	a, err := op.Store.Load(ctx, id)
	if err != nil || a.Deadline != deadline {
		t.Fatal("suspension reset provisioning deadline", err)
	}
	_, after, err := op.loadFleet(ctx)
	if err != nil || after.Admission != before.Admission {
		t.Fatal("suspended demand changed bounded admission state", err)
	}
	op.PauseAdmissions(false)
	if count, err := op.HandleDesiredRunnerCount(ctx, 1); err != nil || count != 1 {
		t.Fatal("resume rearmed unchanged demand", count, err)
	}
}

func TestDrainRequiresObservedAbsenceAndCannotBeResumed(t *testing.T) {
	ctx := context.Background()
	op, cloud := controlledOperator()
	if _, err := op.HandleDesiredRunnerCount(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := op.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	op.Drain()
	op.PauseAdmissions(false)
	if count, err := op.HandleDesiredRunnerCount(ctx, 2); err != nil || count != 1 {
		t.Fatal("deletion resumed admissions", count, err)
	}
	if err := op.Tick(ctx); err != nil {
		t.Fatal(err)
	} // Retire the running allocation.
	if err := op.Tick(ctx); err != nil {
		t.Fatal(err)
	} // Issue deletion, but leave it visible.
	if cloud.deletes != 1 {
		t.Fatal("drain did not request resource cleanup")
	}
	if done, err := op.Drained(ctx); err != nil || done {
		t.Fatal("delete request incorrectly confirmed drain", err)
	}
	cloud.unknown = true
	if err := op.Tick(ctx); err == nil {
		t.Fatal("unknown cleanup observation accepted")
	}
	if done, err := op.Drained(ctx); err != nil || done {
		t.Fatal("unknown resource state released drain", err)
	}
	cloud.unknown, cloud.exists = false, false
	if err := op.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if done, err := op.Drained(ctx); err != nil || !done {
		t.Fatal("observed cleanup did not complete drain", err)
	}
	if count, err := op.HandleDesiredRunnerCount(ctx, 2); err != nil || count != 0 {
		t.Fatal("drained worker accepted new capacity", count, err)
	}
	if cloud.creates != 1 {
		t.Fatal("draining issued another create")
	}
}

func TestDrainRetiresPendingWorkAndRejectsMissingRecords(t *testing.T) {
	ctx := context.Background()
	op, cloud := controlledOperator()
	if _, err := op.HandleDesiredRunnerCount(ctx, 1); err != nil {
		t.Fatal(err)
	}
	op.Drain()
	if done, err := op.Drained(ctx); err != nil || done {
		t.Fatal("pending intent was considered drained", err)
	}
	if err := op.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if cloud.creates != 0 {
		t.Fatal("deletion created a VM for pending work")
	}
	if done, err := op.Drained(ctx); err != nil || !done {
		t.Fatal("retired no-effect intent did not drain", err)
	}
	cm, fleet, err := op.loadFleet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fleet.Created["missing-allocation"] = time.Now()
	if err := op.saveFleet(ctx, cm, fleet); err != nil {
		t.Fatal(err)
	}
	if done, err := op.Drained(ctx); err != nil || done {
		t.Fatal("missing durable record was treated as observed absence", err)
	}
}
