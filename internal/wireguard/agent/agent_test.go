package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tsouza/runnerscout/internal/wireguard"
	"github.com/tsouza/runnerscout/internal/wireguard/tunnel"
)

func writePayload(t *testing.T, payload wireguard.CloudInitPayload) string {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "wireguard.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadPayloadRejectsMissingRequiredFields(t *testing.T) {
	path := writePayload(t, wireguard.CloudInitPayload{PrivateKey: "x"})
	if _, err := LoadPayload(path); err == nil {
		t.Fatal("expected an error for a payload missing required fields")
	}
}

func TestLoadPayloadFailsOnMissingFile(t *testing.T) {
	if _, err := LoadPayload(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("expected an error for a missing payload file")
	}
}

func TestInitialConfigBuildsTunnelConfigAndCurrentSet(t *testing.T) {
	pubA := keyFixture(1)
	payload := wireguard.CloudInitPayload{
		PrivateKey:     base64.StdEncoding.EncodeToString(make([]byte, wireguard.KeySize)),
		AllocationID:   "rs-self",
		ControllerURL:  "https://controller.internal",
		PollToken:      "fixture-token",
		OverlayAddress: "10.60.0.9",
		Peers:          []wireguard.Peer{peerFixture("rs-a", pubA, "10.60.0.1", "10.0.0.1:51820")},
	}
	cfg, current, err := InitialConfig(payload)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenPort != wireguard.DefaultListenPort {
		t.Fatalf("expected the fixed default listen port, got %d", cfg.ListenPort)
	}
	if cfg.OverlayAddress.String() != "10.60.0.9" {
		t.Fatalf("unexpected overlay address: %v", cfg.OverlayAddress)
	}
	if len(cfg.Peers) != 1 || cfg.Peers[0].PublicKey != pubA {
		t.Fatalf("unexpected initial peer list: %+v", cfg.Peers)
	}
	if len(current) != 1 || current[pubA].AllocationID != "rs-a" {
		t.Fatalf("unexpected initial current set: %+v", current)
	}
}

func TestInitialConfigSkipsMalformedPeerButKeepsOthers(t *testing.T) {
	pubA := keyFixture(1)
	malformed := wireguard.Peer{AllocationID: "rs-bad", PublicKey: "not-valid!!", OverlayAddress: "10.60.0.9"}
	payload := wireguard.CloudInitPayload{
		PrivateKey:     base64.StdEncoding.EncodeToString(make([]byte, wireguard.KeySize)),
		OverlayAddress: "10.60.0.9",
		Peers:          []wireguard.Peer{malformed, peerFixture("rs-a", pubA, "10.60.0.1", "")},
	}
	cfg, current, err := InitialConfig(payload)
	if err == nil {
		t.Fatal("expected an error describing the malformed peer")
	}
	if len(cfg.Peers) != 1 || len(current) != 1 {
		t.Fatalf("well-formed peer was not still included: cfg=%+v current=%+v", cfg.Peers, current)
	}
}

// TestRunBringsUpConfiguresInitialPeersThenReconcilesOnPoll is this
// package's end-to-end wiring proof: given a real cloud-init payload file
// with one initial peer, Run must bring up a tunnel with that peer already
// configured (never added again via AddPeer - it came from BringUp itself),
// then converge to a second poll response that revokes it (RemovePeer) and
// adds a different peer (AddPeer) - all on Options.PollInterval's cadence,
// stopping cleanly on context cancellation.
func TestRunBringsUpConfiguresInitialPeersThenReconcilesOnPoll(t *testing.T) {
	pubInitial, pubLater := keyFixture(1), keyFixture(2)
	var reqCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer fixture-token" {
			t.Errorf("unexpected Authorization header: %q", got)
		}
		if got := r.URL.Path; got != "/v1/wireguard/peers/rs-self" {
			t.Errorf("unexpected path: %q", got)
		}
		n := atomic.AddInt32(&reqCount, 1)
		peers := []wireguard.Peer{peerFixture("rs-initial", pubInitial, "10.60.0.1", "")}
		if n >= 2 {
			peers = []wireguard.Peer{peerFixture("rs-later", pubLater, "10.60.0.2", "")}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Peers []wireguard.Peer `json:"peers"`
		}{Peers: peers})
	}))
	defer server.Close()

	path := writePayload(t, wireguard.CloudInitPayload{
		PrivateKey:     base64.StdEncoding.EncodeToString(make([]byte, wireguard.KeySize)),
		AllocationID:   "rs-self",
		ControllerURL:  server.URL,
		PollToken:      "fixture-token",
		OverlayAddress: "10.60.0.9",
		Peers:          []wireguard.Peer{peerFixture("rs-initial", pubInitial, "10.60.0.1", "")},
	})

	ft := &fakeTunnel{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			PayloadPath:  path,
			PollInterval: 5 * time.Millisecond,
			BringUp:      func(tunnel.Config) (Closer, error) { return fakeCloser{ft}, nil },
			Logf:         func(string, ...any) {},
		})
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		_, removed := ft.snapshot()
		if len(removed) >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the revoked peer to be removed")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected Run error: %v", err)
	}

	added, removed := ft.snapshot()
	// The initial peer must never be re-added via AddPeer - BringUp itself
	// (fakeCloser here) is what configures it; only rs-later should ever
	// reach AddPeer.
	for _, pub := range added {
		if pub == pubInitial {
			t.Fatal("initial peer was redundantly added via AddPeer")
		}
	}
	if len(added) != 1 || added[0] != pubLater {
		t.Fatalf("expected AddPeer(later) only, got %+v", added)
	}
	if len(removed) != 1 || removed[0] != pubInitial {
		t.Fatalf("expected RemovePeer(initial), got %+v", removed)
	}
}
