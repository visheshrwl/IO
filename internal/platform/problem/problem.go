// Package problem writes HTTP error responses in the application/problem+json
// format (RFC 9457). A machine-readable error body lets a client tell a
// validation error from a downstream outage without scraping a plain-text
// string, and gives every service on the north-south edge one error shape.
package problem

import (
	"encoding/json"
	"net/http"
)

// Detail is the RFC 9457 problem object. Type defaults to "about:blank",
// for which Title is the standard status phrase.
type Detail struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// Write sends status with a problem+json body. detail is the
// human-readable explanation specific to this occurrence.
func Write(w http.ResponseWriter, r *http.Request, status int, detail string) {
	writeDetail(w, Detail{
		Type:     "about:blank",
		Title:    http.StatusText(status),
		Status:   status,
		Detail:   detail,
		Instance: r.URL.Path,
	})
}

// WriteDetail sends a fully specified problem, for cases that need a custom
// Type URI or Title.
func WriteDetail(w http.ResponseWriter, p Detail) {
	if p.Type == "" {
		p.Type = "about:blank"
	}
	if p.Title == "" {
		p.Title = http.StatusText(p.Status)
	}
	writeDetail(w, p)
}

func writeDetail(w http.ResponseWriter, p Detail) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}
