// Package config loads service configuration from the environment and
// validates it at startup. A misconfigured deployment should fail
// immediately with the complete list of problems, not crash later on the
// first request that touches an unset variable — so the Loader accumulates
// errors instead of returning on the first one.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/visheshrwl/io/internal/platform/health"
)

// Loader reads environment variables, collecting validation errors.
type Loader struct {
	errs []string
}

// String returns the value of key, or def when it is unset or empty.
func (l *Loader) String(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// Require returns the value of key and records an error when it is unset.
func (l *Loader) Require(key string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		l.errs = append(l.errs, fmt.Sprintf("%s is required", key))
	}
	return v
}

// Duration parses key as a Go duration (e.g. "5s"), falling back to def when
// unset and recording an error when set but unparseable.
func (l *Loader) Duration(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		l.errs = append(l.errs, fmt.Sprintf("%s: invalid duration %q", key, raw))
		return def
	}
	return d
}

// Int parses key as an integer, falling back to def when unset and
// recording an error when set but unparseable.
func (l *Loader) Int(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		l.errs = append(l.errs, fmt.Sprintf("%s: invalid integer %q", key, raw))
		return def
	}
	return n
}

// Err returns a single error describing every validation problem found, or
// nil when the configuration is valid.
func (l *Loader) Err() error {
	if len(l.errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(l.errs, "\n  - "))
}

// Base is the configuration every service shares.
type Base struct {
	ServiceName string
	Version     string
	Port        string
	LogLevel    string
	DrainDelay  time.Duration
}

// LoadBase reads the common variables: APP_VERSION, PORT, LOG_LEVEL, and
// SHUTDOWN_DRAIN_DELAY.
func LoadBase(l *Loader, serviceName string) Base {
	return Base{
		ServiceName: serviceName,
		Version:     l.String("APP_VERSION", "unknown"),
		Port:        l.String("PORT", "8080"),
		LogLevel:    l.String("LOG_LEVEL", "info"),
		DrainDelay:  l.Duration("SHUTDOWN_DRAIN_DELAY", health.DefaultDrainDelay),
	}
}
