package operator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/actions/scaleset"
	"github.com/google/uuid"
	"k8s.io/client-go/kubernetes/fake"
)

func TestReadinessRequiresScaleSetSessionAndClearsOnExit(t *testing.T) {
	for _, rejectSession := range []bool{false, true} {
		t.Run(fmt.Sprintf("rejectSession=%v", rejectSession), func(t *testing.T) {
			// This is an explicit local HTTP fixture, not evidence of live GitHub use.
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
					if rejectSession {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					_ = json.NewEncoder(w).Encode(scaleset.RunnerScaleSetSession{SessionID: uuid.New(), OwnerName: "fixture", RunnerScaleSet: &scaleset.RunnerScaleSet{ID: 1, Name: "test"}, MessageQueueURL: server.URL + "/messages", MessageQueueAccessToken: token, Statistics: &scaleset.RunnerScaleSetStatistic{}})
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
			var ready atomic.Bool
			becameReady := make(chan struct{}, 1)
			o.Readiness = func(value bool) {
				ready.Store(value)
				if value {
					select {
					case becameReady <- struct{}{}:
					default:
					}
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- o.runLeader(ctx) }()
			if rejectSession {
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("rejected session succeeded")
					}
				case <-ctx.Done():
					t.Fatal("session rejection did not terminate")
				}
				select {
				case <-becameReady:
					t.Fatal("ready despite rejected session")
				default:
				}
			} else {
				select {
				case <-becameReady:
				case err := <-done:
					t.Fatalf("terminated before ready: %v", err)
				case <-ctx.Done():
					t.Fatal("never ready")
				}
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("did not exit after cancellation")
				}
			}
			if ready.Load() {
				t.Fatal("readiness retained after leader exit")
			}
		})
	}
}
