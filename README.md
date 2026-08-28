# IO

A pair of minimal Go HTTP services used as a hands-on sandbox for Kubernetes and Istio traffic-management patterns — shadow deployments (traffic mirroring), health probes, service-to-service messaging, and Prometheus-style metrics.

- **`io`** ([`cmd/io`](cmd/io)) is identifiable per-version (`v1`/`v2`) so that Kubernetes and Istio behavior (rollouts, mirroring, routing) can be observed from the outside.
- **`pong-service`** ([`cmd/pong-service`](cmd/pong-service)) is a second, independently deployed service that `io` talks to over the network, to exercise real inter-service traffic in the same cluster.

Both are separate binaries, separate container images, and separate Kubernetes Deployments/Services — they only ever talk to each other over HTTP via `PEER_URL`, never through an in-process call, so the exchange between them reflects genuine distributed communication (DNS-based service discovery in-cluster, or two independent local processes when run outside one).

## Endpoints

### `io`

| Path              | Purpose                                                        |
|-------------------|-----------------------------------------------------------------|
| `GET /ping`       | Returns `Pong from <APP_VERSION>!`, echoes/generates request & trace IDs |
| `POST /peer/trigger` | Sends a `pong` message to `pong-service` over HTTP; returns `202` once dispatched (the reply arrives asynchronously) |
| `POST /peer/ping` | Receives `pong-service`'s `ping` reply — this is `pong-service` calling *back into* `io`, not `io`'s own response |
| `GET /peer/log`   | Last 20 sent/received peer messages, most recent first |
| `GET /health/live`  | Liveness probe — always `200 ok`                            |
| `GET /health/ready` | Readiness probe — `503` for the first 2s after start, then `200` |
| `GET /metrics`  | Prometheus-format counters (`io_http_requests_total`, `io_peer_messages_*_total`, `io_app_info`) |

### `pong-service`

| Path              | Purpose                                                        |
|-------------------|-----------------------------------------------------------------|
| `POST /peer/pong` | Receives `io`'s `pong` message, acks it, then asynchronously calls `io`'s `/peer/ping` with a `ping` reply |
| `GET /peer/log`   | Last 20 sent/received peer messages, most recent first |
| `GET /health/live`, `GET /health/ready` | Same probes as `io` |
| `GET /metrics`    | Same metric families as `io`, labeled `service="pong-service"` |

Every `/ping` response includes `X-App-Version`, `X-Request-ID`, and `X-Trace-ID` headers, and is logged server-side with the same fields — useful for confirming which version actually served a request when traffic is being split or mirrored.

## Distributed ping-pong exchange

`POST /peer/trigger` on `io` kicks off a two-hop, asynchronous exchange between the two services:

1. `io` sends a `pong` message to `pong-service` at `POST /peer/pong` — a real outbound HTTP request over `PEER_URL`. `io`'s handler returns `202 Accepted` immediately; it does not wait for a reply.
2. `pong-service` receives it, acknowledges it, and — in a separate goroutine, after its own response is already sent — makes its **own independent outbound HTTP request** back to `io` at `POST /peer/ping`, carrying a `ping` message.
3. `io`'s `/peer/ping` handler receives that callback and logs the round trip as complete.

Both hops carry the same `request_id` (to correlate the exchange) and `trace_id` (propagated end to end), visible in both services' logs and via `GET /peer/log` on each. Because each hop is its own HTTP request/response pair — not a single call whose response is reused — this is genuine service-to-service messaging rather than an in-process function call.

```bash
curl -X POST localhost:8080/peer/trigger
curl localhost:8080/peer/log      # shows: sent pong, received ping
curl localhost:8081/peer/log      # shows: received pong, sent ping
```

## Getting started

### Run locally

Each service needs `PEER_URL` pointing at the other. Run them on two ports in separate terminals:

```bash
# terminal 1
PORT=8080 PEER_URL=http://localhost:8081 go run ./cmd/io

# terminal 2
PORT=8081 PEER_URL=http://localhost:8080 go run ./cmd/pong-service

curl localhost:8080/ping
curl -X POST localhost:8080/peer/trigger
```

`APP_VERSION` defaults to `"unknown"` if unset; `PEER_URL` must be set for the peer endpoints to work — `/peer/trigger` returns `500` with a clear error otherwise.

### Run with Docker Compose

The simplest way to see two real, separately-containered services talk to each other:

```bash
docker compose up --build
curl -X POST localhost:8080/peer/trigger
curl localhost:8080/peer/log
curl localhost:8081/peer/log
docker compose logs -f      # watch both containers log each hop of the exchange
```

### Build and run a single image with Docker

Both services share one [`Dockerfile`](Dockerfile), selected via the `SERVICE` build arg:

```bash
docker build --build-arg SERVICE=io -t io:latest .
docker run -p 8080:8080 -e APP_VERSION=v1 -e PEER_URL=http://host.docker.internal:8081 io:latest

docker build --build-arg SERVICE=pong-service -t pong-service:latest .
docker run -p 8081:8080 -e PEER_URL=http://host.docker.internal:8080 pong-service:latest
```

The image is a multi-stage build: compiled with `golang:1.27.0-trixie`, then run from a minimal `alpine:3.20` image as a non-root user.

## Kubernetes deployment

Manifests are split by concern so each can be applied/edited independently:

| File | Resource |
|---|---|
| [`deploy/kubernetes/deployment-v1.yaml`](deploy/kubernetes/deployment-v1.yaml) | `io-service-v1` Deployment (2 replicas), `PEER_URL=http://pong-service` |
| [`deploy/kubernetes/deployment-v2.yaml`](deploy/kubernetes/deployment-v2.yaml) | `io-service-v2` Deployment (1 replica), `PEER_URL=http://pong-service` |
| [`deploy/kubernetes/service.yaml`](deploy/kubernetes/service.yaml) | `io-service` ClusterIP Service, selects both versions |
| [`deploy/kubernetes/pong-service-deployment.yaml`](deploy/kubernetes/pong-service-deployment.yaml) | `pong-service` Deployment (1 replica), `PEER_URL=http://io-service` |
| [`deploy/kubernetes/pong-service-service.yaml`](deploy/kubernetes/pong-service-service.yaml) | `pong-service` ClusterIP Service |
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
kubectl apply -f deploy/kubernetes/pong-service-deployment.yaml
kubectl apply -f deploy/kubernetes/pong-service-service.yaml
```

`io`'s pods resolve `pong-service` and `pong-service`'s pod resolves `io-service` purely through in-cluster DNS (`PEER_URL`) — the same mechanism either Service would use for any other client, so `/peer/trigger` exercises real cluster networking between two independently scaled, independently healthy Deployments:

```bash
kubectl port-forward svc/io-service 8080:80
curl -X POST localhost:8080/peer/trigger
```

Then, in an Istio-enabled mesh:

```bash
kubectl apply -f deploy/istio/gateway.yaml
kubectl apply -f deploy/istio/destination-rule.yaml
kubectl apply -f deploy/istio/virtual-service.yaml
kubectl apply -f deploy/istio/telemetry.yaml
```

All three Deployments expose the readiness/liveness probes above, so `kubectl rollout status` reflects actual application health rather than just container start.

## Shadow deployment (traffic mirroring)

[`deploy/istio/virtual-service.yaml`](deploy/istio/virtual-service.yaml) sends **all** live traffic to the `v1` subset while mirroring a copy of that same traffic to `v2` (`mirrorPercentage: 100.0`) — the caller only ever sees `v1`'s response, but `v2` receives real, unweighted traffic shadow-copies so it can be validated before it's ever allowed to serve a live request. This is distinct from canary/weighted routing (`v1`/`v2` never split live traffic here). [`deploy/istio/examples/shadow-traffic-20-percent.yaml`](deploy/istio/examples/shadow-traffic-20-percent.yaml) is a self-contained variant of the same idea at 20% mirroring instead of 100%. See [`docs/kubernetes-learning-guide.md`](docs/kubernetes-learning-guide.md) for the full write-up of how each manifest was built and why.

## Releases & container images

Tagged releases (`vX.Y.Z`) are built and published automatically by [`.github/workflows/release.yml`](.github/workflows/release.yml):

- Both images are built (one per service, via the `SERVICE` build arg) and pushed to the **GitHub Container Registry** as `ghcr.io/visheshrwl/io` and `ghcr.io/visheshrwl/pong-service`, each tagged with both the version and `latest`.
- A **GitHub Release** is created with auto-generated notes from the commits since the previous tag, once both images are pushed.

To cut a release:

```bash
git tag v0.1.0
git push origin v0.1.0
```

Then point each Deployment's `image:` field at its corresponding `ghcr.io/visheshrwl/<service>:0.1.0` instead of a locally-built tag.

## Project structure

```
.
├── cmd/
│   ├── io/
│   │   └── main.go                       # io: /ping, /peer/*, /health/*, /metrics
│   └── pong-service/
│       └── main.go                       # pong-service: /peer/*, /health/*, /metrics
├── internal/
│   ├── service/
│   │   └── service.go                    # shared metrics store, health handlers, request/trace IDs
│   └── peer/
│       └── peer.go                       # peer Message/Client/Log used by both services
├── deploy/
│   ├── kubernetes/
│   │   ├── deployment-v1.yaml            # io-service-v1 Deployment
│   │   ├── deployment-v2.yaml            # io-service-v2 Deployment
│   │   ├── service.yaml                  # io-service ClusterIP Service
│   │   ├── pong-service-deployment.yaml  # pong-service Deployment
│   │   └── pong-service-service.yaml     # pong-service ClusterIP Service
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
│       └── release.yml                   # Tag-triggered matrix image build + GitHub Release
├── Dockerfile                            # Multi-stage build -> minimal Alpine runtime, shared by both services
├── docker-compose.yml                    # Runs both services as separate containers locally
└── go.mod
```

## Learn more

[`docs/kubernetes-learning-guide.md`](docs/kubernetes-learning-guide.md) is a running log of the commands and Kubernetes/Istio concepts used to build this project — useful if you're following along rather than just deploying it.
