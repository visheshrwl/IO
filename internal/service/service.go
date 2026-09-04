// Package service provides the HTTP scaffolding shared by every binary in
// this repo: request/trace IDs and Prometheus counters. Each cmd/ binary
// wires these into its own routes.
package service

import (
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MetricsStore tracks HTTP request counts and peer-to-peer message counts,
// rendered together in Prometheus text format.
type MetricsStore struct {
	mu         sync.Mutex
	httpCounts map[string]uint64 // method|path|version
	peerSent   map[string]uint64 // message type
	peerRecv   map[string]uint64 // message type
}

// IsShadowRequest identifies requests mirrored by Istio, whose Host header
// receives a -shadow suffix. Applications use this to suppress side effects
// while still exercising request validation and response behavior.
func IsShadowRequest(r *http.Request) bool {
	host := strings.ToLower(r.Host)
	if hostName, _, err := net.SplitHostPort(host); err == nil {
		host = hostName
	}
	return strings.HasSuffix(host, "-shadow")
}

func NewMetricsStore() *MetricsStore {
	return &MetricsStore{
		httpCounts: map[string]uint64{},
		peerSent:   map[string]uint64{},
		peerRecv:   map[string]uint64{},
	}
}

func (m *MetricsStore) RecordHTTP(method, path, version string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.httpCounts[method+"|"+path+"|"+version]++
}

func (m *MetricsStore) RecordPeerSent(messageType string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.peerSent[messageType]++
}

func (m *MetricsStore) RecordPeerReceived(messageType string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.peerRecv[messageType]++
}

// Render returns Prometheus-format metrics. serviceName distinguishes
// multiple services scraped under the same metric names (e.g. "io" vs
// "pong-service") via a `service` label.
func (m *MetricsStore) Render(serviceName, version string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var b strings.Builder

	b.WriteString("# HELP io_http_requests_total Total HTTP requests received by the service\n")
	b.WriteString("# TYPE io_http_requests_total counter\n")
	for _, k := range sortedKeys(m.httpCounts) {
		parts := strings.SplitN(k, "|", 3)
		if len(parts) < 3 {
			continue
		}
		method, path, appVersion := parts[0], parts[1], parts[2]
		fmt.Fprintf(&b, "io_http_requests_total{service=%q,app_version=%q,method=%q,path=%q} %s\n",
			serviceName, appVersion, method, path, strconv.FormatUint(m.httpCounts[k], 10))
	}

	b.WriteString("\n# HELP io_peer_messages_sent_total Peer-to-peer messages sent to the other service\n")
	b.WriteString("# TYPE io_peer_messages_sent_total counter\n")
	for _, k := range sortedKeys(m.peerSent) {
		fmt.Fprintf(&b, "io_peer_messages_sent_total{service=%q,message_type=%q} %s\n",
			serviceName, k, strconv.FormatUint(m.peerSent[k], 10))
	}

	b.WriteString("\n# HELP io_peer_messages_received_total Peer-to-peer messages received from the other service\n")
	b.WriteString("# TYPE io_peer_messages_received_total counter\n")
	for _, k := range sortedKeys(m.peerRecv) {
		fmt.Fprintf(&b, "io_peer_messages_received_total{service=%q,message_type=%q} %s\n",
			serviceName, k, strconv.FormatUint(m.peerRecv[k], 10))
	}

	b.WriteString("\n# HELP io_app_info Static application metadata\n")
	b.WriteString("# TYPE io_app_info gauge\n")
	fmt.Fprintf(&b, "io_app_info{service=%q,app_version=%q} 1\n", serviceName, version)

	return b.String()
}

func sortedKeys(m map[string]uint64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func GenerateRequestID() string {
	return fmt.Sprintf("req-%d", time.Now().UnixNano())
}

func GenerateTraceID() string {
	return fmt.Sprintf("trace-%d", time.Now().UnixNano())
}

// RequestID returns the inbound X-Request-ID header, generating one if absent.
func RequestID(r *http.Request) string {
	if id := r.Header.Get("X-Request-ID"); id != "" {
		return id
	}
	return GenerateRequestID()
}

// TraceID returns the inbound X-B3-TraceId header, generating one if absent.
func TraceID(r *http.Request) string {
	if id := r.Header.Get("X-B3-TraceId"); id != "" {
		return id
	}
	return GenerateTraceID()
}
