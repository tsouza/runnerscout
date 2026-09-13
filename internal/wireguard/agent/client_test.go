package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tsouza/runnerscout/internal/wireguard"
)

func TestPollSendsBearerTokenAndAllocationPathAndDecodesPeers(t *testing.T) {
	var sawPath, sawAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath, sawAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Peers []wireguard.Peer `json:"peers"`
		}{Peers: []wireguard.Peer{{AllocationID: "rs-a", PublicKey: "cGVlci1wdWJsaWMta2V5", OverlayAddress: "10.60.0.1"}}})
	}))
	defer server.Close()

	peers, err := Poll(context.Background(), server.Client(), server.URL, "rs-self", "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	if sawPath != "/v1/wireguard/peers/rs-self" {
		t.Fatalf("unexpected path: %q", sawPath)
	}
	if sawAuth != "Bearer fixture-token" {
		t.Fatalf("unexpected Authorization header: %q", sawAuth)
	}
	if len(peers) != 1 || peers[0].AllocationID != "rs-a" {
		t.Fatalf("unexpected peers: %+v", peers)
	}
}

func TestPollFailsOnNon200Status(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	if _, err := Poll(context.Background(), server.Client(), server.URL, "rs-self", "wrong-token"); err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}

func TestPollFailsOnUnreachableController(t *testing.T) {
	if _, err := Poll(context.Background(), http.DefaultClient, "http://127.0.0.1:1", "rs-self", "fixture-token"); err == nil {
		t.Fatal("expected an error for an unreachable controller")
	}
}
