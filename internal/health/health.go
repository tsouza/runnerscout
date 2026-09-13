// Package health exposes process liveness separately from controller readiness.
package health

import (
	"net/http"
	"sync/atomic"
)

// Status is safe for the HTTP server and controller reconciliation goroutines.
// Readiness means the leader has opened its scale-set session and its latest
// reconciliation succeeded. It does not prove capacity or a successful job.
type Status struct {
	ready atomic.Bool
	// WireGuardPeers optionally mounts the narrow, allocation-scoped,
	// bearer-token-authenticated WireGuard peer-poll endpoint
	// (docs/networking-peer-model.md's "Revocation" section) alongside the
	// /healthz and /readyz probes below, on this same HTTP server. Both
	// cmd/runnerscout/main.go entry points (mounted-config and CRD-driven)
	// always construct a real, non-nil *health.WireGuardPeersHandler here
	// (see wireGuardPeersHandler in main.go) - it is never gated behind a
	// Config opt-in, since it costs nothing beyond one idle
	// internal/state.Kubernetes value and is a complete no-op for any
	// allocation whose NetworkProfile is "" (see
	// WireGuardPeersHandler.ServeHTTP's own eligibility check). nil remains
	// a valid value (e.g. in tests exercising only /healthz and /readyz);
	// Handler below mounts the route only when it is non-nil.
	WireGuardPeers http.Handler
}

func (s *Status) SetReady(ready bool) { s.ready.Store(ready) }

func (s *Status) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("alive\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if !s.ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	if s.WireGuardPeers != nil {
		mux.Handle("GET /v1/wireguard/peers/{id}", s.WireGuardPeers)
	}
	return mux
}
