package lifecycle_test

import (
	"context"
	"errors"
	"testing"
	"time"

	l "github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/provider"
	"github.com/tsouza/runnerscout/internal/testutil"
)

func TestJITPreparationFailureRetriesWithoutCloudCommitment(t *testing.T) {
	for _, finish := range []string{"recover", "deadline", "attempt-limit"} {
		t.Run(finish, func(t *testing.T) {
			controller, durable, _, now := setup()
			fixture := testutil.NewAWS(t)
			durable.a.Offering = durable.a.Catalog.Offerings[0]
			durable.a.Offering.Region = "us-east-1"
			durable.a.Offering.Zone = "us-east-1a"
			durable.a.Offering.Image = "ami-test"
			durable.a.Offering.Machine = "c6i.large"
			durable.a.Offering.Architecture = "amd64"
			durable.a.Catalog.Offerings[0] = durable.a.Offering
			durable.a.Requirements.Regions = []string{"us-east-1"}
			fail := true
			requests := 0
			adapter, cleanup, err := provider.NewCommand(provider.Config{Kind: "aws", AccountID: "000000000000", Owner: "test", Subnet: "private-subnet", SecurityGroup: "private-sg"}, map[string]string{"AWS_ACCESS_KEY_ID": "fixture-id", "AWS_SECRET_ACCESS_KEY": "fixture-secret"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cleanup() })
			adapter.AWS.HTTPClient, adapter.AWS.Endpoint = fixture.Server.Client(), fixture.Server.URL
			adapter.Bootstrap = func(context.Context, string) (string, error) {
				requests++
				if fail {
					return "", errors.New("private upstream failure")
				}
				return "jit", nil
			}
			controller.Providers = map[string]l.Provider{"a": adapter}
			deadline := durable.a.Deadline
			if err := controller.Step(context.Background(), "rs-test"); err != nil {
				t.Fatal(err)
			}
			if durable.a.Phase != l.Pending || len(fixture.Requests()) != 0 || durable.a.Attempts != 1 {
				t.Fatalf("preparation failure invented a cloud obligation: phase=%s calls=%d attempts=%d", durable.a.Phase, len(fixture.Requests()), durable.a.Attempts)
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
				if durable.a.Phase != l.Running || durable.a.ResourceID != testutil.AWSInstanceID || len(fixture.Requests()) != 6 || len(durable.a.Resources) != 2 || requests != 2 {
					t.Fatalf("recovery failed: %+v calls=%d requests=%d", durable.a, len(fixture.Requests()), requests)
				}
			} else if durable.a.Phase != l.TimedOut || len(fixture.Requests()) != 0 || requests > durable.a.MaxAttempts {
				t.Fatalf("unbounded preparation retries: %+v calls=%d requests=%d", durable.a, len(fixture.Requests()), requests)
			}
		})
	}
}
