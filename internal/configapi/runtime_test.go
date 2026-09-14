package configapi

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/operator"
	"github.com/tsouza/runnerscout/internal/placement"
	"github.com/tsouza/runnerscout/internal/provider"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
)

func awsPriceRefreshConfig(enabled bool, providers map[string]provider.Config, requirementProviders []string) operator.Config {
	return operator.Config{Name: "test", Namespace: "test", GitHubURL: "https://github.com/tsouza/runnerscout", ScaleSetID: 1, MaxRunners: 2, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600,
		Requirements:    placement.Requirements{CPU: 1, MemoryMiB: 1, Architecture: "amd64", MaxPriceMicros: 100, Providers: requirementProviders, Regions: []string{"r"}, Policy: "lowest-price"},
		Providers:       providers,
		AWSPriceRefresh: enabled,
	}
}

func TestNewWorkerWiresAWSPricesWhenExplicitlyEnabled(t *testing.T) {
	cfg := awsPriceRefreshConfig(true, map[string]provider.Config{"aws": {Kind: "aws", Owner: "test", AccountID: "000000000000", Subnet: "private", SecurityGroup: "private"}}, []string{"aws"})
	r := &Runtime{Client: fake.NewClientset()}
	credentials := Credentials{Providers: map[string]map[string]string{"aws": {"AWS_ACCESS_KEY_ID": "fixture-id", "AWS_SECRET_ACCESS_KEY": "fixture-secret"}}}
	worker, cleanup, err := r.newWorker(Resolved{Config: cfg}, credentials, CleanupMode, func(bool) {})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	op, ok := worker.(*operator.Operator)
	if !ok {
		t.Fatal("expected the real operator worker")
	}
	if op.AWSPrices == nil {
		t.Fatal("AWSPriceRefresh enabled but AWSPrices was not wired")
	}
}

func TestNewWorkerLeavesAWSPricesNilWhenDisabled(t *testing.T) {
	cfg := awsPriceRefreshConfig(false, map[string]provider.Config{"aws": {Kind: "aws", Owner: "test", AccountID: "000000000000", Subnet: "private", SecurityGroup: "private"}}, []string{"aws"})
	r := &Runtime{Client: fake.NewClientset()}
	credentials := Credentials{Providers: map[string]map[string]string{"aws": {"AWS_ACCESS_KEY_ID": "fixture-id", "AWS_SECRET_ACCESS_KEY": "fixture-secret"}}}
	worker, cleanup, err := r.newWorker(Resolved{Config: cfg}, credentials, CleanupMode, func(bool) {})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	op, ok := worker.(*operator.Operator)
	if !ok {
		t.Fatal("expected the real operator worker")
	}
	if op.AWSPrices != nil {
		t.Fatal("AWSPriceRefresh disabled but AWSPrices was wired anyway")
	}
}

// TestNewWorkerAlwaysWiresNetworkPeers proves provider.Command.NetworkPeers
// reaches the CRD-driven (Runtime) entry point too, not only the mounted
// -config entry point cmd/runnerscout/main.go exercises directly through
// operator.NewWithCredentials: Runtime.newWorker calls the exact same
// operator.NewWithCredentials, so this hook needs no separate wiring here -
// but unlike AWSPriceRefresh above, it is never gated behind a Config flag,
// so it must be wired unconditionally.
func TestNewWorkerAlwaysWiresNetworkPeers(t *testing.T) {
	cfg := awsPriceRefreshConfig(false, map[string]provider.Config{"aws": {Kind: "aws", Owner: "test", AccountID: "000000000000", Subnet: "private", SecurityGroup: "private"}}, []string{"aws"})
	cfg.NetworkProfile = "mesh"
	cfg.NetworkOverlayCIDRs = []string{"10.90.0.0/24"}
	r := &Runtime{Client: fake.NewClientset()}
	credentials := Credentials{Providers: map[string]map[string]string{"aws": {"AWS_ACCESS_KEY_ID": "fixture-id", "AWS_SECRET_ACCESS_KEY": "fixture-secret"}}}
	worker, cleanup, err := r.newWorker(Resolved{Config: cfg}, credentials, CleanupMode, func(bool) {})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	op, ok := worker.(*operator.Operator)
	if !ok {
		t.Fatal("expected the real operator worker")
	}
	command, ok := op.Controller.Providers["aws"].(*provider.Command)
	if !ok || command.NetworkPeers == nil {
		t.Fatal("provider.Command.NetworkPeers was not wired through the CRD-driven Runtime entry point")
	}
}

func TestNewWorkerRequiresConfiguredAWSProviderWhenEnabled(t *testing.T) {
	cfg := awsPriceRefreshConfig(true, map[string]provider.Config{"azure": {Kind: "azure", Owner: "test", Subnet: "private", Subscription: "sub", ResourceGroup: "rg", SecurityGroup: "sg", SSHPublicKey: "ssh-ed25519 AAAA"}}, []string{"azure"})
	r := &Runtime{Client: fake.NewClientset()}
	if _, _, err := r.newWorker(Resolved{Config: cfg}, Credentials{}, CleanupMode, func(bool) {}); err == nil {
		t.Fatal("expected an error when AWSPriceRefresh is enabled without a configured \"aws\" provider")
	}
}

func azurePriceRefreshConfig(enabled bool, providers map[string]provider.Config, requirementProviders []string) operator.Config {
	return operator.Config{Name: "test", Namespace: "test", GitHubURL: "https://github.com/tsouza/runnerscout", ScaleSetID: 1, MaxRunners: 2, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600,
		Requirements:      placement.Requirements{CPU: 1, MemoryMiB: 1, Architecture: "amd64", MaxPriceMicros: 100, Providers: requirementProviders, Regions: []string{"r"}, Policy: "lowest-price"},
		Providers:         providers,
		AzurePriceRefresh: enabled,
	}
}

func TestNewWorkerWiresAzurePricesWhenExplicitlyEnabled(t *testing.T) {
	cfg := azurePriceRefreshConfig(true, map[string]provider.Config{"azure": {Kind: "azure", Owner: "test", Subnet: "private", Subscription: "sub", ResourceGroup: "rg", SecurityGroup: "sg", SSHPublicKey: "ssh-ed25519 AAAA"}}, []string{"azure"})
	r := &Runtime{Client: fake.NewClientset()}
	credentials := Credentials{Providers: map[string]map[string]string{"azure": {"AZURE_CLIENT_ID": "fixture-client"}}}
	worker, cleanup, err := r.newWorker(Resolved{Config: cfg}, credentials, CleanupMode, func(bool) {})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	op, ok := worker.(*operator.Operator)
	if !ok {
		t.Fatal("expected the real operator worker")
	}
	if op.AzurePrices == nil {
		t.Fatal("AzurePriceRefresh enabled but AzurePrices was not wired")
	}
}

func TestNewWorkerLeavesAzurePricesNilWhenDisabled(t *testing.T) {
	cfg := azurePriceRefreshConfig(false, map[string]provider.Config{"azure": {Kind: "azure", Owner: "test", Subnet: "private", Subscription: "sub", ResourceGroup: "rg", SecurityGroup: "sg", SSHPublicKey: "ssh-ed25519 AAAA"}}, []string{"azure"})
	r := &Runtime{Client: fake.NewClientset()}
	credentials := Credentials{Providers: map[string]map[string]string{"azure": {"AZURE_CLIENT_ID": "fixture-client"}}}
	worker, cleanup, err := r.newWorker(Resolved{Config: cfg}, credentials, CleanupMode, func(bool) {})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	op, ok := worker.(*operator.Operator)
	if !ok {
		t.Fatal("expected the real operator worker")
	}
	if op.AzurePrices != nil {
		t.Fatal("AzurePriceRefresh disabled but AzurePrices was wired anyway")
	}
}

func TestNewWorkerRequiresConfiguredAzureProviderWhenEnabled(t *testing.T) {
	cfg := azurePriceRefreshConfig(true, map[string]provider.Config{"aws": {Kind: "aws", Owner: "test", AccountID: "000000000000", Subnet: "private", SecurityGroup: "private"}}, []string{"aws"})
	r := &Runtime{Client: fake.NewClientset()}
	if _, _, err := r.newWorker(Resolved{Config: cfg}, Credentials{}, CleanupMode, func(bool) {}); err == nil {
		t.Fatal("expected an error when AzurePriceRefresh is enabled without a configured \"azure\" provider")
	}
}

func azureInterruptionQueueConfig(url string, providers map[string]provider.Config, requirementProviders []string) operator.Config {
	return operator.Config{Name: "test", Namespace: "test", GitHubURL: "https://github.com/tsouza/runnerscout", ScaleSetID: 1, MaxRunners: 2, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600,
		Requirements:              placement.Requirements{CPU: 1, MemoryMiB: 1, Architecture: "amd64", MaxPriceMicros: 100, Providers: requirementProviders, Regions: []string{"r"}, Policy: "lowest-price"},
		Providers:                 providers,
		AzureInterruptionQueueURL: url,
	}
}

func TestNewWorkerWiresAzureInterruptionsWhenExplicitlySet(t *testing.T) {
	cfg := azureInterruptionQueueConfig("https://fixture.queue.core.windows.net/interruptions", map[string]provider.Config{"azure": {Kind: "azure", Owner: "test", Subnet: "private", Subscription: "sub", ResourceGroup: "rg", SecurityGroup: "sg", SSHPublicKey: "ssh-ed25519 AAAA"}}, []string{"azure"})
	r := &Runtime{Client: fake.NewClientset()}
	credentials := Credentials{Providers: map[string]map[string]string{"azure": {"AZURE_CLIENT_ID": "fixture-client"}}}
	worker, cleanup, err := r.newWorker(Resolved{Config: cfg}, credentials, CleanupMode, func(bool) {})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	op, ok := worker.(*operator.Operator)
	if !ok {
		t.Fatal("expected the real operator worker")
	}
	if op.AzureInterruptions == nil {
		t.Fatal("AzureInterruptionQueueURL set but AzureInterruptions was not wired")
	}
}

func TestNewWorkerLeavesAzureInterruptionsNilWhenUnset(t *testing.T) {
	cfg := azureInterruptionQueueConfig("", map[string]provider.Config{"azure": {Kind: "azure", Owner: "test", Subnet: "private", Subscription: "sub", ResourceGroup: "rg", SecurityGroup: "sg", SSHPublicKey: "ssh-ed25519 AAAA"}}, []string{"azure"})
	r := &Runtime{Client: fake.NewClientset()}
	credentials := Credentials{Providers: map[string]map[string]string{"azure": {"AZURE_CLIENT_ID": "fixture-client"}}}
	worker, cleanup, err := r.newWorker(Resolved{Config: cfg}, credentials, CleanupMode, func(bool) {})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	op, ok := worker.(*operator.Operator)
	if !ok {
		t.Fatal("expected the real operator worker")
	}
	if op.AzureInterruptions != nil {
		t.Fatal("AzureInterruptionQueueURL unset but AzureInterruptions was wired anyway")
	}
}

func TestNewWorkerRequiresConfiguredAzureProviderWhenInterruptionQueueSet(t *testing.T) {
	cfg := azureInterruptionQueueConfig("https://fixture.queue.core.windows.net/interruptions", map[string]provider.Config{"aws": {Kind: "aws", Owner: "test", AccountID: "000000000000", Subnet: "private", SecurityGroup: "private"}}, []string{"aws"})
	r := &Runtime{Client: fake.NewClientset()}
	if _, _, err := r.newWorker(Resolved{Config: cfg}, Credentials{}, CleanupMode, func(bool) {}); err == nil {
		t.Fatal("expected an error when AzureInterruptionQueueURL is set without a configured \"azure\" provider")
	}
}

type runtimeWorker struct {
	paused, draining, drained atomic.Bool
	started, finish, stopped  chan struct{}
	beforeStart               func() error
}

func (w *runtimeWorker) RunSession(ctx context.Context) error  { return w.run(ctx, false) }
func (w *runtimeWorker) RunRecovery(ctx context.Context) error { return w.run(ctx, false) }
func (w *runtimeWorker) RunCleanup(ctx context.Context) error  { return w.run(ctx, true) }
func (w *runtimeWorker) PauseAdmissions(paused bool)           { w.paused.Store(paused) }
func (w *runtimeWorker) Drain()                                { w.draining.Store(true); w.paused.Store(true) }
func (w *runtimeWorker) Drained(context.Context) (bool, error) { return w.drained.Load(), nil }
func (w *runtimeWorker) run(ctx context.Context, cleanup bool) error {
	defer close(w.stopped)
	if w.beforeStart != nil {
		if err := w.beforeStart(); err != nil {
			return err
		}
	}
	close(w.started)
	if cleanup {
		select {
		case <-ctx.Done():
		case <-w.finish:
		}
	} else {
		<-ctx.Done()
	}
	return nil
}

type runtimeFixture struct {
	r        *Runtime
	workers  []*runtimeWorker
	cleanups int
}

func newRuntimeFixture(t *testing.T) *runtimeFixture {
	t.Helper()
	reader := readerFixture(t)
	objects := make([]runtime.Object, 0, len(reader.objects))
	for _, object := range reader.objects {
		objects = append(objects, object)
	}
	_, client := credentialFixture(t)
	f := &runtimeFixture{r: &Runtime{Namespace: "test", Name: "build", Client: client, Dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)}}
	f.r.Factory = func(resolved Resolved, _ Credentials, mode WorkerMode, ready func(bool)) (Worker, func() error, error) {
		w := &runtimeWorker{started: make(chan struct{}), finish: make(chan struct{}), stopped: make(chan struct{})}
		w.PauseAdmissions(resolved.Suspend)
		w.beforeStart = func() error {
			if _, err := f.r.checkpoints().Read(context.Background()); err != nil {
				return err
			}
			if mode != CleanupMode {
				root, err := f.r.roots().Get(context.Background(), "build", metav1.GetOptions{})
				if err != nil {
					return err
				}
				if !slices.Contains(root.GetFinalizers(), Finalizer) {
					return errors.New("session started without finalizer")
				}
			}
			ready(true)
			return nil
		}
		f.workers = append(f.workers, w)
		return w, func() error { f.cleanups++; return nil }, nil
	}
	t.Cleanup(func() {
		if err := f.r.stop(); err != nil {
			t.Error(err)
		}
	})
	return f
}

func reconcileRuntime(t *testing.T, r *Runtime) {
	t.Helper()
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func awaitRuntime(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker transition did not complete")
	}
}
func mutateRoot(t *testing.T, r *Runtime, mutate func(*unstructured.Unstructured)) {
	t.Helper()
	root, err := r.roots().Get(context.Background(), r.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mutate(root)
	if _, err = r.roots().Update(context.Background(), root, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}
func runtimeReason(t *testing.T, r *Runtime) string {
	t.Helper()
	root, err := r.roots().Get(context.Background(), r.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conditions, _, _ := unstructured.NestedSlice(root.Object, "status", "conditions")
	for _, entry := range conditions {
		c := entry.(map[string]any)
		if c["type"] == "Ready" {
			return c["reason"].(string)
		}
	}
	return ""
}

func TestRuntimeProtectsBeforeSessionAndReloadsOnlySemanticChanges(t *testing.T) {
	f := newRuntimeFixture(t)
	reconcileRuntime(t, f.r)
	awaitRuntime(t, f.workers[0].started)
	reconcileRuntime(t, f.r)
	reconcileRuntime(t, f.r)
	if len(f.workers) != 1 || runtimeReason(t, f.r) != "Reconciled" {
		t.Fatal("status write restarted the session")
	}
	secret, err := f.r.Client.CoreV1().Secrets("test").Get(context.Background(), "github", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secret.ResourceVersion = "2"
	if _, err = f.r.Client.CoreV1().Secrets("test").Update(context.Background(), secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	reconcileRuntime(t, f.r)
	if len(f.workers) != 2 || f.cleanups != 1 {
		t.Fatal("secret rotation did not replace and clean the session")
	}
	awaitRuntime(t, f.workers[0].stopped)
	awaitRuntime(t, f.workers[1].started)
	mutateRoot(t, f.r, func(root *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(root.Object, "missing", "spec", "runnerClassRef", "name")
	})
	reconcileRuntime(t, f.r)
	if !f.workers[1].paused.Load() || f.workers[1].draining.Load() || len(f.workers) != 2 {
		t.Fatal("invalid reference must pause new admissions while preserving accepted jobs")
	}
}

func TestRuntimeCheckpointLossOrCorruptionPausesAndCannotBeReplaced(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "corrupt", true: "missing"}[missing], func(t *testing.T) {
			f := newRuntimeFixture(t)
			reconcileRuntime(t, f.r)
			awaitRuntime(t, f.workers[0].started)
			maps := f.r.Client.CoreV1().ConfigMaps("test")
			if missing {
				if err := maps.Delete(context.Background(), "build-configuration", metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			} else {
				cm, err := maps.Get(context.Background(), "build-configuration", metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				cm.Data["snapshot"] = "broken"
				if _, err = maps.Update(context.Background(), cm, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			reconcileRuntime(t, f.r)
			if !f.workers[0].paused.Load() || len(f.workers) != 1 {
				t.Fatal("checkpoint failure left admissions enabled")
			}
			if err := f.r.stop(); err != nil {
				t.Fatal(err)
			}
			reconcileRuntime(t, f.r)
			if len(f.workers) != 1 {
				t.Fatal("restart replaced the lost cleanup binding")
			}
		})
	}
}

func TestRuntimeRecoversAcceptedConfigurationWithoutDrainingJobs(t *testing.T) {
	f := newRuntimeFixture(t)
	reconcileRuntime(t, f.r)
	awaitRuntime(t, f.workers[0].started)
	if err := f.r.stop(); err != nil {
		t.Fatal(err)
	}
	if err := f.r.Client.CoreV1().Secrets("test").Delete(context.Background(), "github", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	mutateRoot(t, f.r, func(root *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(root.Object, "missing", "spec", "runnerClassRef", "name")
	})
	reconcileRuntime(t, f.r)
	if len(f.workers) != 2 {
		t.Fatal("accepted configuration was not recovered")
	}
	if f.r.worker.mode != RecoveryMode {
		t.Fatal("invalid configuration recovery attempted to open a GitHub session")
	}
	awaitRuntime(t, f.workers[1].started)
	if !f.workers[1].paused.Load() || f.workers[1].draining.Load() {
		t.Fatal("recovery must preserve accepted jobs without new admissions")
	}
	reconcileRuntime(t, f.r)
	if len(f.workers) != 2 {
		t.Fatal("recovery restarted on every poll")
	}
}

func TestRuntimeDeletionRequiresObservedDrainAndRetainsOtherFinalizers(t *testing.T) {
	f := newRuntimeFixture(t)
	reconcileRuntime(t, f.r)
	awaitRuntime(t, f.workers[0].started)
	mutateRoot(t, f.r, func(root *unstructured.Unstructured) {
		now := metav1.Now()
		root.SetDeletionTimestamp(&now)
		root.SetFinalizers(append(root.GetFinalizers(), "example.com/retain"))
	})
	if err := f.r.Client.CoreV1().Secrets("test").Delete(context.Background(), "github", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	reconcileRuntime(t, f.r)
	if len(f.workers) != 2 {
		t.Fatal("cleanup depended on GitHub credentials")
	}
	w := f.workers[1]
	awaitRuntime(t, w.started)
	if !w.draining.Load() {
		t.Fatal("cleanup did not request drain")
	}
	close(w.finish)
	awaitRuntime(t, f.r.worker.done)
	reconcileRuntime(t, f.r)
	if runtimeReason(t, f.r) != "CleanupPending" {
		t.Fatal("worker exit was mistaken for observed absence")
	}
	w.drained.Store(true)
	reconcileRuntime(t, f.r)
	root, err := f.r.roots().Get(context.Background(), "build", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(root.GetFinalizers(), []string{"example.com/retain"}) {
		t.Fatalf("incorrect finalizer removal: %v", root.GetFinalizers())
	}
	if _, err := f.r.checkpoints().Read(context.Background()); err != nil {
		t.Fatal("cleanup provenance removed:", err)
	}
}

func TestRuntimeFinalizerConflictRetriesWithoutRepeatingCloudCleanup(t *testing.T) {
	f := newRuntimeFixture(t)
	reconcileRuntime(t, f.r)
	awaitRuntime(t, f.workers[0].started)
	mutateRoot(t, f.r, func(root *unstructured.Unstructured) { now := metav1.Now(); root.SetDeletionTimestamp(&now) })
	reconcileRuntime(t, f.r)
	w := f.workers[1]
	awaitRuntime(t, w.started)
	w.drained.Store(true)
	close(w.finish)
	awaitRuntime(t, f.r.worker.done)
	conflict := true
	f.r.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("update", "runnerscalesets", func(action kt.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() == "" && conflict {
			conflict = false
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "runnerscalesets"}, "build", errors.New("changed"))
		}
		return false, nil, nil
	})
	if err := f.r.Reconcile(context.Background()); !apierrors.IsConflict(err) {
		t.Fatalf("expected finalizer conflict: %v", err)
	}
	reconcileRuntime(t, f.r)
	if len(f.workers) != 2 || runtimeReason(t, f.r) != "CleanupComplete" {
		t.Fatal("finalizer conflict repeated cloud cleanup")
	}
}

func TestRuntimeReplacementRetainsOldOwnershipWithoutCleanupLoop(t *testing.T) {
	f := newRuntimeFixture(t)
	reconcileRuntime(t, f.r)
	awaitRuntime(t, f.workers[0].started)
	mutateRoot(t, f.r, func(root *unstructured.Unstructured) { root.SetUID("replacement-uid") })
	reconcileRuntime(t, f.r)
	w := f.workers[1]
	awaitRuntime(t, w.started)
	w.drained.Store(true)
	close(w.finish)
	awaitRuntime(t, f.r.worker.done)
	for range 3 {
		reconcileRuntime(t, f.r)
	}
	if len(f.workers) != 2 || runtimeReason(t, f.r) != "PreviousOwnershipRetained" {
		t.Fatal("replacement adopted ownership or repeated cleanup")
	}
	checkpoint, err := f.r.checkpoints().Read(context.Background())
	if err != nil || checkpoint.ScaleSet.UID != "build-uid" {
		t.Fatal("lost previous ownership")
	}
}

func TestRuntimeRetriesCredentialCleanupAfterJoiningWorker(t *testing.T) {
	f := newRuntimeFixture(t)
	reconcileRuntime(t, f.r)
	awaitRuntime(t, f.workers[0].started)
	attempts := 0
	f.r.worker.cleanup = func() error {
		select {
		case <-f.workers[0].stopped:
		default:
			t.Fatal("credential cleanup preceded worker shutdown")
		}
		attempts++
		if attempts == 1 {
			return errors.New("temporary filesystem failure")
		}
		return nil
	}
	if err := f.r.stop(); err == nil || f.r.worker == nil {
		t.Fatal("failed cleanup obligation was forgotten")
	}
	if err := f.r.stop(); err != nil || f.r.worker != nil || attempts != 2 {
		t.Fatal("credential cleanup was not retried")
	}
}

func TestRuntimeProtectionFailureRetainsCandidateCleanup(t *testing.T) {
	f := newRuntimeFixture(t)
	originalFactory := f.r.Factory
	allowCleanup := false
	f.r.Factory = func(resolved Resolved, credentials Credentials, mode WorkerMode, ready func(bool)) (Worker, func() error, error) {
		worker, cleanup, err := originalFactory(resolved, credentials, mode, ready)
		return worker, func() error {
			if !allowCleanup {
				return errors.New("private cache still in use")
			}
			return cleanup()
		}, err
	}
	conflict := true
	f.r.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("update", "runnerscalesets", func(action kt.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() == "" && conflict {
			conflict = false
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "runnerscalesets"}, "build", errors.New("changed"))
		}
		return false, nil, nil
	})
	if err := f.r.Reconcile(context.Background()); !apierrors.IsConflict(err) {
		t.Fatalf("expected protection conflict: %v", err)
	}
	if f.r.worker != nil || len(f.r.pendingCleanups) != 1 {
		t.Fatal("unprotected worker started or its cache was forgotten")
	}
	select {
	case <-f.workers[0].started:
		t.Fatal("session started before finalizer was installed")
	default:
	}
	if err := f.r.Reconcile(context.Background()); err == nil || len(f.workers) != 1 {
		t.Fatal("unresolved cache cleanup allowed a new candidate")
	}
	allowCleanup = true
	reconcileRuntime(t, f.r)
	if len(f.r.pendingCleanups) != 0 || f.cleanups != 1 || len(f.workers) != 2 {
		t.Fatal("recovery lost candidate cleanup obligation")
	}
	awaitRuntime(t, f.workers[1].started)
}

func TestRuntimeBindingChangeRecoversOriginalFleetAfterRestart(t *testing.T) {
	f := newRuntimeFixture(t)
	reconcileRuntime(t, f.r)
	awaitRuntime(t, f.workers[0].started)
	if err := f.r.stop(); err != nil {
		t.Fatal(err)
	}
	mutateRoot(t, f.r, func(root *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(root.Object, "https://github.com/different-owner", "spec", "github", "url")
	})
	reconcileRuntime(t, f.r)
	if f.r.worker == nil || f.r.worker.mode != RecoveryMode {
		t.Fatal("rejected binding left the original fleet without reconciliation")
	}
	awaitRuntime(t, f.r.worker.worker.(*runtimeWorker).started)
	if runtimeReason(t, f.r) != "BindingRejected" {
		t.Fatal("changed fleet binding was accepted")
	}
	checkpoint, err := f.r.checkpoints().Read(context.Background())
	if err != nil || checkpoint.ScaleSet.Spec.GitHub.URL != "https://github.com/example" {
		t.Fatal("recovery replaced the original fleet binding", err)
	}
}

func TestRuntimeConditionRejectsUnobservedRootChanges(t *testing.T) {
	for _, change := range []string{"generation", "deletion"} {
		t.Run(change, func(t *testing.T) {
			f := newRuntimeFixture(t)
			root, err := f.r.roots().Get(context.Background(), f.r.Name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			mutateRoot(t, f.r, func(current *unstructured.Unstructured) {
				if change == "generation" {
					current.SetGeneration(root.GetGeneration() + 1)
				} else {
					now := metav1.Now()
					current.SetDeletionTimestamp(&now)
				}
			})
			ready := true
			f.r.Readiness = func(value bool) { ready = value }
			if err := f.r.condition(context.Background(), root, true, "Reconciled"); !errors.Is(err, ErrChanged) {
				t.Errorf("unobserved %s certified: %v", change, err)
			}
			if ready {
				t.Error("readiness certified an unobserved root change")
			}
			if runtimeReason(t, f.r) != "" {
				t.Error("stale observation wrote a condition")
			}
		})
	}
}

func TestRuntimeConditionWriteFailureClearsReadiness(t *testing.T) {
	f := newRuntimeFixture(t)
	root, err := f.r.roots().Get(context.Background(), f.r.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f.r.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("update", "runnerscalesets", func(action kt.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() == "status" {
			return true, nil, errors.New("status unavailable")
		}
		return false, nil, nil
	})
	ready := true
	f.r.Readiness = func(value bool) { ready = value }
	if err := f.r.condition(context.Background(), root, true, "Reconciled"); err == nil || ready {
		t.Fatal("status failure left readiness true", err, ready)
	}
}

func TestRuntimeRejectsRootChangedDuringWorkerPreparation(t *testing.T) {
	f := newRuntimeFixture(t)
	factory := f.r.Factory
	f.r.Factory = func(resolved Resolved, credentials Credentials, mode WorkerMode, ready func(bool)) (Worker, func() error, error) {
		worker, cleanup, err := factory(resolved, credentials, mode, ready)
		mutateRoot(t, f.r, func(root *unstructured.Unstructured) {
			root.SetGeneration(root.GetGeneration() + 1)
			if err := unstructured.SetNestedField(root.Object, true, "spec", "suspend"); err != nil {
				t.Fatal(err)
			}
		})
		return worker, cleanup, err
	}
	if err := f.r.Reconcile(context.Background()); !errors.Is(err, ErrChanged) {
		t.Errorf("worker used stale root: %v", err)
	}
	if f.r.worker != nil {
		t.Error("started a session after the root changed during preparation")
	}
	if err := f.r.retryCleanups(); err != nil {
		t.Fatal(err)
	}
	if f.cleanups != 1 {
		t.Errorf("prepared credentials were not cleaned: %d", f.cleanups)
	}
}

func TestRuntimeMissingRootCannotRemainReady(t *testing.T) {
	f := newRuntimeFixture(t)
	root, err := f.r.roots().Get(context.Background(), f.r.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.r.roots().Delete(context.Background(), f.r.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	ready := true
	f.r.Readiness = func(value bool) { ready = value }
	if err := f.r.condition(context.Background(), root, true, "Reconciled"); err != nil || ready {
		t.Fatal("missing root remained ready", err, ready)
	}
}
