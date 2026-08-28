// Package peer implements the service-to-service message exchange between
// the two servers in this repo. Each hop is an independent outbound HTTP
// request over the network (via PEER_URL, a real DNS name or k8s Service in
// non-local deployments) rather than an in-process function call, so a full
// exchange is two separate, asynchronous request/response round trips.
package peer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Message is the JSON body exchanged between services.
type Message struct {
	Type      string    `json:"type"`       // "pong" or "ping"
	From      string    `json:"from"`       // sending service's name
	RequestID string    `json:"request_id"` // correlates a pong with its ping reply
	SentAt    time.Time `json:"sent_at"`
}

// Exchange is one logged hop (either side of a Message), kept for inspection
// via a service's /peer/log endpoint.
type Exchange struct {
	Message
	Direction string    `json:"direction"` // "sent" or "received"
	At        time.Time `json:"at"`
}

// Log is a small thread-safe, bounded, most-recent-first record of exchanges.
type Log struct {
	mu      sync.Mutex
	entries []Exchange
	max     int
}

func NewLog(max int) *Log {
	return &Log{max: max}
}

func (l *Log) Add(e Exchange) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append([]Exchange{e}, l.entries...)
	if len(l.entries) > l.max {
		l.entries = l.entries[:l.max]
	}
}

func (l *Log) Recent() []Exchange {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Exchange, len(l.entries))
	copy(out, l.entries)
	return out
}

// Client sends Messages to the peer service over HTTP.
type Client struct {
	httpClient *http.Client
	peerURL    string
}

func NewClient(peerURL string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 5 * time.Second},
		peerURL:    peerURL,
	}
}

// Configured reports whether a peer URL was set; callers should fail fast
// with a clear error rather than attempting a request to an empty address.
func (c *Client) Configured() bool {
	return c.peerURL != ""
}

// Send POSTs msg as JSON to the peer's given path (e.g. "/peer/pong"),
// propagating the request/trace IDs so the exchange stays correlated across
// both hops in each service's logs.
func (c *Client) Send(ctx context.Context, path string, msg Message, requestID, traceID string) error {
	if !c.Configured() {
		return fmt.Errorf("peer: PEER_URL is not configured")
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("peer: encode message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.peerURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("peer: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", requestID)
	req.Header.Set("X-B3-TraceId", traceID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("peer: request to %s%s: %w", c.peerURL, path, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("peer: %s%s returned status %d", c.peerURL, path, resp.StatusCode)
	}
	return nil
}

// DecodeMessage reads a Message from an inbound request body.
func DecodeMessage(r *http.Request) (Message, error) {
	defer r.Body.Close()
	var msg Message
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		return Message{}, fmt.Errorf("peer: decode message: %w", err)
	}
	return msg, nil
}
