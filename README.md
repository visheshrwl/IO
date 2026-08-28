# IO

A minimal Go HTTP service used as a hands-on sandbox for Kubernetes and Istio traffic-management patterns — shadow deployments (traffic mirroring), health probes, and Prometheus-style metrics.

The app itself is intentionally simple: it just needs to be identifiable per-version (`v1`/`v2`) so that Kubernetes and Istio behavior (rollouts, mirroring, routing) can be observed from the outside.

## Endpoints

| Path            | Purpose                                                        |
|-----------------|-----------------------------------------------------------------|
| `GET /ping`     | Returns `Pong from <APP_VERSION>!`, echoes/generates request & trace IDs |
| `GET /health/live`  | Liveness probe — always `200 ok`                            |
| `GET /health/ready` | Readiness probe — `503` for the first 2s after start, then `200` |
| `GET /metrics`  | Prometheus-format counters (`io_http_requests_total`, `io_app_info`) |

Every `/ping` response includes `X-App-Version`, `X-Request-ID`, and `X-Trace-ID` headers, and is logged server-side with the same fields — useful for confirming which version actually served a request when traffic is being split or mirrored.

## Getting started

### Run locally

```bash
go run ./cmd/io
# PORT defaults to 8080, APP_VERSION defaults to "unknown"
curl localhost:8080/ping
```

### Build and run with Docker

```bash
docker build -t io:latest .
docker run -p 8080:8080 -e APP_VERSION=v1 io:latest
```

The image is a multi-stage build: compiled with `golang:1.27.0-trixie`, then run from a minimal `alpine:3.20` image as a non-root user.

## Kubernetes deployment

Manifests are split by concern so each can be applied/edited independently:

| File | Resource |
|---|---|
| [`deploy/kubernetes/deployment-v1.yaml`](deploy/kubernetes/deployment-v1.yaml) | `io-service-v1` Deployment (2 replicas) |
| [`deploy/kubernetes/deployment-v2.yaml`](deploy/kubernetes/deployment-v2.yaml) | `io-service-v2` Deployment (1 replica) |
| [`deploy/kubernetes/service.yaml`](deploy/kubernetes/service.yaml) | `io-service` ClusterIP Service, selects both versions |
| [`deploy/istio/gateway.yaml`](deploy/istio/gateway.yaml) | Istio `Gateway` accepting traffic on port 80 |
| [`deploy/istio/virtual-service.yaml`](deploy/istio/virtual-service.yaml) | `VirtualService` — routes to `v1`, mirrors 100% to `v2` |
| [`deploy/istio/destination-rule.yaml`](deploy/istio/destination-rule.yaml) | `DestinationRule` defining the `v1`/`v2` subsets |
| [`deploy/istio/examples/shadow-traffic-20-percent.yaml`](deploy/istio/examples/shadow-traffic-20-percent.yaml) | All-in-one variant with 20% mirrored traffic |
| [`deploy/istio/telemetry.yaml`](deploy/istio/telemetry.yaml) | Mesh-wide access logging + Zipkin tracing |

Apply the core resources:

```bash
kubectl apply -f deploy/kubernetes/deployment-v1.yaml
kubectl apply -f deploy/kubernetes/deployment-v2.yaml
kubectl apply -f deploy/kubernetes/service.yaml
```

Then, in an Istio-enabled mesh:

```bash
kubectl apply -f deploy/istio/gateway.yaml
kubectl apply -f deploy/istio/destination-rule.yaml
kubectl apply -f deploy/istio/virtual-service.yaml
kubectl apply -f deploy/istio/telemetry.yaml
```

Both Deployments expose the readiness/liveness probes above, so `kubectl rollout status` reflects actual application health rather than just container start.

## Shadow deployment (traffic mirroring)

[`deploy/istio/virtual-service.yaml`](deploy/istio/virtual-service.yaml) sends **all** live traffic to the `v1` subset while mirroring a copy of that same traffic to `v2` (`mirrorPercentage: 100.0`) — the caller only ever sees `v1`'s response, but `v2` receives real, unweighted traffic shadow-copies so it can be validated before it's ever allowed to serve a live request. This is distinct from canary/weighted routing (`v1`/`v2` never split live traffic here). [`deploy/istio/examples/shadow-traffic-20-percent.yaml`](deploy/istio/examples/shadow-traffic-20-percent.yaml) is a self-contained variant of the same idea at 20% mirroring instead of 100%. See [`docs/kubernetes-learning-guide.md`](docs/kubernetes-learning-guide.md) for the full write-up of how each manifest was built and why.

## Releases & container images

Tagged releases (`vX.Y.Z`) are built and published automatically by [`.github/workflows/release.yml`](.github/workflows/release.yml):

- The Docker image is built and pushed to the **GitHub Container Registry** at `ghcr.io/visheshrwl/io`, tagged with both the version and `latest`.
- A **GitHub Release** is created with auto-generated notes from the commits since the previous tag.

To cut a release:

```bash
git tag v0.1.0
git push origin v0.1.0
```

Then point any Deployment's `image:` field at `ghcr.io/visheshrwl/io:0.1.0` instead of a locally-built tag.

## Project structure

```
.
├── cmd/
│   └── io/
│       └── main.go                       # HTTP server: /ping, /health/*, /metrics
├── deploy/
│   ├── kubernetes/
│   │   ├── deployment-v1.yaml            # io-service-v1 Deployment
│   │   ├── deployment-v2.yaml            # io-service-v2 Deployment
│   │   └── service.yaml                  # ClusterIP Service
│   └── istio/
│       ├── gateway.yaml                  # Istio Gateway
│       ├── virtual-service.yaml          # Istio VirtualService (routing + mirroring)
│       ├── destination-rule.yaml         # Istio DestinationRule (v1/v2 subsets)
│       ├── telemetry.yaml                # Mesh-wide access logs + tracing
│       └── examples/
│           └── shadow-traffic-20-percent.yaml  # Combined manifest, 20% mirror variant
├── docs/
│   └── kubernetes-learning-guide.md      # Detailed notes on the concepts behind each file
├── .github/
│   └── workflows/
│       └── release.yml                   # Tag-triggered image build + GitHub Release
├── Dockerfile                            # Multi-stage build -> minimal Alpine runtime
└── go.mod
```

## Learn more

[`docs/kubernetes-learning-guide.md`](docs/kubernetes-learning-guide.md) is a running log of the commands and Kubernetes/Istio concepts used to build this project — useful if you're following along rather than just deploying it.
