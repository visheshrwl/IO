package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoaderString(t *testing.T) {
	t.Setenv("PRESENT", "value")
	l := &Loader{}

	if got := l.String("PRESENT", "fallback"); got != "value" {
		t.Errorf("String(PRESENT) = %q, want %q", got, "value")
	}
	if got := l.String("ABSENT", "fallback"); got != "fallback" {
		t.Errorf("String(ABSENT) = %q, want %q", got, "fallback")
	}
	if err := l.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}

func TestLoaderRequire(t *testing.T) {
	t.Setenv("SET", "x")
	l := &Loader{}
	l.Require("SET")
	l.Require("MISSING_ONE")
	l.Require("MISSING_TWO")

	err := l.Err()
	if err == nil {
		t.Fatal("Err() = nil, want error for two missing vars")
	}
	for _, want := range []string{"MISSING_ONE is required", "MISSING_TWO is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Err() = %q, missing %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "SET is required") {
		t.Errorf("Err() wrongly reports SET as missing: %q", err.Error())
	}
}

func TestLoaderDuration(t *testing.T) {
	t.Setenv("GOOD", "1500ms")
	t.Setenv("BAD", "nonsense")
	l := &Loader{}

	if got := l.Duration("GOOD", time.Second); got != 1500*time.Millisecond {
		t.Errorf("Duration(GOOD) = %v, want 1.5s", got)
	}
	if got := l.Duration("ABSENT", 2*time.Second); got != 2*time.Second {
		t.Errorf("Duration(ABSENT) = %v, want 2s", got)
	}
	if got := l.Duration("BAD", 3*time.Second); got != 3*time.Second {
		t.Errorf("Duration(BAD) = %v, want fallback 3s", got)
	}
	if err := l.Err(); err == nil || !strings.Contains(err.Error(), "BAD: invalid duration") {
		t.Errorf("Err() = %v, want BAD invalid duration", err)
	}
}

func TestLoaderInt(t *testing.T) {
	t.Setenv("N", "42")
	t.Setenv("NOPE", "4x")
	l := &Loader{}

	if got := l.Int("N", 1); got != 42 {
		t.Errorf("Int(N) = %d, want 42", got)
	}
	if got := l.Int("NOPE", 7); got != 7 {
		t.Errorf("Int(NOPE) = %d, want fallback 7", got)
	}
	if err := l.Err(); err == nil || !strings.Contains(err.Error(), "NOPE: invalid integer") {
		t.Errorf("Err() = %v, want NOPE invalid integer", err)
	}
}

func TestLoadBaseDefaults(t *testing.T) {
	l := &Loader{}
	b := LoadBase(l, "svc")

	if b.ServiceName != "svc" || b.Version != "unknown" || b.Port != "8080" || b.LogLevel != "info" {
		t.Errorf("LoadBase defaults = %+v", b)
	}
	if b.DrainDelay == 0 {
		t.Error("LoadBase DrainDelay = 0, want non-zero default")
	}
	if err := l.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}
