// Package middleware holds the HTTP middleware every service wraps its
// router in: panic recovery, a correlation ID per request, and one
// structured access-log line per request. Each is an
// http.Handler-decorating func so they compose with Chain.
package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// HeaderRequestID is the canonical correlation-ID header, matched to the
// name Istio and most gateways already use.
const HeaderRequestID = "X-Request-Id"

type contextKey int

const (
	requestIDKey contextKey = iota
	loggerKey
)

// Middleware decorates an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain wraps h with the given middleware. The first argument is the
// outermost layer, so Chain(h, Recover, RequestID) runs Recover first.
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// RequestID ensures every request carries a correlation ID: it reuses an
// inbound X-Request-Id when present (so an ID assigned upstream survives
// the whole call graph) and generates a UUID otherwise. The ID goes onto
// the request context and the response header.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(HeaderRequestID)
			if id == "" {
				id = uuid.NewString()
				r.Header.Set(HeaderRequestID, id)
			}
			w.Header().Set(HeaderRequestID, id)
			ctx := context.WithValue(r.Context(), requestIDKey, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Recover turns a panic in a downstream handler into a 500 and a logged
// error instead of a dropped connection and a crashed goroutine.
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					logger.ErrorContext(r.Context(), "panic recovered",
						"panic", v, "method", r.Method, "path", r.URL.Path,
						"request_id", RequestIDFrom(r.Context()))
					w.WriteHeader(http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog writes one structured line per request with method, path,
// status, byte count, and latency, plus a request-scoped logger on the
// context for handlers to use via LoggerFrom.
func AccessLog(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			reqLogger := logger.With("request_id", RequestIDFrom(r.Context()))
			ctx := context.WithValue(r.Context(), loggerKey, reqLogger)
			rec := &recorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r.WithContext(ctx))

			reqLogger.LogAttrs(r.Context(), slog.LevelInfo, "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int("bytes", rec.bytes),
				slog.Duration("duration", time.Since(start)),
			)
		})
	}
}

// RequestIDFrom returns the correlation ID RequestID put on ctx, or "".
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// LoggerFrom returns the request-scoped logger AccessLog put on ctx,
// falling back to slog.Default when there is none (e.g. in tests).
func LoggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

type recorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *recorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}
