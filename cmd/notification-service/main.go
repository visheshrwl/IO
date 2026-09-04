package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/visheshrwl/io/internal/platform/config"
	"github.com/visheshrwl/io/internal/platform/health"
	"github.com/visheshrwl/io/internal/platform/httpserver"
	"github.com/visheshrwl/io/internal/platform/logging"
	"github.com/visheshrwl/io/internal/platform/middleware"
	"github.com/visheshrwl/io/internal/service"
)

const serviceName = "notification-service"

type appConfig struct {
	config.Base
	DatabaseURL string
	RedisURL    string
}

func loadConfig() (appConfig, error) {
	l := &config.Loader{}
	c := appConfig{Base: config.LoadBase(l, serviceName)}
	c.DatabaseURL = l.Require("DATABASE_URL")
	c.RedisURL = l.Require("REDIS_URL")
	return c, l.Err()
}

type notification struct {
	ID        string    `json:"id"`
	Recipient string    `json:"recipient"`
	Channel   string    `json:"channel"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type createRequest struct {
	Recipient string `json:"recipient"`
	Channel   string `json:"channel"`
	Message   string `json:"message"`
}

type server struct {
	db    *pgxpool.Pool
	redis *redis.Client
}

func main() {
	ctx := context.Background()

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	logger := logging.New(cfg.ServiceName, cfg.Version, cfg.LogLevel)

	fatal := func(msg string, err error) {
		logger.Error(msg, "err", err)
		os.Exit(1)
	}

	db, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal("connect to postgres", err)
	}
	defer db.Close()
	options, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		fatal("parse redis url", err)
	}
	redisClient := redis.NewClient(options)
	defer redisClient.Close()
	startupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := waitForDependencies(startupCtx, db, redisClient); err != nil {
		fatal("wait for dependencies", err)
	}
	if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS notifications (
		id TEXT PRIMARY KEY, recipient TEXT NOT NULL, channel TEXT NOT NULL,
		message TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`); err != nil {
		fatal("create notifications table", err)
	}

	probe := health.New(
		health.Check{Name: "postgres", Func: db.Ping},
		health.Check{Name: "redis", Func: func(ctx context.Context) error { return redisClient.Ping(ctx).Err() }},
	)

	s := &server{db: db, redis: redisClient}
	mux := http.NewServeMux()
	mux.HandleFunc("/health/live", probe.LiveHandler())
	mux.HandleFunc("/health/ready", probe.ReadyHandler())
	mux.HandleFunc("/notifications", s.notificationsHandler)
	mux.HandleFunc("/notifications/", s.notificationHandler)

	srvCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	probe.Ready()

	handler := middleware.Chain(mux,
		middleware.Recover(logger),
		middleware.RequestID(),
		middleware.AccessLog(logger),
	)

	logger.Info("starting", "port", cfg.Port)
	err = httpserver.Run(srvCtx, httpserver.Options{
		Addr:           ":" + cfg.Port,
		Handler:        handler,
		Logger:         logger,
		BeforeShutdown: probe.Draining(cfg.DrainDelay),
	})
	if err != nil {
		fatal("http server", err)
	}
}

func (s *server) notificationsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input createRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil || strings.TrimSpace(input.Recipient) == "" || strings.TrimSpace(input.Channel) == "" || strings.TrimSpace(input.Message) == "" {
		http.Error(w, "recipient, channel, and message are required", http.StatusBadRequest)
		return
	}

	id, err := newID()
	if err != nil {
		http.Error(w, "failed to create notification", http.StatusInternalServerError)
		return
	}
	createdAt := time.Now().UTC()
	if service.IsShadowRequest(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(notification{ID: id, Recipient: input.Recipient, Channel: input.Channel, Message: input.Message, CreatedAt: createdAt})
		return
	}
	_, err = s.db.Exec(r.Context(), `INSERT INTO notifications (id, recipient, channel, message, created_at) VALUES ($1, $2, $3, $4, $5)`, id, input.Recipient, input.Channel, input.Message, createdAt)
	if err != nil {
		http.Error(w, "failed to persist notification", http.StatusInternalServerError)
		return
	}
	n := notification{ID: id, Recipient: input.Recipient, Channel: input.Channel, Message: input.Message, CreatedAt: createdAt}
	payload, _ := json.Marshal(n)
	if err := s.redis.Publish(r.Context(), "notifications", payload).Err(); err != nil {
		http.Error(w, "notification persisted but publish failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(n)
}

func (s *server) notificationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/notifications/")
	if id == "" || strings.Contains(id, "/") {
		http.Error(w, "invalid notification id", http.StatusBadRequest)
		return
	}
	var n notification
	err := s.db.QueryRow(r.Context(), `SELECT id, recipient, channel, message, created_at FROM notifications WHERE id = $1`, id).Scan(&n.ID, &n.Recipient, &n.Channel, &n.Message, &n.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "failed to read notification", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(n)
}

func waitForDependencies(ctx context.Context, db *pgxpool.Pool, redisClient *redis.Client) error {
	var lastError error
	for {
		if err := db.Ping(ctx); err != nil {
			lastError = err
		} else if err := redisClient.Ping(ctx).Err(); err != nil {
			lastError = err
		} else {
			return nil
		}

		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), lastError)
		case <-time.After(2 * time.Second):
		}
	}
}

func newID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
