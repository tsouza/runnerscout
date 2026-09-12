package lifecycle_test

import (
	"context"
	"errors"
	"testing"
	"time"

	l "github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/provider"
)

type preparationExecutor struct{ calls int }

func (e *preparationExecutor) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	e.calls++
	if len(args) > 0 && args[0] == "sts" {
		return []byte(`{"Account":"000000000000"}`), nil
	}
	return []byte(`{"Instances":[{"InstanceId":"i-preparation"}]}`), nil
}

func TestJITPreparationFailureRetriesWithoutCloudCommitment(t *testing.T) {
	for _, finish := range []string{"recover", "deadline", "attempt-limit"} {
		t.Run(finish, func(t *testing.T) {
			controller, durable, _, now := setup()
			executor := &preparationExecutor{}
			fail := true
			requests := 0
			adapter := &provider.Command{Config: provider.Config{Kind: "aws", AccountID: "000000000000", Owner: "test", Subnet: "subnet", SecurityGroup: "sg"}, Exec: executor,
				Bootstrap: func(context.Context, string) (string, error) {
					requests++
					if fail {
						return "", errors.New("private upstream failure")
					}
					return "jit", nil
				},
			}
			controller.Providers = map[string]l.Provider{"a": adapter}
			deadline := durable.a.Deadline
			if err := controller.Step(context.Background(), "rs-test"); err != nil {
				t.Fatal(err)
			}
			if durable.a.Phase != l.Pending || executor.calls != 0 || durable.a.Attempts != 1 {
				t.Fatalf("preparation failure invented a cloud obligation: phase=%s calls=%d attempts=%d", durable.a.Phase, executor.calls, durable.a.Attempts)
			}
			// Reconstruct the controller: all retry/deadline state must be durable.
			controller = &l.Controller{Store: durable, Providers: map[string]l.Provider{"a": adapter}, Now: func() time.Time { return *now }}
			switch finish {
			case "recover":
				fail = false
			case "deadline":
				*now = deadline
			case "attempt-limit":
				for i := 1; i < durable.a.MaxAttempts; i++ {
					if err := controller.Step(context.Background(), "rs-test"); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := controller.Step(context.Background(), "rs-test"); err != nil {
				t.Fatal(err)
			}
			if durable.a.Deadline != deadline {
				t.Fatal("preparation failure moved deadline")
			}
			if finish == "recover" {
				if durable.a.Phase != l.Running || durable.a.ResourceID != "i-preparation" || executor.calls != 2 || requests != 2 {
					t.Fatalf("recovery failed: %+v calls=%d requests=%d", durable.a, executor.calls, requests)
				}
			} else if durable.a.Phase != l.TimedOut || executor.calls != 0 || requests > durable.a.MaxAttempts {
				t.Fatalf("unbounded preparation retries: %+v calls=%d requests=%d", durable.a, executor.calls, requests)
			}
		})
	}
}
