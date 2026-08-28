package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

const dashboardPort = "8090"

var (
	ingressURL = envOr("INGRESS_URL", "http://localhost:18081")
	ioURL      = envOr("IO_URL", "http://localhost:18086")
	pongURL    = envOr("PONG_URL", "http://localhost:18083")
)

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return strings.TrimRight(value, "/")
	}
	return fallback
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", statusHandler)
	mux.HandleFunc("/api/logs", logsHandler)
	mux.HandleFunc("/api/metrics", metricsHandler)
	mux.HandleFunc("/api/peer-log", peerLogHandler)
	mux.HandleFunc("/api/traffic", trafficHandler)
	mux.HandleFunc("/api/trigger", triggerHandler)
	mux.Handle("/", http.FileServer(http.Dir("dashboard")))

	server := &http.Server{Addr: ":" + dashboardPort, Handler: mux}
	log.Printf("dashboard available at http://localhost:%s", dashboardPort)
	log.Printf("ingress=%s io=%s pong=%s", ingressURL, ioURL, pongURL)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"deployments":     kubectlJSON("get", "deployments", "io-service-v1", "io-service-v2", "pong-service", "-o", "json"),
		"pods":            kubectlJSON("get", "pods", "-l", "app", "-o", "json"),
		"services":        kubectlJSON("get", "svc", "io-service", "pong-service", "-o", "json"),
		"virtualService":  kubectlJSON("get", "virtualservice", "io-virtualservice", "-o", "json"),
		"destinationRule": kubectlJSON("get", "destinationrule", "io-destination", "-o", "json"),
	})
}

func logsHandler(w http.ResponseWriter, r *http.Request) {
	selector := r.URL.Query().Get("selector")
	container := r.URL.Query().Get("container")
	if !allowedSelector(selector) || !allowedContainer(container) {
		http.Error(w, "invalid log selector", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"output": kubectlText("logs", "-l", selector, "-c", container, "--tail=100", "--prefix=true")})
}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	baseURL := pongURL
	if service == "io" {
		baseURL = ioURL
	}
	proxyGet(w, baseURL+"/metrics")
}

func peerLogHandler(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	baseURL := pongURL
	if service == "io" {
		baseURL = ioURL
	}
	proxyGet(w, baseURL+"/peer/log")
}

func trafficHandler(w http.ResponseWriter, r *http.Request) {
	proxyGet(w, ingressURL+"/ping")
}

func triggerHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, ioURL+"/peer/trigger", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func proxyGet(w http.ResponseWriter, target string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func kubectlJSON(args ...string) any {
	output := kubectlText(args...)
	var value any
	if json.Unmarshal([]byte(output), &value) == nil {
		return value
	}
	return map[string]string{"error": output}
}

func kubectlText(args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("kubectl %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func allowedSelector(value string) bool {
	return value == "app=io-service,version=v1" || value == "app=io-service,version=v2" || value == "app=pong-service"
}

func allowedContainer(value string) bool {
	return value == "io-service" || value == "pong-service"
}

func init() {
	if _, err := url.Parse(ingressURL); err != nil {
		log.Fatal(err)
	}
}
