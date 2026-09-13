package operator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	kt "k8s.io/client-go/testing"
)

func TestOperatorCanceledBeforeLeadershipStopsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	op := &Operator{Config: Config{Name: "follower-test", Namespace: "test"}, Client: fake.NewClientset()}
	if err := op.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("ordinary cancellation was reported as a leadership failure", err)
	}
}

func TestOperatorShutdownWaitsForLeaderOperations(t *testing.T) {
	client := fake.NewClientset()
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	client.PrependReactor("get", "configmaps", func(kt.Action) (bool, runtime.Object, error) {
		once.Do(func() { close(started) })
		<-release
		return true, nil, context.Canceled
	})
	op := &Operator{Config: Config{Name: "shutdown-test", Namespace: "test"}, Client: client}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- op.Run(ctx) }()
	select {
	case <-started:
	case err := <-done:
		close(release)
		t.Fatal("leadership did not start", err)
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("leadership did not start before deadline")
	}
	cancel()
	premature := false
	select {
	case <-done:
		premature = true
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if !premature {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("shutdown did not finish after active operation stopped")
		}
	}
	if premature {
		t.Fatal("Run returned while a leader operation was still using its provider scope")
	}
}
