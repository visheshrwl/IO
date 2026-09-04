package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/visheshrwl/io/internal/peer"
	"github.com/visheshrwl/io/internal/platform/logging"
	"github.com/visheshrwl/io/internal/service"
)

const serviceName = "io"

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	version := os.Getenv("APP_VERSION")
	if version == "" {
		version = "unknown"
	}

	peerURL := os.Getenv("PEER_URL")

	logger := logging.New(serviceName, version, os.Getenv("LOG_LEVEL"))

	start := time.Now()
	metrics := service.NewMetricsStore()
	peerClient := peer.NewClient(peerURL)
	exchangeLog := peer.NewLog(20)

	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(metrics.Render(serviceName, version)))
	})

	http.HandleFunc("/health/live", service.LivenessHandler())
	http.HandleFunc("/health/ready", service.ReadinessHandler(start, 2*time.Second))

	http.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		requestID := service.RequestID(r)
		traceID := service.TraceID(r)

		metrics.RecordHTTP(r.Method, r.URL.Path, version)
		slog.Info("handled request", "path", r.URL.Path, "method", r.Method, "request_id", requestID, "trace_id", traceID)

		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-App-Version", version)
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Trace-ID", traceID)
		_, _ = fmt.Fprintf(w, "Pong from %s!", version)
	})

	// /peer/trigger kicks off a real, asynchronous inter-service exchange:
	// this service sends a "pong" message to the peer over HTTP, and the
	// peer replies with its own separate "ping" HTTP call back to /peer/ping
	// below. The two hops are independent network round trips, not a single
	// request/response.
	http.HandleFunc("/peer/trigger", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		requestID := service.RequestID(r)
		traceID := service.TraceID(r)
		metrics.RecordHTTP(r.Method, r.URL.Path, version)
		if version == "v2" && service.IsShadowRequest(r) {
			slog.Info("shadow request suppressed", "path", r.URL.Path, "request_id", requestID, "trace_id", traceID)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status":     "shadowed",
				"request_id": requestID,
				"note":       "side effect suppressed for Istio mirror",
			})
			return
		}

		if !peerClient.Configured() {
			slog.Error("peer trigger failed", "reason", "peer_not_configured", "request_id", requestID)
			http.Error(w, "PEER_URL is not configured", http.StatusInternalServerError)
			return
		}

		msg := peer.Message{Type: "pong", From: serviceName, RequestID: requestID, SentAt: time.Now().UTC()}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		if err := peerClient.Send(ctx, "/peer/pong", msg, requestID, traceID); err != nil {
			slog.Error("peer send failed", "type", "pong", "request_id", requestID, "err", err)
			http.Error(w, "failed to reach peer: "+err.Error(), http.StatusBadGateway)
			return
		}

		metrics.RecordPeerSent("pong")
		exchangeLog.Add(peer.Exchange{Message: msg, Direction: "sent", At: time.Now().UTC()})
		slog.Info("peer message sent", "type", "pong", "request_id", requestID, "trace_id", traceID)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":     "dispatched",
			"message":    "pong",
			"request_id": requestID,
			"note":       "reply will arrive asynchronously at /peer/ping; check /peer/log",
		})
	})

	// /peer/ping receives the peer's reply to a pong this service sent.
	http.HandleFunc("/peer/ping", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		traceID := service.TraceID(r)
		metrics.RecordHTTP(r.Method, r.URL.Path, version)

		msg, err := peer.DecodeMessage(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		metrics.RecordPeerReceived("ping")
		exchangeLog.Add(peer.Exchange{Message: msg, Direction: "received", At: time.Now().UTC()})
		slog.Info("peer message received", "type", "ping", "from", msg.From, "request_id", msg.RequestID, "trace_id", traceID, "round_trip_complete", true)

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "acknowledged"})
	})

	http.HandleFunc("/peer/log", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(exchangeLog.Recent())
	})

	logger.Info("starting", "port", port, "peer_url", peerURL)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		logger.Error("server failed", "err", err)
		os.Exit(1)
	}
}
