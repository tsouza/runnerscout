package operator

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"sync"
	"time"
)

// WithLease serializes a controller's complete lifecycle, including configuration
// checkpoints and finalization. It joins the callback before returning, so a
// caller can then remove credentials without racing active work.
func WithLease(ctx context.Context, client kubernetes.Interface, namespace, name string, run func(context.Context) error) error {
	lock := &resourcelock.LeaseLock{LeaseMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Client: client.CoordinationV1(), LockConfig: resourcelock.ResourceLockConfig{Identity: uuid.NewString()}}
	errCh := make(chan error, 1)
	leaderCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Leader election starts its callback asynchronously and does not join it.
	// Close the start gate before waiting so a delayed callback cannot start new
	// operations after Run returns and its caller destroys credential scopes.
	var gate sync.Mutex
	var workers sync.WaitGroup
	stopping := false
	elector, e := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{Lock: lock, LeaseDuration: 30 * time.Second, RenewDeadline: 20 * time.Second, RetryPeriod: 5 * time.Second, ReleaseOnCancel: false, Callbacks: leaderelection.LeaderCallbacks{OnStartedLeading: func(c context.Context) {
		gate.Lock()
		if stopping {
			gate.Unlock()
			return
		}
		workers.Add(1)
		gate.Unlock()
		defer workers.Done()
		errCh <- run(c)
		cancel()
	}, OnStoppedLeading: func() { cancel() }}})
	if e != nil {
		return e
	}
	elector.Run(leaderCtx)
	gate.Lock()
	stopping = true
	gate.Unlock()
	workers.Wait()
	select {
	case e := <-errCh:
		return e
	default:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("leadership ended; state retained")
	}
}
