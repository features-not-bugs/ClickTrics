// Package httpobs exposes a lightweight HTTP server for self-observability:
//
//	/healthz  — liveness (always 200 once started)
//	/readyz   — readiness (200 if exporter is connected)
//	/metrics  — Prometheus scrape endpoint exposing clicktrics_* counters
package httpobs

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server is the HTTP observability server.
type Server struct {
	addr  string
	srv   *http.Server
	ready atomic.Bool
}

// New constructs a server bound to addr (e.g. ":9090").
func New(addr string) *Server {
	s := &Server{addr: addr}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	mux.Handle("/metrics", promhttp.Handler())
	s.srv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// SetReady toggles the readiness gate.
func (s *Server) SetReady(v bool) { s.ready.Store(v) }

// Start binds and serves in a goroutine. Errors surface on the returned
// channel (non-blocking).
func (s *Server) Start() <-chan error {
	ch := make(chan error, 1)
	go func() {
		if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			ch <- err
		}
		close(ch)
	}()
	return ch
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
