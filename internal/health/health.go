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
	// /healthz and /readyz probes below, on this same HTTP server. Nil by
	// default, and nothing in this codebase constructs a non-nil value here
	// today - internal/configapi/compile.go's network() still rejects every
	// NetworkProfileSpec.Mode other than "separate", so no allocation this
	// codebase's own configuration path can produce ever has anything for
	// this endpoint to serve. This mirrors the same "declared, inert, zero
	// callers" pattern already used by provider.Command.NetworkPeers and
	// operator.Operator.AzureInterruptions: the shape exists so a later,
	// separate, more carefully reviewed change can start constructing one
	// without first landing this wiring.
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
