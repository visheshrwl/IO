# Kubernetes and Istio Learning Guide

This document records the work done in this project, the commands used, the YAML files created, and the Kubernetes concepts behind them.

## 1. Project overview

This project is a simple Go HTTP service.

Files involved:
- [main.go](../cmd/io/main.go)
- [Dockerfile](../Dockerfile)
- [deployment-v1.yaml](../deploy/kubernetes/deployment-v1.yaml)
- [service.yaml](../deploy/kubernetes/service.yaml)
- [gateway.yaml](../deploy/istio/gateway.yaml)
- [virtual-service.yaml](../deploy/istio/virtual-service.yaml)
- [destination-rule.yaml](../deploy/istio/destination-rule.yaml)
- [shadow-traffic-20-percent.yaml](../deploy/istio/examples/shadow-traffic-20-percent.yaml)

The Go app listens on port 8080 and exposes `/ping`, which returns `Pong from v1!` or `Pong from v2!` based on the `APP_VERSION` environment variable.

It also exposes:
- `/health/live` for liveness
- `/health/ready` for readiness
- `/metrics` for Prometheus-style metrics

This makes it possible to validate each deployment version independently, not just through sidecar logs.

---

## 2. Application code

### [main.go](../cmd/io/main.go)

```go
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
```

What it does:
- reads the `PORT` environment variable
- defaults to `8080`
- reads `APP_VERSION`
- logs version, method, path, request ID, and trace ID
- exposes `/metrics`, `/health/live`, and `/health/ready`
- exposes `/ping`
- serves a version-aware response

This is the exact backend instrumentation we needed for a shadow deployment because Envoy logs alone do not tell us what the app itself did.

---

## 4.1 App-level observability and health checks

### Metrics

Use the `/metrics` endpoint to scrape application metrics:

```bash
curl http://localhost:8080/metrics
```

This returns Prometheus-style counters like:

```text
io_http_requests_total{app_version="v2",method="GET",path="/ping"} 3
io_app_info{app_version="v2"} 1
```

This makes it easy to determine which version handled traffic and how many requests each version served.

### Readiness and liveness

Kubernetes probes call these endpoints:

```yaml
readinessProbe:
  httpGet:
    path: /health/ready
    port: 8080

livenessProbe:
  httpGet:
    path: /health/live
    port: 8080
```

The readiness probe ensures the process is ready to serve traffic, while the liveness probe helps restart a broken pod.

### Request and trace correlation

The app logs request IDs and trace IDs, and it also sets response headers:

```text
X-App-Version: v2
X-Request-ID: req-123
X-Trace-ID: trace-456
```

This is valuable when you are debugging a shadow deployment because you can trace one user request across the app, the proxy, and the metrics output.

---

This is the kind of application that is perfect for a Kubernetes deployment.

---

## 3. Docker image setup

### [Dockerfile](../Dockerfile)

This Dockerfile does a multi-stage build:
- builds a Go binary in a builder container
- copies it into a small Alpine runtime image
- runs the app as a non-root user
- exposes port 8080

Important parts:
- `FROM golang:1.27.0-trixie AS builder` builds the binary
- `RUN CGO_ENABLED=0 GOOS=linux go build ...` compiles it
- `FROM alpine:3.20` creates a lightweight runtime
- `EXPOSE 8080` documents the port
- `CMD ["./server"]` launches the app

This is a standard pattern for Go containers.

---

## 4. Basic Kubernetes manifest: Deployment

### [deployment-v1.yaml](../deploy/kubernetes/deployment-v1.yaml)

Current version:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: io-service
spec:
  replicas: 2
  selector:
    matchLabels:
      app: io-service
  template:
    metadata:
      labels:
        app: io-service
    spec:
      containers:
        - name: io-service
          image: io:latest
          imagePullPolicy: IfNotPresent
          ports:
            - name: http
              containerPort: 8080
              protocol: TCP
          env:
            - name: PORT
              value: "8080"
```

### What each section means

#### `apiVersion: apps/v1`
This tells Kubernetes that this is a Deployment object in the apps API.

#### `kind: Deployment`
A Deployment is a controller that ensures the desired number of pod replicas exist.

#### `metadata.name: io-service`
This is the deployment name.

#### `spec.replicas: 2`
Kubernetes will keep 2 pods running.

#### `spec.selector.matchLabels`
This tells the Deployment which pods it manages.

#### `template`
This is the pod template. Every pod created by the Deployment is based on this template.

#### `metadata.labels`
Labels on the pod. This matches the selector above.

#### `spec.containers`
Defines the running container(s).

#### `image: io:latest`
The image used to create the pod.

#### `ports.containerPort: 8080`
The app listens on 8080 inside the container.

#### `env.PORT=8080`
The app uses this environment variable to know which port to bind to.

### Why the pod kept recreating

The Deployment controller was always trying to maintain 2 pods. So when a pod was deleted manually, Kubernetes recreated it.

That is the essence of a Deployment:
- desired state = 2 pods
- controller continuously reconciles the cluster to that state

---

## 5. Service manifest

### [service.yaml](../deploy/kubernetes/service.yaml)

Current version:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: io-service
spec:
  selector:
    app: io-service
  ports:
    - protocol: TCP
      port: 80
      targetPort: 8080
  type: ClusterIP
```

### What each section means

#### `kind: Service`
A Service gives pods a stable network identity and load balances traffic to them.

#### `selector: app=io-service`
This tells the Service which pods to send traffic to.

#### `ports.port: 80`
The Service listens on port 80.

#### `targetPort: 8080`
Traffic to port 80 on the Service is forwarded to pod port 8080.

#### `type: ClusterIP`
Internal cluster-only access. It is not publicly reachable from outside the cluster.

### Why this matters

The Service is how you connect to a pod group without knowing the pod IPs. The pods are dynamic, but the Service gives a stable network endpoint.

---

## 6. Istio manifests

### [gateway.yaml](../deploy/istio/gateway.yaml)

```yaml
apiVersion: networking.istio.io/v1beta1
kind: Gateway
metadata:
  name: io-gateway
spec:
  selector:
    istio: ingressgateway
  servers:
    - port:
        number: 80
        name: http
        protocol: HTTP
      hosts:
        - "*"
```

### What this does

A Gateway configures the ingress point into the Istio mesh.

- `selector.istio: ingressgateway` tells Istio to use the ingress gateway deployment
- port 80 is exposed for HTTP traffic
- `hosts: ["*"]` accepts all hosts

This is the public entry point for the mesh.

---

### [virtual-service.yaml](../deploy/istio/virtual-service.yaml)

```yaml
apiVersion: networking.istio.io/v1beta1
kind: VirtualService
metadata:
  name: io-virtualservice
spec:
  hosts:
    - "*"
  gateways:
    - io-gateway
  http:
    - route:
        - destination:
            host: io-service
            subset: v1
            port:
              number: 80
      mirror:
        host: io-service
        subset: v2
      mirrorPercentage:
        value: 100.0
```

### What this does

A VirtualService tells Istio how to route incoming traffic.

It says:
- accept all hosts from the `io-gateway`
- route the real HTTP request to the `v1` subset
- send a copy of the request to the `v2` subset
- return only the `v1` response to the client

This is how traffic enters the mesh and is forwarded to the application. The mirror request is asynchronous from the client's point of view: v2's response body, status code, and headers are discarded by Istio.

---

### [destination-rule.yaml](../deploy/istio/destination-rule.yaml)

```yaml
apiVersion: networking.istio.io/v1beta1
kind: DestinationRule
metadata:
  name: io-destination
spec:
  host: io-service
  subsets:
    - name: v1
      labels:
        app: io-service
        version: v1
    - name: v2
      labels:
        app: io-service
        version: v2
```

### What this does

A DestinationRule defines subsets of a service for routing policy.

This lets us treat:
- `v1` as the main version
- `v2` as the shadow or new version

In Istio, subsets are selected by labels.

---

### [shadow-traffic-20-percent.yaml](../deploy/istio/examples/shadow-traffic-20-percent.yaml)

This was created as an example shadow deployment setup.

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: io-service-v1
spec:
  replicas: 2
  selector:
    matchLabels:
      app: io-service
      version: v1
  template:
    metadata:
      labels:
        app: io-service
        version: v1
    spec:
      containers:
        - name: io-service
          image: io:latest
          ports:
            - containerPort: 8080
          env:
            - name: PORT
              value: "8080"
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: io-service-v2
spec:
  replicas: 1
  selector:
    matchLabels:
      app: io-service
      version: v2
  template:
    metadata:
      labels:
        app: io-service
        version: v2
    spec:
      containers:
        - name: io-service
          image: io:latest
          ports:
            - containerPort: 8080
          env:
            - name: PORT
              value: "8080"
---
apiVersion: v1
kind: Service
metadata:
  name: io-service
spec:
  selector:
    app: io-service
  ports:
    - port: 80
      targetPort: 8080
  type: ClusterIP
---
apiVersion: networking.istio.io/v1beta1
kind: VirtualService
metadata:
  name: io-virtualservice
spec:
  hosts:
    - "*"
  gateways:
    - io-gateway
  http:
    - route:
        - destination:
            host: io-service
            subset: v1
      mirror:
        host: io-service
        subset: v2
      mirrorPercentage:
        value: 20.0
```

### Shadow deployment explained

This does not replace the main version. It copies some traffic to the second version.

Routing logic:
- main traffic goes to `v1`
- a mirror of 20% of traffic goes to `v2`
- user response still comes from `v1`
- `v2` sees traffic but does not interfere with normal user experience

This is an important deployment strategy for testing a risky release safely.

---

## 7. Kubernetes concepts we learned

### Pod
A pod is the smallest deployable unit. It runs one or more containers.

### Deployment
A Deployment manages Pods and ensures the desired count is maintained.

### Service
A Service exposes Pods through a stable IP, DNS name, and load-balancing behavior.

### Endpoints
Endpoints are the actual pod IPs and ports behind a Service. `kubectl get endpoints` shows them.

### ClusterIP
Internal service type. Only reachable from inside the cluster.

### LoadBalancer
Publicly exposes a Service via cloud infrastructure, such as AWS ELB in EKS.

### NodePort
Exposes service on a port on each node.

### Ingress
HTTP/HTTPS traffic entry point; often used instead of public LoadBalancer for multiple services.

### Gateway (Istio)
Entry point into the Istio mesh.

### VirtualService (Istio)
Routes traffic based on host, path, and headers.

### DestinationRule (Istio)
Defines subset routing and policies.

### Mirror traffic / shadow deployment
Copies traffic to a second version without affecting normal response handling.

---

## 8. Commands we ran and what they meant

These are the commands captured from the workspace session and terminal history.

### Docker / environment

```bash
docker ps
```
Checks running Docker containers and verifies Docker is active.

Observed result:
- Docker Desktop / kind containers were running.

---

### Basic cluster service checks

```bash
kubectl get svc -o wide
```
Shows all Services with their cluster IPs, external IPs, ports, and selectors.

```bash
kubectl get pods
```
Shows all pod states.

```bash
kubectl describe services io-service
```
Shows Service metadata, selector, endpoints, type, and events.

```bash
kubectl get endpoints io-service
```
Shows the pod IPs behind the Service.

```bash
kubectl get endpoints io-service -o yaml
```
Shows full Endpoints object as YAML.

---

### Deployment creation and failure scenarios

```bash
kubectl create deployment io --image=io
```
Creates a Deployment named `io` with image `io`.

It failed because the deployment already existed.

```bash
kubectl create deployment iobackend --image=io
```
Creates a new deployment named `iobackend`.

Important: Kubernetes names must be valid lowercase RFC 1123 names.

```bash
kubectl create deployment ioBackend --image=io
```
Failed because of uppercase letters in the name.

---

### Pod deletion and controller behavior

```bash
kubectl delete pod io-6df66788c7-k56f4
```
Deletes one pod; because it is owned by a Deployment, the Deployment recreated it.

```bash
kubectl delete pods iobackend-777bd7f5fc-7wh76
```
Deletes a pod owned by a Deployment; a replacement is created automatically.

This is the key lesson:
- deleting a pod does not permanently remove a Deployment-managed workload
- the Deployment controller reconciles to the desired replicas count

---

### Service creation

```bash
kubectl create -f deploy/kubernetes/service.yaml
```
Creates the Service resource from YAML.

```bash
kubectl create deploy/kubernetes/service.yaml
```
This failed because `kubectl create` expects a resource type, not a filename directly in that form.

---

### Apply operations

```bash
kubectl apply -f deploy/kubernetes/deployment-v1.yaml
kubectl apply -f deploy/kubernetes/service.yaml
```
Applies the declarations in the YAML files to the cluster.

This is usually preferred over `create` when managing declarative YAML.

```bash
kubectl apply --dry-run=client -f deploy/istio/gateway.yaml
kubectl apply --dry-run=client -f deploy/istio/virtual-service.yaml
kubectl apply --dry-run=client -f deploy/istio/examples/shadow-traffic-20-percent.yaml
```
Validates YAML syntax and Kubernetes API compatibility without actually applying it.

This failed because the Istio CRDs were not installed in the cluster.

---

### Port forwarding

```bash
kubectl port-forward svc/io-service 8080:80
```
Creates a local tunnel from your machine to the Service.

This is useful for testing a cluster-local Service from your laptop without a public LoadBalancer or Ingress.

---

### YAML validation

```bash
python3 - <<'PY'
import yaml, sys
for path in ['deploy/kubernetes/deployment-v1.yaml','deploy/kubernetes/service.yaml']:
    with open(path, 'r') as f:
        yaml.safe_load(f)
    print(f'{path}: OK')
PY
```
Validates the YAML files parse correctly.

Observed result:
- deploy/kubernetes/deployment-v1.yaml: OK
- deploy/kubernetes/service.yaml: OK

This checks syntax, not Kubernetes semantics.

---

### Scaling a Deployment

```bash
kubectl scale deployment io-service --replicas=0
```
Stops all pods for the Deployment.

```bash
kubectl scale deployment io-service --replicas=2
```
Restores the desired replica count.

This is a controlled way to stop a deployment without deleting the Deployment object itself.

---

### Delete a Deployment

```bash
kubectl delete deployment io-service
```
Removes the Deployment and all pods it manages.

This is the permanent stop mechanism for a Deployment-driven workload.

---

### Delete the default cluster Service

```bash
kubectl delete svc kubernetes
```
This is the Kubernetes API service, not your app service. It is not recommended to delete it.

Why it was a bad idea:
- it is not your application service
- it is part of the cluster control plane plumbing
- it can confuse kube access flow if removed from the namespace unexpectedly

---

## 9. Lessons from debugging the service

### Problem: no endpoints for the Service

We saw that the Service selector and the Deployment labels must match exactly.

Example of the correct pattern:

Deployment:
```yaml
metadata:
  labels:
    app: io-service
```

Service:
```yaml
spec:
  selector:
    app: io-service
```

If they do not match, the Service has no endpoints, and no traffic reaches the pods.

### Why endpoints are important

`kubectl get endpoints` tells you the real backend addresses behind the Service.

If there are no endpoints:
- selector mismatch
- pod not ready
- pods not running
- service ports mismatch

---

## 10. What we learned about Docker vs Kubernetes

### Docker
- containers run on a host
- `-p 8080:8080` publishes a port on the host
- the app is directly exposed from the container host network

### Kubernetes
- pods live inside the cluster network
- services route traffic to pods
- external connectivity depends on Service type and cloud integration

### Why port-forward is different

`kubectl port-forward` is not the same as publishing a Docker port.

It is a local tunnel to the cluster:
- your laptop -> local port
- cluster Service -> pod

It is useful for debugging, not for a production public exposure model.

---

## 11. How to see logs and observability in Kubernetes and Istio

This is the part that usually causes confusion: if you do not see any logs, it does not always mean the app is broken. In many cases, the HTTP request is being logged by the Istio sidecar proxy rather than the application itself.

### 11.1 First check whether the pod is running

```bash
kubectl get pods -l app=io-service -o wide
```

This tells you:
- whether the pods are healthy
- whether they are ready
- which node they are running on
- whether the service is actually connected to the right pods

If the pod is not running, you will not see logs from it at all.

### 11.2 Check application logs

If the application itself writes logs, use the app container:

```bash
kubectl logs -l app=io-service -c io-service --tail=100
```

If the pod has multiple containers, this is the safest form because it targets the app container directly.

For all containers in the pod:

```bash
kubectl logs -l app=io-service --all-containers=true --tail=100
```

### 11.3 Check Envoy / Istio sidecar logs

Because Istio injects a sidecar proxy, the traffic is often logged there. The sidecar container is usually named `istio-proxy`.

```bash
kubectl logs -l app=io-service -c istio-proxy --tail=100
```

This is often the most useful command for measuring traffic behavior in an Istio setup. You will see request metadata such as:
- request path
- HTTP status code
- upstream destination
- source IP
- latency
- service name

### 11.4 Check the ingress gateway logs

The ingress gateway is the entry point into the mesh. This is where you can see traffic entering the cluster:

```bash
kubectl logs -n istio-system deploy/istio-ingressgateway --tail=100
```

This is especially helpful when testing a request through the Gateway.

### 11.5 Check the control plane logs

If routing does not behave as expected, inspect the control plane:

```bash
kubectl logs -n istio-system deploy/istiod --tail=100
```

This is useful when you have:
- config mistakes
- VirtualService issues
- Gateway mismatch
- stale/invalid routing configuration

### 11.6 Generate real traffic and then inspect logs

This is the exact sequence we used to prove the path works:

```bash
kubectl port-forward svc/istio-ingressgateway -n istio-system 8080:80
curl http://localhost:8080/ping
kubectl logs -l app=io-service --all-containers=true --tail=50
```

What you will usually see:
- request logs from the proxy sidecar
- the HTTP request hitting `/ping`
- a `200` status code
- upstream destination information

This is exactly how you validate that the gateway and service chain are functioning.

### 11.7 Why you may not see any logs

This is often the reason: your Go application is not logging anything.

The app code in [main.go](../cmd/io/main.go) currently does not log requests. It just returns a response from `http.HandleFunc("/ping")`.

That means:
- when you curl the app, the response is returned successfully
- but there may be no application log line at all
- the request will still appear in the Istio proxy logs

This is why you may see logs in the `istio-proxy` container and not in the app container.

### 11.8 Example output we observed in this project

When we made a real request through the gateway, the logs included entries like:

```text
[2026-08-27T06:22:16.295Z] "GET /ping HTTP/1.1" 200 - via_upstream - "-" 0 5 0 0 "10.244.2.4" "curl/8.5.0" ...
```

This is an Envoy/Istio access log showing:
- method: `GET`
- path: `/ping`
- status: `200`
- upstream service: `io-service`
- client: `curl`

This proves that the request passed through the mesh and the gateway route worked.

### 11.9 Observability checklist

Use this checklist whenever debugging a Kubernetes + Istio app:

1. `kubectl get pods` — confirm the pods are healthy
2. `kubectl get svc` — confirm routing targets exist
3. `kubectl logs -l app=io-service -c io-service` — app logs
4. `kubectl logs -l app=io-service -c istio-proxy` — Envoy/Istio logs
5. `kubectl logs -n istio-system deploy/istio-ingressgateway` — gateway logs
6. `kubectl logs -n istio-system deploy/istiod` — control plane logs
7. `curl http://localhost:8080/ping` — verify actual traffic

### 11.10 How to verify v1 versus v2 traffic

This is the key for shadow deployment validation.

You should filter by the `version` label:

```bash
kubectl logs -l app=io-service,version=v1 --all-containers=true --tail=50
kubectl logs -l app=io-service,version=v2 --all-containers=true --tail=50
```

This gives you separate logs for each version.

You can also check the pod list by version:

```bash
kubectl get pods -l app=io-service,version=v1 -o wide
kubectl get pods -l app=io-service,version=v2 -o wide
```

This proves which pod set belongs to which version.

### 11.11 What to look for in the logs

In an Istio shadow deployment, the important thing is to confirm both versions are receiving traffic, even if only the main version is skipping user impact.

For example, the logs show upstream destination IPs and request metadata. You can correlate:
- `10.244.2.5` with v1 pod IP
- `10.244.2.6` with v2 pod IP

That lets you tell which version handled the request.

A good verification pattern is:

```bash
kubectl port-forward svc/istio-ingressgateway -n istio-system 8080:80
curl http://localhost:8080/ping
kubectl logs -l app=io-service,version=v1 --all-containers=true --tail=50
kubectl logs -l app=io-service,version=v2 --all-containers=true --tail=50
```

This gives real evidence of the traffic split and helps you confirm the shadow behavior.

### 11.12 Why the version-specific logs are important

Without separating by version, you only see aggregate traffic. That does not tell you:
- which version is primary
- which version is shadowed
- whether v2 is receiving mirrored traffic
- whether v2 is behaving correctly behind the scenes

Version-specific logs are the proof that your shadow deployment is actually working.

---

## 12. Why Istio is useful

Istio becomes valuable when you have multiple services and want:
- traffic management
- retry logic
- canary and blue-green deployments
- metrics and traces
- security and policy enforcement

For a simple one-service app, Kubernetes alone is enough.

For a real-world system with multiple services, Istio helps with operational control.

---

## 12. Current status

At this point, we have:
- built and run a Go app
- containerized it with Docker
- deployed it to a local Kubernetes cluster
- created a simple Service
- validated YAML files
- debugged selector mismatch and endpoints
- created Istio Gateway and VirtualService manifests
- discussed shadow deployment strategy
- verified that Istio CRDs are not yet installed, so custom Istio resources cannot be applied until the control plane is installed

---

## 13. Recommended next steps

### If you want to continue with plain Kubernetes
1. Keep the Deployment and Service as they are
2. Learn how to expose a Service via `LoadBalancer`
3. Practice `kubectl get svc`, `kubectl get endpoints`, `kubectl describe`
4. Understand selector/label matching

### If you want to continue with Istio
1. Install Istio control plane and ingress gateway
2. Apply the Gateway and VirtualService manifests
3. Run `kubectl get pods -n istio-system`
4. Create v1 and v2 versions
5. Use `mirrorPercentage` for shadow traffic
6. Observe logs and metrics

---

## 14. Final learning summary

This project taught the following main Kubernetes principles:

1. A Deployment ensures pods stay in the desired state.
2. A Service gives pods a stable network endpoint.
3. Labels and selectors must match exactly.
4. Endpoints show whether the service is routing to real pods.
5. Deleting a pod is not permanent if it is managed by a Deployment.
6. A LoadBalancer makes the Service externally reachable in a cloud environment.
7. Port-forward is only for local debugging, not production exposure.
8. Istio adds routing, observability, and traffic control on top of Kubernetes.
9. Shadow deployment is a safe testing mechanism for new versions.

---

## 15. Reference files in this workspace

- [main.go](../cmd/io/main.go)
- [Dockerfile](../Dockerfile)
- [deployment-v1.yaml](../deploy/kubernetes/deployment-v1.yaml)
- [service.yaml](../deploy/kubernetes/service.yaml)
- [gateway.yaml](../deploy/istio/gateway.yaml)
- [virtual-service.yaml](../deploy/istio/virtual-service.yaml)
- [destination-rule.yaml](../deploy/istio/destination-rule.yaml)
- [shadow-traffic-20-percent.yaml](../deploy/istio/examples/shadow-traffic-20-percent.yaml)

This guide is intended as a full learning record for Kubernetes and Istio foundations.
