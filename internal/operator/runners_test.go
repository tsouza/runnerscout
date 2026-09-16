package operator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/actions/scaleset"
	"github.com/tsouza/runnerscout/internal/provider"
	"k8s.io/client-go/kubernetes/fake"
)

// fixtureClient builds a *scaleset.Client backed by a local HTTP fixture (not
// live GitHub - matching readiness_test.go's own fixture pattern), serving
// the agent-registry endpoints DeregisterRunner depends on: listing a runner
// by name (agentName query) and removing it by numeric ID.
func fixtureClient(t *testing.T, runners map[string]int, removed *[]int) *scaleset.Client {
	t.Helper()
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Hour).Unix())))
	token := "eyJhbGciOiJIUzI1NiJ9." + payload + ".c2ln"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(req.URL.Path, "/runners/registration-token"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"fixture"}`))
		case strings.HasSuffix(req.URL.Path, "/actions/runner-registration"):
			_ = json.NewEncoder(w).Encode(map[string]string{"url": server.URL + "/tenant/123/", "token": token})
		case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/agents"):
			name := req.URL.Query().Get("agentName")
			id, ok := runners[name]
			if !ok {
				_, _ = w.Write([]byte(`{"count":0,"value":[]}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"count": 1, "value": []map[string]any{{"id": id, "name": name}}})
		case req.Method == http.MethodDelete && strings.Contains(req.URL.Path, "/agents/"):
			parts := strings.Split(strings.TrimSuffix(req.URL.Path, "/"), "/")
			var id int
			_, _ = fmt.Sscanf(parts[len(parts)-1], "%d", &id)
			*removed = append(*removed, id)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected fixture request %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{GitHubConfigURL: server.URL + "/org", PersonalAccessToken: "fixture"}, scaleset.WithRetryMax(0))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// internal/configapi/runtime.go's CleanupMode and RecoveryMode both call
// operator.New with a nil *scaleset.Client (they never open a GitHub
// session, by design) and then run Tick in a loop - the exact shape this
// test constructs. Controller.Runners must be a true nil interface in that
// case, not a non-nil *runnerDeregistrar wrapping a nil client: the latter
// would make every Controller.Runners == nil check elsewhere (deregister,
// pruneTerminalAllocations) silently unable to detect "no GitHub wiring at
// all," letting pruneTerminalAllocations delete records having asked GitHub
// nothing while believing it had confirmed clean.
func TestNewLeavesRunnersNilWithoutAGitHubClient(t *testing.T) {
	o := New(Config{Name: "test", Namespace: "test", MaxRunners: 1, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600}, fake.NewClientset(), nil)
	if o.Controller.Runners != nil {
		t.Fatal("Controller.Runners must be nil when no GitHub client is configured", o.Controller.Runners)
	}
}

// New's own Bootstrap closure (github JIT config generation, shared by every
// provider.Command it builds) is the first of three swallow points issue
// #176 found: a real GitHub-side failure (e.g. a 429) was previously
// collapsed to a bare "GitHub JIT request failed" before command.go or
// lifecycle.go ever saw the real cause, which is exactly why the incident
// looked like a GCP-provider problem instead of a GitHub one.
func TestBootstrapPreservesGitHubJITFailureCause(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Hour).Unix())))
	token := "eyJhbGciOiJIUzI1NiJ9." + payload + ".c2ln"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(req.URL.Path, "/runners/registration-token"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"fixture"}`))
		case strings.HasSuffix(req.URL.Path, "/actions/runner-registration"):
			_ = json.NewEncoder(w).Encode(map[string]string{"url": server.URL + "/tenant/123/", "token": token})
		case strings.Contains(req.URL.Path, "/generatejitconfig"):
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"rate limited"}`))
		default:
			t.Errorf("unexpected fixture request %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{GitHubConfigURL: server.URL + "/org", PersonalAccessToken: "fixture"}, scaleset.WithRetryMax(0))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Name: "test", Namespace: "test", ScaleSetID: 1, MaxRunners: 1, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600,
		Providers: map[string]provider.Config{"a": {Kind: "aws"}}}
	o := New(cfg, fake.NewClientset(), client)
	command := o.Controller.Providers["a"].(*provider.Command)
	material, err := command.Bootstrap(context.Background(), "rs-test")
	if err == nil || material != "" {
		t.Fatal("expected the fixture's 429 to fail bootstrap", material, err)
	}
	if !strings.Contains(err.Error(), "generatejitconfig") {
		t.Fatal("Bootstrap must preserve the real GitHub failure detail, not a bare fixed string", err)
	}
}

// A batch of allocations admitted in the same instant must not fire their
// GenerateJitRunnerConfig calls at GitHub within the same sub-millisecond
// window - issue #176's own evidence (a concurrently-admitted batch
// consistently failed before ever reaching the cloud provider, while an
// isolated allocation always succeeded) is the signature of a burst-
// sensitive backend limit. jitLimiter mitigates that without penalizing the
// common case: an isolated Bootstrap call must never wait.
func TestBootstrapThrottlesConcurrentGitHubJITRequests(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Hour).Unix())))
	token := "eyJhbGciOiJIUzI1NiJ9." + payload + ".c2ln"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(req.URL.Path, "/runners/registration-token"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"fixture"}`))
		case strings.HasSuffix(req.URL.Path, "/actions/runner-registration"):
			_ = json.NewEncoder(w).Encode(map[string]string{"url": server.URL + "/tenant/123/", "token": token})
		case strings.Contains(req.URL.Path, "/generatejitconfig"):
			_ = json.NewEncoder(w).Encode(map[string]string{"encodedJITConfig": "fixture-jit"})
		default:
			t.Errorf("unexpected fixture request %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{GitHubConfigURL: server.URL + "/org", PersonalAccessToken: "fixture"}, scaleset.WithRetryMax(0))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Name: "test", Namespace: "test", ScaleSetID: 1, MaxRunners: 1, ProvisioningSeconds: 60, MaxLifetimeSeconds: 600,
		Providers: map[string]provider.Config{"a": {Kind: "aws"}}}
	o := New(cfg, fake.NewClientset(), client)
	command := o.Controller.Providers["a"].(*provider.Command)

	start := time.Now()
	if _, err := command.Bootstrap(context.Background(), "rs-first"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > githubJITRequestInterval/2 {
		t.Fatal("an isolated Bootstrap call must not be throttled", elapsed)
	}

	start = time.Now()
	if _, err := command.Bootstrap(context.Background(), "rs-second"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < githubJITRequestInterval/2 {
		t.Fatal("a second Bootstrap call arriving immediately after the first must be throttled", elapsed)
	}
}

func TestRunnerDeregistrarNilClientNoop(t *testing.T) {
	d := &runnerDeregistrar{}
	if err := d.DeregisterRunner(context.Background(), "rs-test"); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerDeregistrarNoOpsWhenRegistrationAlreadyGone(t *testing.T) {
	var removed []int
	client := fixtureClient(t, map[string]int{}, &removed)
	d := &runnerDeregistrar{client: client}
	if err := d.DeregisterRunner(context.Background(), "rs-test"); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatal("must not attempt to remove a registration that was never found", removed)
	}
}

func TestRunnerDeregistrarRemovesClaimedRegistration(t *testing.T) {
	var removed []int
	client := fixtureClient(t, map[string]int{"rs-test": 42}, &removed)
	d := &runnerDeregistrar{client: client}
	if err := d.DeregisterRunner(context.Background(), "rs-test"); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != 42 {
		t.Fatal("must remove exactly the claimed registration's numeric ID", removed)
	}
}
