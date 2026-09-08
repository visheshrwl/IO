package problem

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notifications", nil)

	Write(rec, r, http.StatusBadRequest, "recipient is required")

	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}

	var p Detail
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if p.Status != 400 || p.Title != "Bad Request" || p.Type != "about:blank" {
		t.Errorf("problem = %+v", p)
	}
	if p.Detail != "recipient is required" {
		t.Errorf("Detail = %q", p.Detail)
	}
	if p.Instance != "/notifications" {
		t.Errorf("Instance = %q, want /notifications", p.Instance)
	}
}

func TestWriteDetailFillsDefaults(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteDetail(rec, Detail{Status: http.StatusNotFound})

	var p Detail
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if p.Type != "about:blank" || p.Title != "Not Found" {
		t.Errorf("defaults not filled: %+v", p)
	}
}
