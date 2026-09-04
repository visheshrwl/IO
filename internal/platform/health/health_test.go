package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func status(t *testing.T, h http.HandlerFunc) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	return rec.Code
}

func TestProbeLifecycle(t *testing.T) {
	p := New()

	if got := status(t, p.ReadyHandler()); got != http.StatusServiceUnavailable {
		t.Errorf("new probe ready = %d, want 503", got)
	}

	p.Ready()
	if got := status(t, p.ReadyHandler()); got != http.StatusOK {
		t.Errorf("after Ready = %d, want 200", got)
	}

	p.NotReady()
	if got := status(t, p.ReadyHandler()); got != http.StatusServiceUnavailable {
		t.Errorf("after NotReady = %d, want 503", got)
	}
}

func TestProbeLivenessAlwaysOK(t *testing.T) {
	p := New()
	if got := status(t, p.LiveHandler()); got != http.StatusOK {
		t.Errorf("liveness = %d, want 200 even before Ready", got)
	}
}

func TestProbeDependencyChecks(t *testing.T) {
	failing := New(Check{Name: "db", Func: func(context.Context) error { return errors.New("down") }})
	failing.Ready()
	if got := status(t, failing.ReadyHandler()); got != http.StatusServiceUnavailable {
		t.Errorf("failing check ready = %d, want 503", got)
	}

	passing := New(Check{Name: "db", Func: func(context.Context) error { return nil }})
	passing.Ready()
	if got := status(t, passing.ReadyHandler()); got != http.StatusOK {
		t.Errorf("passing check ready = %d, want 200", got)
	}
}

func TestProbeDraining(t *testing.T) {
	p := New()
	p.Ready()

	start := time.Now()
	p.Draining(20 * time.Millisecond)()
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Errorf("Draining returned after %v, want >= 20ms", elapsed)
	}
	if got := status(t, p.ReadyHandler()); got != http.StatusServiceUnavailable {
		t.Errorf("after Draining ready = %d, want 503", got)
	}
}
