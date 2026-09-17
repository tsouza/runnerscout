package operator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/tsouza/runnerscout/internal/azurequeue"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/placement"
	"github.com/tsouza/runnerscout/internal/provider"
	"k8s.io/client-go/kubernetes/fake"
)

// fakeAzureInterruptions is a local azureInterruptionObserver test double,
// mirroring fakeAWSPrices/fakeAzurePrices in prices_test.go. hang, when set,
// blocks until ctx is done and returns ctx.Err() - proving
// pollAzureInterruptions supplies its own bounded context (externalCallBudget)
// rather than the bare ctx Tick itself was given, mirroring
// TestRefreshAWSPricesBoundsAnUnresponsiveObserver in prices_test.go.
type fakeAzureInterruptions struct {
	results []azurequeue.Result
	err     error
	calls   int
	hang    bool
}

func (f *fakeAzureInterruptions) Poll(ctx context.Context) ([]azurequeue.Result, error) {
	f.calls++
	if f.hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.results, f.err
}

// fakeAzureToken is a minimal azcore.TokenCredential, mirroring
// internal/provider's own testAzureToken - duplicated locally rather than
// exported from internal/provider purely for this cross-package test, since
// internal/provider's copy also enforces test-specific scope assertions
// this package has no need for.
type fakeAzureToken struct{}

func (fakeAzureToken) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "test-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

const azureInterruptionSubscription = "sub"
const azureInterruptionResourceGroup = "rg"

// azureVMResourceID builds the exact ARM resource ID azureID/observeAzure's
// own correlation would build for id, given
// azureInterruptionProviderConfig's Subscription/ResourceGroup.
func azureVMResourceID(id string) string {
	return "/subscriptions/" + azureInterruptionSubscription + "/resourceGroups/" + azureInterruptionResourceGroup + "/providers/Microsoft.Compute/virtualMachines/" + id
}

// azureAbsentSDK builds a real *provider.AzureSDK wired to a fake ARM
// transport that reports every allocation's deployment as gone
// (DeploymentNotFound, terminal per azureTerminal) and its resource group as
// empty - i.e. every azure allocation this Tick observes is confirmed
// absent, the only precondition under which Observation.Interrupted is ever
// meaningful (lifecycle.go's Observation doc comment).
func azureAbsentSDK(t *testing.T) *provider.AzureSDK {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := strings.ToLower(r.URL.Path)
		if strings.Contains(path, "/microsoft.resources/deployments/") {
			w.WriteHeader(404)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "DeploymentNotFound"}})
			return
		}
		if strings.HasSuffix(path, "/resources") {
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{}})
			return
		}
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "ResourceNotFound"}})
	}))
	t.Cleanup(server.Close)
	options := &arm.ClientOptions{ClientOptions: policy.ClientOptions{Transport: server.Client(), Retry: policy.RetryOptions{MaxRetries: -1}, Cloud: cloud.Configuration{ActiveDirectoryAuthorityHost: cloud.AzurePublic.ActiveDirectoryAuthorityHost, Services: map[cloud.ServiceName]cloud.ServiceConfiguration{cloud.ResourceManager: {Audience: "https://management.azure.com", Endpoint: server.URL}}}}}
	return &provider.AzureSDK{Credential: fakeAzureToken{}, Options: options}
}

func azureInterruptionProviderConfig() provider.Config {
	return provider.Config{Kind: "azure", Owner: "test", Subscription: azureInterruptionSubscription, ResourceGroup: azureInterruptionResourceGroup, Subnet: "/subnet", SecurityGroup: "/nsg", SSHPublicKey: "ssh-ed25519 test"}
}

// azureInterruptionOperator builds a real *Operator (via New, so
// Controller.Providers holds a real *provider.Command for "azure") whose
// Azure ARM transport is faked to report every allocation absent, matching
// this test file's end-to-end scope: proving the per-Tick wiring, not
// exercising the ARM SDK itself (already covered in internal/provider).
func azureInterruptionOperator(t *testing.T) *Operator {
	t.Helper()
	cfg := Config{Name: "test", Namespace: "test", MaxRunners: 10, ProvisioningSeconds: 60, MaxLifetimeSeconds: 3600,
		Providers: map[string]provider.Config{"azure": azureInterruptionProviderConfig()}}
	o := New(cfg, fake.NewClientset(), nil)
	command := o.Controller.Providers["azure"].(*provider.Command)
	command.Azure = azureAbsentSDK(t)
	return o
}

// seedRunningAzureAllocation directly checkpoints a Running-phase azure/spot
// allocation and its fleet lifetime origin (f.Created), bypassing
// HandleDesiredRunnerCount's Pending/Creating admission machinery entirely -
// this test file exercises Tick's Running-phase Observe path, not
// admission.
func seedRunningAzureAllocation(t *testing.T, ctx context.Context, o *Operator, id string) {
	t.Helper()
	seedAllocation(t, ctx, o.Store, lifecycle.Allocation{
		ID: id, Phase: lifecycle.Running, Deadline: time.Now().Add(time.Hour), MaxAttempts: 3,
		Offering: placement.Offering{Provider: "azure", Spot: true},
	})
	cm, f, e := o.loadFleet(ctx)
	if e != nil {
		t.Fatal(e)
	}
	f.Created[id] = time.Now()
	if e := o.saveFleet(ctx, cm, f); e != nil {
		t.Fatal(e)
	}
}

// TestTickAppliesPerCycleAzureInterruptionSnapshotByExactResourceID is the
// full per-Tick flow: one fake azureInterruptionObserver.Poll seeds two
// results (one matching "rs-hit", one for an unrelated resource ID no
// allocation here has), two allocations are seeded ("rs-hit" and
// "rs-miss"), and only "rs-hit" - the exact resource ID match - must come
// out of Tick confirmed interrupted.
func TestTickAppliesPerCycleAzureInterruptionSnapshotByExactResourceID(t *testing.T) {
	ctx := context.Background()
	o := azureInterruptionOperator(t)
	seedRunningAzureAllocation(t, ctx, o, "rs-hit")
	seedRunningAzureAllocation(t, ctx, o, "rs-miss")
	o.AzureInterruptions = &fakeAzureInterruptions{results: []azurequeue.Result{
		{ResourceID: azureVMResourceID("rs-hit"), Preempted: true},
		{ResourceID: azureVMResourceID("rs-unrelated"), Preempted: true},
	}}
	if err := o.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	hit, err := o.Store.Load(ctx, "rs-hit")
	if err != nil || hit.Phase != lifecycle.Deleted || hit.Condition != "ResourceAbsentConfirmedInterruption" {
		t.Fatalf("exact resource ID match must be confirmed interrupted: %+v %v", hit, err)
	}
	miss, err := o.Store.Load(ctx, "rs-miss")
	if err != nil || miss.Phase != lifecycle.Deleted || miss.Condition != "ResourceAbsentInterruptionUnproven" {
		t.Fatalf("non-matching allocation must never be confirmed interrupted: %+v %v", miss, err)
	}
}

// TestTickNilAzureInterruptionsIsCompleteNoOp is the AWSPrices/AzurePrices-
// style nil-is-inert regression test: with Operator.AzureInterruptions left
// at its default nil (today's behavior for every deployment), an allocation
// whose resource ID would have matched had the observer been configured
// must still never be confirmed interrupted.
func TestTickNilAzureInterruptionsIsCompleteNoOp(t *testing.T) {
	ctx := context.Background()
	o := azureInterruptionOperator(t)
	seedRunningAzureAllocation(t, ctx, o, "rs-hit")
	if err := o.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := o.Store.Load(ctx, "rs-hit")
	if err != nil || got.Phase != lifecycle.Deleted || got.Condition != "ResourceAbsentInterruptionUnproven" {
		t.Fatalf("nil AzureInterruptions must never confirm an interruption: %+v %v", got, err)
	}
}

func TestPollAzureInterruptionsNilObserverReturnsNilMap(t *testing.T) {
	o := &Operator{}
	if m := o.pollAzureInterruptions(context.Background()); m != nil {
		t.Fatalf("nil observer must return a nil map, got %v", m)
	}
}

func TestPollAzureInterruptionsPollErrorReturnsNilMap(t *testing.T) {
	o := &Operator{AzureInterruptions: &fakeAzureInterruptions{err: errors.New("boom")}}
	if m := o.pollAzureInterruptions(context.Background()); m != nil {
		t.Fatalf("a failed poll must return a nil map, got %v", m)
	}
}

// A real production incident: a leader pod stuck with idle CPU and no log
// line for 49+ minutes, because nothing bounded a sequential external call
// under runLeader's unbounded, cancel-only ctx. This is the same mechanism
// as externalCallBudget's other call sites - see that var's own comment
// (operator.go) for the full, current list - found only by auditing every
// external call in this package rather than trusting an earlier audit to
// have already been exhaustive.
func TestPollAzureInterruptionsBoundsAnUnresponsivePoll(t *testing.T) {
	previous := externalCallBudget
	externalCallBudget = 100 * time.Millisecond
	defer func() { externalCallBudget = previous }()

	o := &Operator{AzureInterruptions: &fakeAzureInterruptions{hang: true}}

	// ctx is cancel-only, deliberately with no deadline of its own -
	// matching leaderCtx/runCtx in production. A test-owned deadline here
	// would let the test still pass even if pollAzureInterruptions stopped
	// applying externalCallBudget itself.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan map[string]bool, 1)
	go func() { done <- o.pollAzureInterruptions(ctx) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pollAzureInterruptions did not bound the stalled poll - it hung past externalCallBudget")
	}
}

func TestPollAzureInterruptionsSkipsUnclassifiedMessagesAndORsDuplicates(t *testing.T) {
	o := &Operator{AzureInterruptions: &fakeAzureInterruptions{results: []azurequeue.Result{
		{Err: errors.New("malformed")},
		{ResourceID: "id-a", Preempted: false},
		{ResourceID: "id-a", Preempted: true},
		{ResourceID: "", Preempted: true},
	}}}
	m := o.pollAzureInterruptions(context.Background())
	if len(m) != 1 || !m["id-a"] {
		t.Fatalf("expected exactly {id-a: true}, got %v", m)
	}
}

func TestApplyAzureInterruptionsOnlyTouchesAzureKindProviders(t *testing.T) {
	cfg := Config{Name: "test", Namespace: "test", Providers: map[string]provider.Config{
		"azure": azureInterruptionProviderConfig(),
		"aws":   {Kind: "aws", Owner: "test", AccountID: "000000000000", Subnet: "private", SecurityGroup: "private"},
	}}
	o := New(cfg, fake.NewClientset(), nil)
	m := map[string]bool{"x": true}
	o.applyAzureInterruptions(m)
	azureCmd := o.Controller.Providers["azure"].(*provider.Command)
	awsCmd := o.Controller.Providers["aws"].(*provider.Command)
	if !reflect.DeepEqual(azureCmd.AzureInterrupted, m) {
		t.Fatalf("azure provider must be updated with this cycle's snapshot: %v", azureCmd.AzureInterrupted)
	}
	if awsCmd.AzureInterrupted != nil {
		t.Fatalf("non-azure provider must never be touched: %v", awsCmd.AzureInterrupted)
	}
}

func TestApplyAzureInterruptionsClearsStaleSnapshotWhenNil(t *testing.T) {
	cfg := Config{Name: "test", Namespace: "test", Providers: map[string]provider.Config{"azure": azureInterruptionProviderConfig()}}
	o := New(cfg, fake.NewClientset(), nil)
	azureCmd := o.Controller.Providers["azure"].(*provider.Command)
	azureCmd.AzureInterrupted = map[string]bool{"stale": true}
	o.applyAzureInterruptions(nil)
	if azureCmd.AzureInterrupted != nil {
		t.Fatalf("a nil poll result must clear any stale snapshot, got %v", azureCmd.AzureInterrupted)
	}
}
