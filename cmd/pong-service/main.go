package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/visheshrwl/io/internal/peer"
	"github.com/visheshrwl/io/internal/platform/config"
	"github.com/visheshrwl/io/internal/platform/health"
	"github.com/visheshrwl/io/internal/platform/httpserver"
	"github.com/visheshrwl/io/internal/platform/logging"
	"github.com/visheshrwl/io/internal/platform/middleware"
	"github.com/visheshrwl/io/internal/service"
)

const serviceName = "pong-service"

type appConfig struct {
	config.Base
	PeerURL string
}

func loadConfig() (appConfig, error) {
	l := &config.Loader{}
	c := appConfig{Base: config.LoadBase(l, serviceName)}
	c.PeerURL = l.String("PEER_URL", "")
	return c, l.Err()
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	logger := logging.New(cfg.ServiceName, cfg.Version, cfg.LogLevel)
	version := cfg.Version
	port := cfg.Port
	peerURL := cfg.PeerURL

	metrics := service.NewMetricsStore()
	peerClient := peer.NewClient(peerURL)
	exchangeLog := peer.NewLog(20)
	probe := health.New()

	mux := http.NewServeMux()

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(metrics.Render(serviceName, version)))
	})

	mux.HandleFunc("/health/live", probe.LiveHandler())
	mux.HandleFunc("/health/ready", probe.ReadyHandler())

	// /peer/pong receives a "pong" from the peer, acknowledges it
	// immediately, then replies with its own independent "ping" HTTP call
	// back to the peer's /peer/ping — fired in a goroutine so the ack
	// returned here isn't blocked on that second network round trip.
	mux.HandleFunc("/peer/pong", func(w http.ResponseWriter, r *http.Request) {
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

	mux.HandleFunc("/peer/log", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(exchangeLog.Recent())
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	probe.Ready()

	handler := middleware.Chain(mux,
		middleware.Recover(logger),
		middleware.RequestID(),
		middleware.AccessLog(logger),
	)

	logger.Info("starting", "port", port, "peer_url", peerURL)
	err = httpserver.Run(ctx, httpserver.Options{
		Addr:           ":" + port,
		Handler:        handler,
		Logger:         logger,
		BeforeShutdown: probe.Draining(cfg.DrainDelay),
	})
	if err != nil {
		logger.Error("server exited with error", "err", err)
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
