package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type metricsStore struct {
	mu     sync.Mutex
	counts map[string]uint64
}

func newMetricsStore() *metricsStore {
	return &metricsStore{counts: map[string]uint64{}}
}

func (m *metricsStore) record(method, path, version string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := method + "|" + path + "|" + version
	m.counts[key]++
}

func (m *metricsStore) render(version string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	keys := make([]string, 0, len(m.counts))
	for k := range m.counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("# HELP io_http_requests_total Total HTTP requests received by the service\n")
	b.WriteString("# TYPE io_http_requests_total counter\n")
	for _, k := range keys {
		parts := strings.SplitN(k, "|", 3)
		if len(parts) < 3 {
			continue
		}
		method, path, appVersion := parts[0], parts[1], parts[2]
		b.WriteString("io_http_requests_total{app_version=\"" + appVersion + "\",method=\"" + method + "\",path=\"" + path + "\"} " + strconv.FormatUint(m.counts[k], 10) + "\n")
	}

	b.WriteString("\n# HELP io_app_info Static application metadata\n")
	b.WriteString("# TYPE io_app_info gauge\n")
	b.WriteString("io_app_info{app_version=\"" + version + "\"} 1\n")
	return b.String()
}

func generateRequestID() string {
	return fmt.Sprintf("req-%d", time.Now().UnixNano())
}

func generateTraceID() string {
	return fmt.Sprintf("trace-%d", time.Now().UnixNano())
}

func main() {
	PORT := os.Getenv("PORT")
	if PORT == "" {
		PORT = "8080"
	}

	version := os.Getenv("APP_VERSION")
	if version == "" {
		version = "unknown"
	}

	start := time.Now()
	metrics := newMetricsStore()

	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(metrics.render(version)))
	})

	http.HandleFunc("/health/live", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	http.HandleFunc("/health/ready", func(w http.ResponseWriter, r *http.Request) {
		if time.Since(start) < 2*time.Second {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("warming up"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})

	http.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = generateRequestID()
		}

		traceID := r.Header.Get("X-B3-TraceId")
		if traceID == "" {
			traceID = generateTraceID()
		}

		metrics.record(r.Method, r.URL.Path, version)
		log.Printf("app_version=%s path=%s method=%s request_id=%s trace_id=%s", version, r.URL.Path, r.Method, requestID, traceID)

		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-App-Version", version)
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Trace-ID", traceID)
		_, _ = fmt.Fprintf(w, "Pong from %s!", version)
	})

	log.Printf("starting app version=%s port=%s", version, PORT)
	if err := http.ListenAndServe(":"+PORT, nil); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
