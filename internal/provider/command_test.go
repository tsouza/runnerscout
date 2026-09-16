package provider

import (
	"context"
	"errors"
	"fmt"
	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/placement"
	"github.com/tsouza/runnerscout/internal/testutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func allocation() lifecycle.Allocation {
	return lifecycle.Allocation{ID: "rs-test", Offering: placement.Offering{Region: "us-east-1", Zone: "us-east-1a", Architecture: "amd64", Machine: "c6i.large", Image: "ami-test", Spot: true}}
}
func TestAWSCreateUsesDurableTokenAndPrivateBootstrap(t *testing.T) {
	p, f, a := nativeAWSFixture(t)
	id, err := p.Create(context.Background(), a)
	if err != nil || id != testutil.AWSInstanceID {
		t.Fatal(id, err)
	}
	found := false
	for _, request := range f.Requests() {
		if request.Get("Action") == "RunInstances" {
			found = true
			if request.Get("ClientToken") != a.ID || request.Get("MaxCount") != "1" {
				t.Fatal("launch identity missing")
			}
		}
	}
	if !found {
		t.Fatal("no EC2 create request")
	}
}
func malformedAWSInventory(t *testing.T, body string) {
	t.Helper()
	p, fixture, a := nativeAWSFixture(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if r.Form.Get("Action") == "GetCallerIdentity" {
			fixture.Server.Config.Handler.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	p.AWS.HTTPClient, p.AWS.Endpoint = server.Client(), server.URL
	ob, err := p.Observe(context.Background(), a)
	if err == nil || ob.Known {
		t.Fatal("malformed inventory treated as absence", ob, err)
	}
	if err := p.Delete(context.Background(), a); err == nil {
		t.Fatal("malformed inventory confirmed cleanup")
	}
}
func TestUnknownProviderOutputIsNotAbsence(t *testing.T) { malformedAWSInventory(t, "not-xml") }
func TestMissingInventoryCannotConfirmCleanup(t *testing.T) {
	for _, body := range []string{"", `<DescribeInstancesResponse/>`, `<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>fixture</requestId></DescribeInstancesResponse>`} {
		malformedAWSInventory(t, body)
	}
}
func TestAWSAccountDriftCannotConfirmAbsence(t *testing.T) {
	p, f, a := nativeAWSFixture(t)
	f.SetAccount("999999999999")
	ob, err := p.Observe(context.Background(), a)
	if err == nil || ob.Known || len(f.Requests()) != 1 {
		t.Fatal(ob, err, f.Requests())
	}
}
func TestJITPreparationFailureHasNoCloudEffects(t *testing.T) {
	for _, mode := range []string{"nil", "error", "empty"} {
		t.Run(mode, func(t *testing.T) {
			p, f, a := nativeAWSFixture(t)
			p.Bootstrap = nil
			if mode != "nil" {
				p.Bootstrap = func(context.Context, string) (string, error) {
					if mode == "error" {
						return "", errors.New("private upstream error")
					}
					return "", nil
				}
			}
			receipt, err := p.CreateWithResources(context.Background(), a)
			if !errors.Is(err, lifecycle.ErrNoEffect) || receipt.ResourceID != "" || len(receipt.Resources) != 0 || len(f.Requests()) != 0 {
				t.Fatal("bootstrap failure allowed cloud effects", receipt, err)
			}
			// A real Bootstrap error (e.g. GitHub JIT config generation
			// failing) must stay readable in the wrapped error's own text -
			// Step's Condition assignment relies on it (issue #176: this used
			// to be discarded here, collapsing every preparation failure to
			// an indistinguishable bare sentinel).
			if mode == "error" && !strings.Contains(err.Error(), "private upstream error") {
				t.Fatal("Bootstrap's real error must not be discarded", err)
			}
		})
	}
}
func TestUnsupportedDependencyRecordsRefuseCloudEffects(t *testing.T) {
	for _, kind := range []string{"aws", "azure", "gcp"} {
		calls := 0
		p := Command{Config: credentialConfig(kind), Bootstrap: func(context.Context, string) (string, error) { calls++; return "jit", nil }}
		a := allocation()
		a.Resources = []lifecycle.ResourceReference{{Kind: "future-resource", ID: "retained"}}
		if _, err := p.Create(context.Background(), a); err == nil {
			t.Fatal("created over unsupported dependencies")
		}
		if ob, err := p.Observe(context.Background(), a); err == nil || ob.Known {
			t.Fatal("unsupported dependency falsely observed absent")
		}
		if err := p.Delete(context.Background(), a); err == nil || calls != 0 {
			t.Fatal("unsupported dependency allowed cloud effects")
		}
	}
}
