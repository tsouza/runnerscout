package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/lifecycle"
	"github.com/tsouza/runnerscout/internal/placement"
	"github.com/tsouza/runnerscout/internal/wireguard"
)

// fakeAllocationStore is an in-memory implementation of both
// health.AllocationStore (Load, List) and lifecycle.Store (Load, Save), so
// the same fixture can both serve the poll endpoint under test and be driven
// through the real lifecycle.Controller.Step to produce a genuine phase
// transition - the eager-revocation regression test below needs exactly
// that: a real transition, not a hand-edited fixture.
type fakeAllocationStore struct {
	mu          sync.Mutex
	allocations map[string]lifecycle.Allocation
	listErr     error
}

func (s *fakeAllocationStore) Load(_ context.Context, id string) (lifecycle.Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.allocations[id]
	if !ok {
		return lifecycle.Allocation{}, errors.New("not found")
	}
	return a, nil
}

func (s *fakeAllocationStore) Save(_ context.Context, a lifecycle.Allocation, _ string) (lifecycle.Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.allocations == nil {
		s.allocations = map[string]lifecycle.Allocation{}
	}
	s.allocations[a.ID] = a
	return a, nil
}

func (s *fakeAllocationStore) List(_ context.Context) ([]lifecycle.Allocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := make([]lifecycle.Allocation, 0, len(s.allocations))
	for _, a := range s.allocations {
		out = append(out, a)
	}
	return out, nil
}

func (s *fakeAllocationStore) put(a lifecycle.Allocation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.allocations == nil {
		s.allocations = map[string]lifecycle.Allocation{}
	}
	s.allocations[a.ID] = a
}

// noopProvider is the minimal lifecycle.Provider needed to drive Step
// through a Running->Deleting transition without any real cloud effects.
type noopProvider struct{}

func (noopProvider) Create(context.Context, lifecycle.Allocation) (string, error) {
	return "vm-1", nil
}
func (noopProvider) Observe(context.Context, lifecycle.Allocation) (lifecycle.Observation, error) {
	return lifecycle.Observation{Known: true, Exists: true, ResourceID: "vm-1"}, nil
}
func (noopProvider) Delete(context.Context, lifecycle.Allocation) error { return nil }

// wireGuardAllocation builds a Running, wireguard-mode allocation with a
// freshly minted poll token already checkpointed as its hash, mirroring
// exactly what provider.Command.CreateWithResources now produces at the
// Pending->Creating transition. The raw token is returned alongside so the
// test can present it as a bearer credential.
func wireGuardAllocation(t *testing.T, id, networkProfile, overlayAddress string) (lifecycle.Allocation, string) {
	t.Helper()
	keyPair, err := wireguard.Generate()
	if err != nil {
		t.Fatal(err)
	}
	token, hash, err := wireguard.GeneratePollToken()
	if err != nil {
		t.Fatal(err)
	}
	a := lifecycle.Allocation{
		ID:                      id,
		Phase:                   lifecycle.Running,
		Deadline:                time.Now().Add(time.Hour),
		MaxAttempts:             3,
		Offering:                placement.Offering{Provider: "noop"},
		NetworkProfile:          networkProfile,
		WireGuardPublicKey:      keyPair.Public.Bytes(),
		WireGuardOverlayAddress: overlayAddress,
		WireGuardPollTokenHash:  hash[:],
	}
	return a, token
}

func decodePeers(t *testing.T, body []byte) []wireguard.Peer {
	t.Helper()
	var decoded struct {
		Peers []wireguard.Peer `json:"peers"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("could not decode peer response %q: %v", body, err)
	}
	return decoded.Peers
}

func doPoll(t *testing.T, handler http.Handler, id, authorization string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/wireguard/peers/"+id, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Result()
}

// TestWireGuardPeersEndpointInertByDefault is the required "completely
// inert-by-default" regression test: a Status with no WireGuardPeers set
// (today's real construction in cmd/runnerscout/main.go and
// internal/configapi/runtime.go) must behave exactly as it did before this
// task - /healthz and /readyz unchanged, and the new path simply 404s like
// any other unregistered path, rather than panicking or behaving specially.
func TestWireGuardPeersEndpointInertByDefault(t *testing.T) {
	var status Status
	handler := status.Handler()

	resp := doPoll(t, handler, "rs-a", "Bearer whatever")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("inert Status: /v1/wireguard/peers/rs-a status = %d, want 404", resp.StatusCode)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, want 503", rec.Code)
	}
	status.SetReady(true)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz status after SetReady(true) = %d, want 200", rec.Code)
	}
}

// TestWireGuardPeersValidTokenReturnsSnapshot proves the endpoint's happy
// path: a valid bearer token for a real, Running, wireguard-mode allocation
// returns exactly the peer snapshot wireguard.Snapshot itself would compute
// for it, excluding itself and including its NetworkProfile peers.
func TestWireGuardPeersValidTokenReturnsSnapshot(t *testing.T) {
	store := &fakeAllocationStore{}
	self, token := wireGuardAllocation(t, "rs-self", "profile-a", "10.60.0.1")
	peer, _ := wireGuardAllocation(t, "rs-peer", "profile-a", "10.60.0.2")
	other, _ := wireGuardAllocation(t, "rs-other-profile", "profile-b", "10.60.1.1")
	store.put(self)
	store.put(peer)
	store.put(other)

	var status Status
	status.WireGuardPeers = &WireGuardPeersHandler{Store: store}
	handler := status.Handler()

	resp := doPoll(t, handler, "rs-self", "Bearer "+token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	peers := decodePeers(t, body)
	if len(peers) != 1 || peers[0].AllocationID != "rs-peer" {
		t.Fatalf("unexpected peer snapshot: %+v", peers)
	}
}

// TestWireGuardPeersRejectsInvalidCredentialsIdentically is the auth-boundary
// regression this task calls for extra rigor on: every distinct failure mode
// - missing header, malformed header, wrong token for the polled (real)
// allocation, a real token that belongs to a *different* real allocation,
// and an allocation ID that does not exist at all - must produce the exact
// same status code and exact same (empty) response body, so nothing about
// the failure reason or the allocation's existence is observable.
func TestWireGuardPeersRejectsInvalidCredentialsIdentically(t *testing.T) {
	store := &fakeAllocationStore{}
	self, token := wireGuardAllocation(t, "rs-self", "profile-a", "10.60.0.1")
	other, otherToken := wireGuardAllocation(t, "rs-other", "profile-a", "10.60.0.2")
	store.put(self)
	store.put(other)

	var status Status
	status.WireGuardPeers = &WireGuardPeersHandler{Store: store}
	handler := status.Handler()

	cases := map[string]struct {
		id   string
		auth string
	}{
		"missing header":                    {id: "rs-self", auth: ""},
		"malformed header no bearer prefix": {id: "rs-self", auth: token},
		"malformed header wrong scheme":     {id: "rs-self", auth: "Basic " + token},
		"empty bearer token":                {id: "rs-self", auth: "Bearer "},
		"non-hex bearer token":              {id: "rs-self", auth: "Bearer not-valid-hex!!"},
		"wrong token for real allocation":   {id: "rs-self", auth: "Bearer " + otherToken},
		"token for a different allocation":  {id: "rs-other", auth: "Bearer " + token},
		"nonexistent allocation ID":         {id: "rs-does-not-exist", auth: "Bearer " + token},
	}

	var firstStatus int
	var firstBody []byte
	first := true
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resp := doPoll(t, handler, tc.id, tc.auth)
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
			if first {
				firstStatus, firstBody, first = resp.StatusCode, body, false
				return
			}
			if resp.StatusCode != firstStatus || string(body) != string(firstBody) {
				t.Fatalf("response diverged from baseline: status %d body %q vs baseline status %d body %q", resp.StatusCode, body, firstStatus, firstBody)
			}
		})
	}
}

// TestWireGuardPeersOwnTokenStopsWorkingAfterLeavingRunning proves
// docs/networking-peer-model.background.md's explicit claim: "Its own poll
// token stops working the moment the controller marks the allocation
// Deleted/TimedOut" - even though its checkpointed hash is untouched, an
// allocation no longer Running must be refused service by its own endpoint.
func TestWireGuardPeersOwnTokenStopsWorkingAfterLeavingRunning(t *testing.T) {
	store := &fakeAllocationStore{}
	self, token := wireGuardAllocation(t, "rs-self", "profile-a", "10.60.0.1")
	self.Phase = lifecycle.Deleted
	store.put(self)

	var status Status
	status.WireGuardPeers = &WireGuardPeersHandler{Store: store}
	handler := status.Handler()

	resp := doPoll(t, handler, "rs-self", "Bearer "+token)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for a Deleted allocation's own token", resp.StatusCode)
	}
}

// TestWireGuardPeersEagerlyRevokesDepartedPeer is the required end-to-end
// regression for "eager" revocation: it drives a real
// lifecycle.Controller.Step call that transitions allocation B out of
// Running, then proves - through the actual HTTP endpoint, not by calling
// wireguard.Snapshot directly - that allocation A's very next poll already
// excludes B, with no separate revoke step and no waiting.
func TestWireGuardPeersEagerlyRevokesDepartedPeer(t *testing.T) {
	store := &fakeAllocationStore{}
	a, tokenA := wireGuardAllocation(t, "rs-a", "profile-a", "10.60.0.1")
	b, _ := wireGuardAllocation(t, "rs-b", "profile-a", "10.60.0.2")
	b.Retire = true // Step's Running branch treats Retire as "leaving Running".
	store.put(a)
	store.put(b)

	var status Status
	status.WireGuardPeers = &WireGuardPeersHandler{Store: store}
	handler := status.Handler()

	resp := doPoll(t, handler, "rs-a", "Bearer "+tokenA)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initial poll status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	before := decodePeers(t, body)
	if len(before) != 1 || before[0].AllocationID != "rs-b" {
		t.Fatalf("expected rs-b in initial snapshot, got %+v", before)
	}

	controller := &lifecycle.Controller{
		Store:     store,
		Providers: map[string]lifecycle.Provider{"noop": noopProvider{}},
		Now:       time.Now,
	}
	if err := controller.Step(context.Background(), "rs-b"); err != nil {
		t.Fatal(err)
	}
	updatedB, err := store.Load(context.Background(), "rs-b")
	if err != nil {
		t.Fatal(err)
	}
	if updatedB.Phase != lifecycle.Deleting {
		t.Fatalf("expected rs-b to leave Running via Step, got phase %q", updatedB.Phase)
	}

	resp = doPoll(t, handler, "rs-a", "Bearer "+tokenA)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post-transition poll status = %d, want 200", resp.StatusCode)
	}
	body, _ = io.ReadAll(resp.Body)
	after := decodePeers(t, body)
	if len(after) != 0 {
		t.Fatalf("rs-b still present in rs-a's peer snapshot immediately after leaving Running: %+v", after)
	}
}

// TestWireGuardPeersListFailureIsAServerErrorNotAnAuthFailure proves a
// backing-store failure after a successfully authenticated request is
// reported distinctly from an auth failure - the caller already proved who
// it is, so 401 would misrepresent the cause.
func TestWireGuardPeersListFailureIsAServerErrorNotAnAuthFailure(t *testing.T) {
	store := &fakeAllocationStore{listErr: errors.New("backing store unavailable")}
	self, token := wireGuardAllocation(t, "rs-self", "profile-a", "10.60.0.1")
	store.put(self)

	var status Status
	status.WireGuardPeers = &WireGuardPeersHandler{Store: store}
	handler := status.Handler()

	resp := doPoll(t, handler, "rs-self", "Bearer "+token)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
}
