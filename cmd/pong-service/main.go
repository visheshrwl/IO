package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/visheshrwl/io/internal/peer"
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
		log.Printf("peer_received type=pong from=%s request_id=%s trace_id=%s", msg.From, msg.RequestID, traceID)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "acknowledged"})

		if version == "v2" && service.IsShadowRequest(r) {
			log.Printf("shadow_request_suppressed path=%s request_id=%s trace_id=%s", r.URL.Path, msg.RequestID, traceID)
			return
		}
		go replyWithPing(peerClient, metrics, exchangeLog, msg.RequestID, traceID)
	})

	http.HandleFunc("/peer/log", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(exchangeLog.Recent())
	})

	log.Printf("starting service=%s app_version=%s port=%s peer_url=%q", serviceName, version, port, peerURL)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

func replyWithPing(peerClient *peer.Client, metrics *service.MetricsStore, exchangeLog *peer.Log, requestID, traceID string) {
	if !peerClient.Configured() {
		log.Printf("peer_send_failed type=ping reason=peer_not_configured request_id=%s", requestID)
		return
	}

	reply := peer.Message{Type: "ping", From: serviceName, RequestID: requestID, SentAt: time.Now().UTC()}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := peerClient.Send(ctx, "/peer/ping", reply, requestID, traceID); err != nil {
		log.Printf("peer_send_failed type=ping request_id=%s err=%v", requestID, err)
		return
	}

	metrics.RecordPeerSent("ping")
	exchangeLog.Add(peer.Exchange{Message: reply, Direction: "sent", At: time.Now().UTC()})
	log.Printf("peer_sent type=ping request_id=%s trace_id=%s", requestID, traceID)
}
