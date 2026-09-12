// Package health exposes process liveness separately from controller readiness.
package health

import (
	"net/http"
	"sync/atomic"
)

// Status is safe for the HTTP server and controller reconciliation goroutines.
// Readiness means the leader has opened its scale-set session and its latest
// reconciliation succeeded. It does not prove capacity or a successful job.
type Status struct{ ready atomic.Bool }

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
	return mux
}
