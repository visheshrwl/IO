package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestRunReturnsNilOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	addr := freeAddr(t)

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Addr: addr, Handler: http.NewServeMux(), Logger: discardLogger()})
	}()

	waitForListen(t, addr)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() = %v, want nil on clean shutdown", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

func TestRunDrainsInFlightRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	addr := freeAddr(t)

	released := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		<-released
		_, _ = w.Write([]byte("done"))
	})

	go func() {
		_ = Run(ctx, Options{Addr: addr, Handler: mux, Logger: discardLogger()})
	}()
	waitForListen(t, addr)

	type result struct {
		status int
		body   string
		err    error
	}
	resp := make(chan result, 1)
	go func() {
		r, err := http.Get("http://" + addr + "/slow")
		if err != nil {
			resp <- result{err: err}
			return
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		resp <- result{status: r.StatusCode, body: string(b)}
	}()

	// Give the request time to reach the handler, then start shutdown.
	time.Sleep(100 * time.Millisecond)
	cancel()
	time.Sleep(100 * time.Millisecond)
	close(released)

	select {
	case got := <-resp:
		if got.err != nil {
			t.Fatalf("in-flight request failed during drain: %v", got.err)
		}
		if got.status != http.StatusOK || got.body != "done" {
			t.Fatalf("in-flight request = %d %q, want 200 \"done\"", got.status, got.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request never completed")
	}
}

func waitForListen(t *testing.T, addr string) {
	t.Helper()
	for range 100 {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never started listening on %s", addr)
}
