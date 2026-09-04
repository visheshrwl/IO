package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/visheshrwl/io/internal/service"
)

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
	databaseURL := envOr("DATABASE_URL", "postgres://postgres:postgres@postgres-17db:5432/notifications?sslmode=disable")
	redisURL := envOr("REDIS_URL", "redis://redis:6379/0")

	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatal(err)
	}
	redisClient := redis.NewClient(options)
	defer redisClient.Close()
	startupCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := waitForDependencies(startupCtx, db, redisClient); err != nil {
		log.Fatal(err)
	}
	if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS notifications (
		id TEXT PRIMARY KEY, recipient TEXT NOT NULL, channel TEXT NOT NULL,
		message TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`); err != nil {
		log.Fatalf("create notifications table: %v", err)
	}

	s := &server{db: db, redis: redisClient}
	mux := http.NewServeMux()
	mux.HandleFunc("/health/live", liveHandler)
	mux.HandleFunc("/health/ready", s.readyHandler)
	mux.HandleFunc("/notifications", s.notificationsHandler)
	mux.HandleFunc("/notifications/", s.notificationHandler)

	port := envOr("PORT", "8080")
	log.Printf("starting notification-service port=%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
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

func (s *server) readyHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil || s.redis.Ping(ctx).Err() != nil {
		http.Error(w, "dependencies unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}

func liveHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
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

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func newID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
