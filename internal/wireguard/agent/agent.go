package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"os"
	"time"

	"github.com/tsouza/runnerscout/internal/wireguard"
	"github.com/tsouza/runnerscout/internal/wireguard/tunnel"
)

// DefaultPollInterval is how often Run polls
// internal/health.WireGuardPeersHandler when Options.PollInterval is zero.
// docs/networking-peer-model.md's "What this document does not decide"
// section deliberately left the polling interval unset; this is this
// package's own concrete choice, made because a real VM-side agent needs
// one - see docs/networking-peer-model.background.md's "Why revocation is
// push-then-poll" for why the bound is "one polling interval" regardless of
// its exact value. 30 seconds bounds convergence tightly enough for the
// spot-interruption/retirement timescales this codebase already operates
// on (minutes, not seconds) without generating meaningful load against
// WireGuardPeersHandler's own documented "no rate limiter" posture (its doc
// comment's only accepted abuse scenario is a poller running "far faster
// than any intended interval").
const DefaultPollInterval = 30 * time.Second

// LoadPayload reads and validates the cloud-init-delivered
// wireguard.CloudInitPayload JSON from path - matching exactly where
// provider.BootstrapWithWireGuard writes it, /run/runnerscout/wireguard.json
// (this package does not hard-code that path itself; the caller/flag
// default does - see cmd/runnerscout-wireguard-agent). Every field Run
// needs (PrivateKey, OverlayAddress, AllocationID, ControllerURL, PollToken)
// must be present; Peers may legitimately be empty (a lone allocation with
// no other Running peer yet).
func LoadPayload(path string) (wireguard.CloudInitPayload, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return wireguard.CloudInitPayload{}, fmt.Errorf("agent: read payload file: %w", err)
	}
	var payload wireguard.CloudInitPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return wireguard.CloudInitPayload{}, fmt.Errorf("agent: decode payload: %w", err)
	}
	if payload.PrivateKey == "" || payload.OverlayAddress == "" || payload.AllocationID == "" || payload.ControllerURL == "" || payload.PollToken == "" {
		return wireguard.CloudInitPayload{}, errors.New("agent: payload missing a required field")
	}
	return payload, nil
}

// InitialConfig builds the tunnel.Config BringUp needs from payload,
// including its initial peer list decoded via DecodePeer, and the "current"
// membership map Reconcile's first call should start from - a peer already
// configured by BringUp must never be handed to AddPeer again on the first
// poll's reconcile pass. Every peer's ListenPort is
// wireguard.DefaultListenPort, never zero/ephemeral: Peer.Endpoint (this
// allocation's own outer address, as every OTHER peer would compute it) is
// only correct if this side actually listens where every other peer assumes
// it does.
//
// The two returned errors are never interchangeable. err is fatal: a
// malformed top-level field (PrivateKey, OverlayAddress) leaves cfg a
// useless zero value, and the caller must stop before ever calling
// bringUp with it - treating this as "skipped peers" once produced a
// misleading log line ("some initial peers were skipped") immediately
// followed by an unrelated tunnel.BringUp failure ("overlay address is
// required"), masking the real cause. skipped is the opposite: a
// malformed peer in payload.Peers (matching Reconcile's own tolerance)
// leaves cfg otherwise fully valid, and is purely informational - one bad
// entry must never prevent bringing up connectivity to every other peer.
func InitialConfig(payload wireguard.CloudInitPayload) (cfg tunnel.Config, current map[wireguard.PublicKey]wireguard.Peer, skipped, err error) {
	raw, err := base64.StdEncoding.DecodeString(payload.PrivateKey)
	if err != nil || len(raw) != wireguard.KeySize {
		return tunnel.Config{}, nil, nil, fmt.Errorf("agent: payload has a malformed private key: %v", err)
	}
	var priv wireguard.PrivateKey
	copy(priv[:], raw)
	overlay, err := netip.ParseAddr(payload.OverlayAddress)
	if err != nil {
		return tunnel.Config{}, nil, nil, fmt.Errorf("agent: payload has a malformed overlay address %q: %w", payload.OverlayAddress, err)
	}
	current = make(map[wireguard.PublicKey]wireguard.Peer, len(payload.Peers))
	var peers []tunnel.Peer
	var errs []error
	for _, p := range payload.Peers {
		decoded, err := DecodePeer(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		peers = append(peers, decoded)
		current[decoded.PublicKey] = p
	}
	cfg = tunnel.Config{PrivateKey: priv, OverlayAddress: overlay, ListenPort: wireguard.DefaultListenPort, Peers: peers}
	return cfg, current, errors.Join(errs...), nil
}

// Closer is the subset of *tunnel.Tunnel Run needs beyond Tunnel: releasing
// the device/UDP socket on shutdown.
type Closer interface {
	Tunnel
	Close() error
}

// Options configures Run. Every field has a documented zero-value default,
// so a caller (cmd/runnerscout-wireguard-agent's main) only needs to set
// PayloadPath.
type Options struct {
	// PayloadPath is where LoadPayload reads the cloud-init-delivered
	// CloudInitPayload JSON from. Required.
	PayloadPath string
	// PollInterval is how often Run polls the controller for peer updates.
	// Zero uses DefaultPollInterval.
	PollInterval time.Duration
	// HTTPClient issues each poll request. Zero uses http.DefaultClient.
	HTTPClient *http.Client
	// Logf receives one line per notable, non-fatal event - a poll
	// failure, a reconcile error - this task's "log and continue"
	// requirement, not a structured logging framework. Zero uses
	// log.Printf.
	Logf func(format string, args ...any)
	// BringUp constructs the tunnel this loop configures and later closes.
	// Zero wraps the real tunnel.BringUp. Tests substitute a fake Closer to
	// exercise the poll/reconcile wiring without a real netstack device.
	BringUp func(tunnel.Config) (Closer, error)
}

// Run is the VM-side WireGuard agent's whole lifetime: it loads opts.
// PayloadPath (LoadPayload), brings up a tunnel with its initial peer list
// (InitialConfig + opts.BringUp, defaulting to the real tunnel.BringUp),
// then polls the controller (Poll) on opts.PollInterval's cadence,
// reconciling (Reconcile) the tunnel's peer set against each response,
// until ctx is done. A transient poll failure is logged and skipped - this
// task's "try again next interval on a transient HTTP error, log and
// continue" scope, deliberately with no retry/backoff machinery of its own,
// since the next tick already provides that. Reconcile errors (a malformed
// peer, a failed AddPeer/RemovePeer call) are likewise logged, never fatal
// to the loop - Reconcile's own returned membership map already ensures the
// next poll retries exactly what did not converge.
func Run(ctx context.Context, opts Options) error {
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	bringUp := opts.BringUp
	if bringUp == nil {
		bringUp = func(cfg tunnel.Config) (Closer, error) { return tunnel.BringUp(cfg) }
	}

	payload, err := LoadPayload(opts.PayloadPath)
	if err != nil {
		return err
	}
	cfg, current, skipped, err := InitialConfig(payload)
	if err != nil {
		return fmt.Errorf("agent: initial config: %w", err)
	}
	if skipped != nil {
		opts.Logf("agent: some initial peers were skipped: %v", skipped)
	}
	tun, err := bringUp(cfg)
	if err != nil {
		return fmt.Errorf("agent: bring up tunnel: %w", err)
	}
	defer func() {
		if closeErr := tun.Close(); closeErr != nil {
			opts.Logf("agent: close tunnel: %v", closeErr)
		}
	}()

	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			peers, pollErr := Poll(ctx, opts.HTTPClient, payload.ControllerURL, payload.AllocationID, payload.PollToken)
			if pollErr != nil {
				opts.Logf("agent: poll failed, retrying next interval: %v", pollErr)
				continue
			}
			var reconcileErr error
			current, reconcileErr = Reconcile(tun, current, peers)
			if reconcileErr != nil {
				opts.Logf("agent: reconcile encountered errors: %v", reconcileErr)
			}
		}
	}
}
