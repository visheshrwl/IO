// Package health provides Kubernetes-style liveness and readiness probes.
//
// Liveness answers "is the process healthy enough to keep running" — a
// failure gets the container restarted. Readiness answers "should this pod
// receive traffic right now" — a failure only pulls the pod out of the
// Service load-balancer. They are different questions: a pod that has lost
// its database is not ready, but restarting it will not bring the database
// back, so it is still live.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"
)

// DefaultDrainDelay is how long a service keeps serving after it starts
// reporting "not ready", giving the Kubernetes endpoints controller time to
// remove the pod from Service routing before connections are closed.
const DefaultDrainDelay = 5 * time.Second

// Check is a named readiness dependency, e.g. a database ping.
type Check struct {
	Name string
	Func func(ctx context.Context) error
}

// Probe holds the readiness state and dependency checks for one service.
// A new Probe is not ready until Ready is called, so a pod never receives
// traffic while main is still wiring things up.
type Probe struct {
	ready        atomic.Bool
	checkTimeout time.Duration
	checks       []Check
}

// New returns a Probe with the given dependency checks. It starts not ready.
func New(checks ...Check) *Probe {
	return &Probe{checkTimeout: 2 * time.Second, checks: checks}
}

// Ready marks the service ready to receive traffic.
func (p *Probe) Ready() { p.ready.Store(true) }

// NotReady marks the service as draining. Call it on shutdown, before the
// HTTP server stops accepting connections, so Kubernetes removes the pod
// from the Service endpoints while it can still serve the requests already
// in flight.
func (p *Probe) NotReady() { p.ready.Store(false) }

// Draining returns a func for httpserver.Options.BeforeShutdown: it flips
// readiness to draining, then blocks for delay so the pod leaves Service
// routing before the server stops accepting connections.
func (p *Probe) Draining(delay time.Duration) func() {
	return func() {
		p.NotReady()
		time.Sleep(delay)
	}
}

// LiveHandler reports healthy as soon as the process is serving.
func (p *Probe) LiveHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// ReadyHandler reports 200 only when the service has been marked Ready and
// every dependency check passes; otherwise 503 with the failing details.
func (p *Probe) ReadyHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{"status": "ready"}

		if !p.ready.Load() {
			body["status"] = "draining"
			writeJSON(w, http.StatusServiceUnavailable, body)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), p.checkTimeout)
		defer cancel()

		deps := map[string]string{}
		healthy := true
		for _, c := range p.checks {
			if err := c.Func(ctx); err != nil {
				deps[c.Name] = err.Error()
				healthy = false
				continue
			}
			deps[c.Name] = "ok"
		}
		if len(deps) > 0 {
			body["dependencies"] = deps
		}
		if !healthy {
			body["status"] = "unhealthy"
			writeJSON(w, http.StatusServiceUnavailable, body)
			return
		}
		writeJSON(w, http.StatusOK, body)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
