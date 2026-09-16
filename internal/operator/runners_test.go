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
