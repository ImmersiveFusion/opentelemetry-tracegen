// Package health serves Kubernetes readiness and liveness over HTTP for a
// ticker-driven, no-inbound-traffic process. Readiness flips once startup is
// complete; liveness stays green only while the work loop keeps calling Beat,
// so a hung (not crashed) generator goes stale and gets restarted.
package health

import (
	"net/http"
	"sync/atomic"
	"time"
)

// Monitor tracks process readiness and a work-loop heartbeat and answers
// /readyz and /healthz. The zero value is not usable; call New.
type Monitor struct {
	ready      atomic.Bool
	lastBeat   atomic.Int64 // unix nanos of the last Beat
	staleAfter time.Duration
	now        func() time.Time // injectable for tests
}

// New returns a Monitor that reports unhealthy once no Beat has arrived for
// staleAfter. Startup counts as the first beat, so a process that has not yet
// completed a work cycle is still live during its initial staleAfter window.
func New(staleAfter time.Duration) *Monitor {
	m := &Monitor{staleAfter: staleAfter, now: time.Now}
	m.lastBeat.Store(m.now().UnixNano())
	return m
}

// Ready marks startup complete: /readyz returns 200 from here on.
func (m *Monitor) Ready() { m.ready.Store(true) }

// Beat records forward progress in the work loop. /healthz stays green as long
// as beats keep arriving within staleAfter.
func (m *Monitor) Beat() { m.lastBeat.Store(m.now().UnixNano()) }

func (m *Monitor) readyz(w http.ResponseWriter, _ *http.Request) {
	if m.ready.Load() {
		writeStatus(w, http.StatusOK, "ready\n")
		return
	}
	writeStatus(w, http.StatusServiceUnavailable, "starting\n")
}

func (m *Monitor) healthz(w http.ResponseWriter, _ *http.Request) {
	age := m.now().Sub(time.Unix(0, m.lastBeat.Load()))
	if m.ready.Load() && age < m.staleAfter {
		writeStatus(w, http.StatusOK, "ok\n")
		return
	}
	writeStatus(w, http.StatusServiceUnavailable, "stale\n")
}

func writeStatus(w http.ResponseWriter, code int, body string) {
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

// Handler returns the health mux (/readyz, /healthz). Exposed for testing and
// for callers that want to mount it on their own server.
func (m *Monitor) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", m.readyz)
	mux.HandleFunc("/healthz", m.healthz)
	return mux
}

// Serve starts the health server on addr in a background goroutine and returns
// immediately. A serve error other than a clean shutdown is passed to onErr (if
// non-nil). addr is a standard net address such as ":8080".
func (m *Monitor) Serve(addr string, onErr func(error)) {
	srv := &http.Server{
		Addr:              addr,
		Handler:           m.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed && onErr != nil {
			onErr(err)
		}
	}()
}
