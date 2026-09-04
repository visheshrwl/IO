package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/visheshrwl/io/internal/peer"
	"github.com/visheshrwl/io/internal/platform/logging"
	"github.com/visheshrwl/io/internal/service"
)

const serviceName = "pong-service"

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

	// /peer/pong receives a "pong" from the peer, acknowledges it
	// immediately, then replies with its own independent "ping" HTTP call
	// back to the peer's /peer/ping — fired in a goroutine so the ack
	// returned here isn't blocked on that second network round trip.
	http.HandleFunc("/peer/pong", func(w http.ResponseWriter, r *http.Request) {
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

		metrics.RecordPeerReceived("pong")
		exchangeLog.Add(peer.Exchange{Message: msg, Direction: "received", At: time.Now().UTC()})
		slog.Info("peer message received", "type", "pong", "from", msg.From, "request_id", msg.RequestID, "trace_id", traceID)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "acknowledged"})

		if version == "v2" && service.IsShadowRequest(r) {
			slog.Info("shadow request suppressed", "path", r.URL.Path, "request_id", msg.RequestID, "trace_id", traceID)
			return
		}
		go replyWithPing(peerClient, metrics, exchangeLog, msg.RequestID, traceID)
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

func replyWithPing(peerClient *peer.Client, metrics *service.MetricsStore, exchangeLog *peer.Log, requestID, traceID string) {
	if !peerClient.Configured() {
		slog.Error("peer send failed", "type", "ping", "reason", "peer_not_configured", "request_id", requestID)
		return
	}

	reply := peer.Message{Type: "ping", From: serviceName, RequestID: requestID, SentAt: time.Now().UTC()}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := peerClient.Send(ctx, "/peer/ping", reply, requestID, traceID); err != nil {
		slog.Error("peer send failed", "type", "ping", "request_id", requestID, "err", err)
		return
	}

	metrics.RecordPeerSent("ping")
	exchangeLog.Add(peer.Exchange{Message: reply, Direction: "sent", At: time.Now().UTC()})
	slog.Info("peer message sent", "type", "ping", "request_id", requestID, "trace_id", traceID)
}
