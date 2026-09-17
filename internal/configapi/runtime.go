package configapi

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	"github.com/actions/scaleset"
	api "github.com/tsouza/runnerscout/api/v1alpha1"
	"github.com/tsouza/runnerscout/internal/githubjobs"
	"github.com/tsouza/runnerscout/internal/operator"
	"github.com/tsouza/runnerscout/internal/provider"
	"github.com/tsouza/runnerscout/internal/version"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const Finalizer = "runnerscout.io/runner-cleanup"

type Worker interface {
	RunSession(context.Context) error
	RunCleanup(context.Context) error
	RunRecovery(context.Context) error
	PauseAdmissions(bool)
	Drain()
	Drained(context.Context) (bool, error)
}

type WorkerMode string

const (
	SessionMode  WorkerMode = "session"
	RecoveryMode WorkerMode = "recovery"
	CleanupMode  WorkerMode = "cleanup"
)

type WorkerFactory func(Resolved, Credentials, WorkerMode, func(bool)) (Worker, func() error, error)

type activeWorker struct {
	worker  Worker
	key     string
	uid     string
	mode    WorkerMode
	ready   *atomic.Bool
	cancel  context.CancelFunc
	done    chan struct{}
	err     error
	cleanup func() error
}

// Runtime owns one named RunnerScaleSet and all of its reconciliation under the
// same Lease used by mounted-config controllers. Checkpoint/status/finalizer
// effects cannot race another elected instance of this scale set.
type Runtime struct {
	Namespace       string
	Name            string
	Client          kubernetes.Interface
	Dynamic         dynamic.Interface
	Readiness       func(bool)
	Factory         WorkerFactory
	worker          *activeWorker
	completedUID    string
	pendingCleanups []func() error
}

func (r *Runtime) roots() dynamic.ResourceInterface {
	return r.Dynamic.Resource(schema.GroupVersionResource{Group: api.Group, Version: api.Version, Resource: "runnerscalesets"}).Namespace(r.Namespace)
}

func (r *Runtime) checkpoints() Checkpoints {
	return Checkpoints{Maps: r.Client.CoreV1().ConfigMaps(r.Namespace), Namespace: r.Namespace, Name: r.Name}
}

func (r *Runtime) setReady(ready bool) {
	if r.Readiness != nil {
		r.Readiness(ready)
	}
}

func (r *Runtime) newWorker(resolved Resolved, credentials Credentials, mode WorkerMode, ready func(bool)) (Worker, func() error, error) {
	if r.Factory != nil {
		return r.Factory(resolved, credentials, mode, ready)
	}
	var github *scaleset.Client
	var err error
	if mode == SessionMode {
		system := scaleset.SystemInfo{System: "runnerscout", Version: version.Version}
		if resolved.Auth.Mode == "app" {
			github, err = scaleset.NewClientWithGitHubApp(scaleset.ClientWithGitHubAppConfig{GitHubConfigURL: resolved.Config.GitHubURL,
				GitHubAppAuth: scaleset.GitHubAppAuth{ClientID: resolved.Auth.AppClientID, InstallationID: resolved.Auth.AppInstallationID, PrivateKey: string(credentials.GitHub)}, SystemInfo: system})
		} else {
			github, err = scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{GitHubConfigURL: resolved.Config.GitHubURL, PersonalAccessToken: string(credentials.GitHub), SystemInfo: system})
		}
		if err != nil {
			return nil, nil, errors.New("GitHub client initialization failed")
		}
	}
	op, cleanup, err := operator.NewWithCredentials(resolved.Config, r.Client, github, credentials.Providers)
	if err != nil {
		return nil, nil, err
	}
	if resolved.Config.Retry.Enabled {
		// Compile already refuses Retry.Enabled for App authentication, so
		// credentials.GitHub here is always a PAT.
		op.GitHubJobs = &githubjobs.Client{Token: string(credentials.GitHub)}
	}
	if resolved.Config.AWSPriceRefresh {
		command, ok := op.Controller.Providers["aws"].(*provider.Command)
		if !ok || command.AWS == nil {
			return nil, nil, errors.New(`AWS price refresh requires a configured "aws" provider`)
		}
		op.AWSPrices = command.AWS.SpotPrices()
	}
	if resolved.Config.AzurePriceRefresh {
		command, ok := op.Controller.Providers["azure"].(*provider.Command)
		if !ok || command.Azure == nil {
			return nil, nil, errors.New(`Azure price refresh requires a configured "azure" provider`)
		}
		op.AzurePrices = command.Azure.SpotPrices()
	}
	if resolved.Config.GCPPriceRefresh {
		command, ok := op.Controller.Providers["gcp"].(*provider.Command)
		if !ok || command.GCP == nil || command.GCP.BillingAPIKey == "" {
			return nil, nil, errors.New(`GCP price refresh requires a configured "gcp" provider with a billing API key`)
		}
		op.GCPPrices = command.GCP.SkuPrices()
	}
	if resolved.Config.AzureInterruptionQueueURL != "" {
		command, ok := op.Controller.Providers["azure"].(*provider.Command)
		if !ok || command.Azure == nil {
			return nil, nil, errors.New(`Azure interruption delivery requires a configured "azure" provider`)
		}
		queue, err := command.Azure.InterruptionQueue(resolved.Config.AzureInterruptionQueueURL)
		if err != nil {
			return nil, nil, err
		}
		op.AzureInterruptions = queue
	}
	op.Readiness = ready
	if mode == CleanupMode {
		op.Drain()
	} else {
		op.PauseAdmissions(mode == RecoveryMode || resolved.Suspend)
	}
	return op, cleanup, nil
}

func (r *Runtime) stop() error {
	if r.worker == nil {
		return nil
	}
	worker := r.worker
	worker.cancel()
	<-worker.done
	r.setReady(false)
	// Keep the handle until private credential cache cleanup succeeds.
	if err := worker.cleanup(); err != nil {
		return err
	}
	r.worker = nil
	return nil
}

func (r *Runtime) start(ctx context.Context, worker Worker, cleanup func() error, key, uid string, mode WorkerMode, ready *atomic.Bool) {
	child, cancel := context.WithCancel(ctx)
	active := &activeWorker{worker: worker, key: key, uid: uid, mode: mode, ready: ready, cancel: cancel, done: make(chan struct{}), cleanup: cleanup}
	r.worker = active
	go func() {
		defer close(active.done)
		if mode == CleanupMode {
			active.err = worker.RunCleanup(child)
		} else if mode == RecoveryMode {
			active.err = worker.RunRecovery(child)
		} else {
			active.err = worker.RunSession(child)
		}
	}()
}

func sameRootRevision(a, b metav1.Object) bool {
	return a.GetUID() == b.GetUID() && a.GetGeneration() == b.GetGeneration() && a.GetDeletionTimestamp().Equal(b.GetDeletionTimestamp())
}

func (r *Runtime) condition(ctx context.Context, root *unstructured.Unstructured, ready bool, reason string) (result error) {
	defer func() { r.setReady(result == nil && root != nil && ready) }()
	if root == nil {
		return nil
	}
	current, err := r.roots().Get(ctx, r.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		ready = false
		return nil
	}
	if err != nil {
		return err
	}
	if !sameRootRevision(current, root) {
		return ErrChanged
	}
	status := "False"
	if ready {
		status = "True"
	}
	old, _, _ := unstructured.NestedSlice(current.Object, "status", "conditions")
	conditions := make([]any, 0, len(old)+1)
	transition := metav1.Now().Format(time.RFC3339)
	for _, value := range old {
		condition, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if condition["type"] != "Ready" {
			conditions = append(conditions, condition)
			continue
		}
		if condition["status"] == status {
			if previous, ok := condition["lastTransitionTime"].(string); ok {
				transition = previous
			}
		}
		if condition["status"] == status && condition["reason"] == reason && condition["observedGeneration"] == current.GetGeneration() {
			return nil
		}
	}
	conditions = append(conditions, map[string]any{"type": "Ready", "status": status, "reason": reason, "message": reason, "observedGeneration": current.GetGeneration(), "lastTransitionTime": transition})
	current.Object["status"] = map[string]any{"observedGeneration": current.GetGeneration(), "conditions": conditions}
	_, err = r.roots().UpdateStatus(ctx, current, metav1.UpdateOptions{})
	return err
}

func (r *Runtime) protect(ctx context.Context, root *unstructured.Unstructured) error {
	if root.GetDeletionTimestamp() != nil {
		return ErrChanged
	}
	if slices.Contains(root.GetFinalizers(), Finalizer) {
		return nil
	}
	updated := root.DeepCopy()
	updated.SetFinalizers(append(slices.Clone(root.GetFinalizers()), Finalizer))
	_, err := r.roots().Update(ctx, updated, metav1.UpdateOptions{})
	return err
}

func (r *Runtime) finalize(ctx context.Context, root *unstructured.Unstructured, uid string) error {
	if root == nil {
		return nil
	}
	current, err := r.roots().Get(ctx, r.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if string(current.GetUID()) != uid || current.GetDeletionTimestamp() == nil {
		return ErrCheckpointOwnership
	}
	current.SetFinalizers(slices.DeleteFunc(slices.Clone(current.GetFinalizers()), func(value string) bool { return value == Finalizer }))
	_, err = r.roots().Update(ctx, current, metav1.UpdateOptions{})
	return err
}

func (r *Runtime) reconcileCleanup(ctx context.Context, root *unstructured.Unstructured, snapshot Snapshot) error {
	uid := string(snapshot.ScaleSet.UID)
	if r.completedUID == uid {
		if root != nil && string(root.GetUID()) != uid {
			return r.condition(ctx, root, false, "PreviousOwnershipRetained")
		}
		if err := r.finalize(ctx, root, uid); err != nil {
			return err
		}
		return r.condition(ctx, root, false, "CleanupComplete")
	}
	if r.worker != nil && (r.worker.mode != CleanupMode || r.worker.uid != uid) {
		if err := r.stop(); err != nil {
			return err
		}
	}
	if r.worker == nil {
		resolved, err := Compile(snapshot)
		if err != nil {
			return err
		}
		credentials, err := ResolveCleanupSecrets(ctx, r.Client.CoreV1().Secrets(r.Namespace), resolved)
		if err != nil {
			return r.condition(ctx, root, false, "CleanupCredentialsUnavailable")
		}
		ready := new(atomic.Bool)
		worker, cleanup, err := r.newWorker(resolved, credentials, CleanupMode, ready.Store)
		if err != nil {
			return r.condition(ctx, root, false, "CleanupCredentialsInvalid")
		}
		worker.Drain()
		r.start(ctx, worker, cleanup, "cleanup", uid, CleanupMode, ready)
	}
	select {
	case <-r.worker.done:
		if r.worker.err != nil {
			_ = r.stop()
			return r.condition(ctx, root, false, "CleanupPending")
		}
		done, err := r.worker.worker.Drained(ctx)
		if err != nil || !done {
			return r.condition(ctx, root, false, "CleanupPending")
		}
		if err := r.stop(); err != nil {
			return err
		}
		r.completedUID = uid
		if root != nil && string(root.GetUID()) != uid {
			return r.condition(ctx, root, false, "PreviousOwnershipRetained")
		}
		if err := r.finalize(ctx, root, uid); err != nil {
			return err
		}
		return r.condition(ctx, root, false, "CleanupComplete")
	default:
		return r.condition(ctx, root, false, "CleanupPending")
	}
}

// Reconcile runs only within Run's Lease. Tests may call it with an isolated
// client to observe individual transitions; it must never run concurrently.
func (r *Runtime) Reconcile(ctx context.Context) error {
	if err := r.retryCleanups(); err != nil {
		r.pause()
		return err
	}
	root, err := r.roots().Get(ctx, r.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		root = nil
	} else if err != nil {
		return err
	}
	checkpoint, checkpointErr := r.checkpoints().Read(ctx)
	if checkpointErr != nil && !apierrors.IsNotFound(checkpointErr) {
		r.pause()
		return r.condition(ctx, root, false, "CheckpointInvalid")
	}
	if checkpointErr != nil && (r.worker != nil || (root != nil && slices.Contains(root.GetFinalizers(), Finalizer))) {
		r.pause()
		return r.condition(ctx, root, false, "CheckpointUnavailable")
	}
	deleting := root == nil || root.GetDeletionTimestamp() != nil
	if checkpointErr == nil && root != nil && root.GetUID() != checkpoint.ScaleSet.UID {
		deleting = true
	}
	if deleting {
		if checkpointErr != nil {
			return r.condition(ctx, root, false, "CheckpointUnavailable")
		}
		return r.reconcileCleanup(ctx, root, checkpoint)
	}
	loaded, err := Load(ctx, KubernetesReader{Client: r.Dynamic}, r.Client.CoreV1().Secrets(r.Namespace), r.Namespace, r.Name)
	if err != nil {
		r.pause()
		if checkpointErr == nil && slices.Contains(root.GetFinalizers(), Finalizer) {
			return r.recoverAccepted(ctx, root, checkpoint)
		}
		return r.condition(ctx, root, false, "ConfigurationUnavailable")
	}
	if !sameRootRevision(root, &loaded.Snapshot.ScaleSet) {
		r.pause()
		return ErrChanged
	}
	if r.worker != nil {
		select {
		case <-r.worker.done:
			// Unlike the CleanupMode branch above, this path always falls
			// through to a full worker rebuild regardless of whether
			// loaded.Key() actually changed (r.stop() below clears
			// r.worker to nil, so the Key()-equality short-circuit right
			// after this block can never fire for an already-exited
			// worker) - so worker.err is the only signal available for
			// telling "the session exited because its own configuration
			// changed" apart from "it exited on its own, e.g. an internal
			// runLeader/listener.Run error", and it was previously
			// discarded silently here. Both looked identical from the
			// outside: a stopped worker, then a freshly started one.
			if r.worker.err != nil {
				slog.Warn("session worker exited; restarting", "error", r.worker.err)
			}
			if err := r.stop(); err != nil {
				return err
			}
		default:
		}
	}
	if r.worker != nil && r.worker.key == loaded.Key() {
		r.worker.worker.PauseAdmissions(loaded.Resolved.Suspend)
		ready := r.worker.ready.Load() && !loaded.Resolved.Suspend
		reason := "WaitingForSession"
		if ready {
			reason = "Reconciled"
		} else if loaded.Resolved.Suspend {
			reason = "Suspended"
		}
		return r.condition(ctx, root, ready, reason)
	}
	ready := new(atomic.Bool)
	worker, cleanup, err := r.newWorker(loaded.Resolved, loaded.Credentials, SessionMode, ready.Store)
	if err != nil {
		if r.worker != nil {
			r.worker.worker.PauseAdmissions(true)
		}
		return r.condition(ctx, root, false, "CredentialsInvalid")
	}
	if err := r.checkpoints().Save(ctx, loaded.Snapshot); err != nil {
		r.discard(cleanup)
		if r.worker != nil {
			r.worker.worker.PauseAdmissions(true)
		}
		if errors.Is(err, ErrBindingChange) && checkpointErr == nil && slices.Contains(root.GetFinalizers(), Finalizer) {
			if err := r.recoverAccepted(ctx, root, checkpoint); err != nil {
				return err
			}
		}
		return r.condition(ctx, root, false, "BindingRejected")
	}
	observed, err := r.checkpoints().Read(ctx)
	if err != nil || observed.ScaleSet.UID != loaded.Snapshot.ScaleSet.UID {
		r.discard(cleanup)
		return ErrChanged
	}
	current, err := r.roots().Get(ctx, r.Name, metav1.GetOptions{})
	if err != nil || !sameRootRevision(current, &loaded.Snapshot.ScaleSet) {
		r.discard(cleanup)
		return ErrChanged
	}
	if err := r.protect(ctx, current); err != nil {
		r.discard(cleanup)
		return err
	}
	if err := r.stop(); err != nil {
		r.discard(cleanup)
		return err
	}
	r.start(ctx, worker, cleanup, loaded.Key(), string(current.GetUID()), SessionMode, ready)
	return r.condition(ctx, current, false, "WaitingForSession")
}

// recoverAccepted keeps accepted jobs under their original configuration when
// live references become invalid. Recovery never adopts a changed binding or
// removes a running job simply because the desired configuration is broken.
func (r *Runtime) recoverAccepted(ctx context.Context, root *unstructured.Unstructured, snapshot Snapshot) error {
	if r.worker != nil {
		select {
		case <-r.worker.done:
			if err := r.stop(); err != nil {
				return err
			}
		default:
			r.worker.worker.PauseAdmissions(true)
			return r.condition(ctx, root, false, "ConfigurationUnavailable")
		}
	}
	resolved, err := Compile(snapshot)
	if err != nil {
		return r.condition(ctx, root, false, "CheckpointInvalid")
	}
	credentials, err := ResolveCleanupSecrets(ctx, r.Client.CoreV1().Secrets(r.Namespace), resolved)
	if err != nil {
		return r.condition(ctx, root, false, "RecoveryCredentialsUnavailable")
	}
	resolved.Suspend = true
	ready := new(atomic.Bool)
	worker, cleanup, err := r.newWorker(resolved, credentials, RecoveryMode, ready.Store)
	if err != nil {
		return r.condition(ctx, root, false, "RecoveryCredentialsInvalid")
	}
	worker.PauseAdmissions(true)
	r.start(ctx, worker, cleanup, "recovery", string(snapshot.ScaleSet.UID), RecoveryMode, ready)
	return r.condition(ctx, root, false, "ConfigurationUnavailable")
}

func (r *Runtime) pause() {
	r.setReady(false)
	if r.worker != nil {
		r.worker.worker.PauseAdmissions(true)
	}
}

// A rejected candidate may already have allocated private credential caches.
// Keep failed removals separate from active workers and retry before admitting
// another candidate. The cleanup functions are idempotent.
func (r *Runtime) discard(cleanup func() error) {
	if err := cleanup(); err != nil {
		r.pendingCleanups = append(r.pendingCleanups, cleanup)
	}
}

func (r *Runtime) retryCleanups() error {
	var pending []func() error
	for _, cleanup := range r.pendingCleanups {
		if err := cleanup(); err != nil {
			pending = append(pending, cleanup)
		}
	}
	r.pendingCleanups = pending
	if len(pending) != 0 {
		return errors.New("provider credential cache cleanup incomplete")
	}
	return nil
}

func (r *Runtime) Run(ctx context.Context) error {
	return operator.WithLease(ctx, r.Client, r.Namespace, r.Name, func(leader context.Context) (result error) {
		defer func() {
			if cleanupErr := errors.Join(r.stop(), r.retryCleanups()); cleanupErr != nil {
				result = errors.Join(result, cleanupErr)
			}
		}()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			if err := r.Reconcile(leader); err != nil {
				r.setReady(false)
				if r.worker != nil {
					r.worker.worker.PauseAdmissions(true)
				}
			}
			select {
			case <-leader.Done():
				return leader.Err()
			case <-ticker.C:
			}
		}
	})
}
