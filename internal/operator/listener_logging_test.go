package operator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/actions/scaleset"
	"github.com/google/uuid"
	"k8s.io/client-go/kubernetes/fake"
)

// runLeader used to construct the scale-set listener with no Logger set,
// which listener.Config.Validate defaults to a discard handler - every one
// of the listener's own diagnostic log lines (initial and per-message
// TotalAssignedJobs, "Getting next message"/lastMessageID) was silently
// thrown away. That is exactly the visibility a deployment needs to tell
// "GitHub is genuinely never sending this scale set a job" apart from "we
// received one and failed to act on it" - without it, both look identical
// from this controller's own logs (a real gap surfaced by a live report of
// a scale set stuck at zero admitted jobs with no other errors anywhere).
func TestRunLeaderSurfacesListenerDiagnostics(t *testing.T) {
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
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/sessions"):
			_ = json.NewEncoder(w).Encode(scaleset.RunnerScaleSetSession{SessionID: uuid.New(), OwnerName: "fixture", RunnerScaleSet: &scaleset.RunnerScaleSet{ID: 1, Name: "test"}, MessageQueueURL: server.URL + "/messages", MessageQueueAccessToken: token, Statistics: &scaleset.RunnerScaleSetStatistic{TotalAssignedJobs: 0}})
		case req.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case req.URL.Path == "/messages":
			select {
			case <-req.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
				w.WriteHeader(http.StatusAccepted)
			}
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/1"):
			_ = json.NewEncoder(w).Encode(scaleset.RunnerScaleSet{ID: 1, Name: "test"})
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

	var buf syncBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(previous)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- o.runLeader(ctx) }()

	// The initial session statistics are logged before the listener's own
	// polling loop starts. Poll for the log line rather than sleeping a
	// fixed duration and hoping - a fixed sleep is exactly the kind of
	// timing assumption that flakes under load (a slow CI runner, GC
	// pause, or scheduler contention delaying the goroutine past whatever
	// fixed window was guessed).
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(buf.String(), "totalAssignedJobs") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runLeader did not exit after cancellation")
	}

	if !strings.Contains(buf.String(), "totalAssignedJobs") {
		t.Fatal("listener diagnostics were not surfaced through the controller's own logger", buf.String())
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
