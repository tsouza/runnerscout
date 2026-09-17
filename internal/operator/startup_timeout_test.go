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
	"k8s.io/client-go/kubernetes/fake"
)

// A real production incident: a leader pod acquired its lease, then made no
// further progress at all - no log line, idle CPU, readyz stuck at 503 -
// for 49+ minutes, reproduced identically on restart. leaderCtx (this
// method's own ctx) is cancel-only: lease renewal is a separate goroutine
// that keeps succeeding as long as the Kubernetes API is reachable,
// entirely independent of GitHub connectivity, so nothing upstream ever
// cancelled the stalled call. This test reproduces the shape directly: a
// fixture that never responds to the scale-set lookup, and asserts
// runLeader returns (an error, not a hang) once githubStartupBudget elapses
// rather than blocking forever.
func TestRunLeaderBoundsStartupOnAnUnresponsiveGitHub(t *testing.T) {
	previous := githubStartupBudget
	githubStartupBudget = 100 * time.Millisecond
	defer func() { githubStartupBudget = previous }()

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
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/1"):
			// The scale-set lookup itself never responds - held open until
			// the request's own context is cancelled, exactly modeling a
			// silently stalled connection (no RST, no timeout, no data).
			<-req.Context().Done()
		default:
			t.Errorf("unexpected fixture request %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	github, err := scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{GitHubConfigURL: server.URL + "/org", PersonalAccessToken: "fixture"}, scaleset.WithRetryMax(0))
	if err != nil {
		t.Fatal(err)
	}
	o := New(Config{Name: "test", Namespace: "test", ScaleSetID: 1, MaxRunners: 1}, fake.NewClientset(), github)

	// ctx is cancel-only, deliberately with no deadline of its own -
	// matching leaderCtx in production (context.WithCancel over WithLease's
	// own ctx). A test-owned deadline here would let the test still pass
	// even if runLeader stopped applying githubStartupBudget itself, since
	// context cancellation propagates to any in-flight request regardless
	// of which ancestor context set the deadline - exactly the mistake that
	// would hide a regression rather than catch one.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- o.runLeader(ctx) }()

	select {
	case e := <-done:
		if e == nil {
			t.Fatal("expected the stalled scale-set lookup to surface as an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runLeader did not bound the stalled startup sequence - it hung past githubStartupBudget")
	}
}
