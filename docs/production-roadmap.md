# Production Roadmap — turning IO into a real microservice system

This repository is meant to be read as a course. Each commit introduces **one**
microservices concept, with a message that explains the *why*, and — where the
concept is non-obvious — a short note under [`docs/concepts/`](concepts/) and an
architecture decision record under [`docs/adr/`](adr/).

The reference frame is Sam Newman, *Building Microservices* (2nd ed.). The goal
is a faithful implementation of the ideas, small enough to read in an afternoon.
People learning from it is the by-product, not the target.

---

## Target architecture

Three services, three bounded contexts, no shared database, **no REST between
services**.

| Service | Bounded context | Owns | North–south (customers) | East–west (services) |
|---|---|---|---|---|
| **`io`** | Accounts & API access | customers, API keys, quotas (Postgres) | REST/JSON, versioned (`/v1`), OpenAPI, API-key auth, edge rate limiting | gRPC **client** of `pong-service` |
| **`pong-service`** | Links & click tracking | short links, redirects, click events (Postgres) | REST redirect endpoint `GET /{code}` → `302` for end users | gRPC **server** (`CreateLink`, `GetLinkStats`, `ListLinks`); Kafka **producer** (`LinkCreated`, `LinkClicked`) via transactional outbox |
| **`notification-service`** | Owner notifications | notifications (Postgres) | REST read API for a customer's notifications | Kafka **consumer** of `LinkClicked`; Kafka **producer** of `NotificationCreated` |

Collaboration rules (see [ADR-0002](adr/0002-north-south-rest-east-west-grpc-events.md)):

* **Synchronous, consistency-sensitive** calls between services → **gRPC** with
  deadlines, retries, and a circuit breaker. Example: creating a link from the
  edge — the customer waits for the result.
* **Everything else** → **events over Kafka**, choreographed, with the
  **transactional outbox** on the producer and an **inbox / dedupe** on the
  consumer. Example: a click updates counts and eventually notifies the owner.
* REST is for customers and end users only. A service never calls another
  service's REST endpoint.

### Why keep the `io` / `pong-service` names

They stop being "a transport demo" and become real services with data they own.
`io` is the customer-facing edge/account service; `pong-service` is the
link/redirect domain service. The old in-repo `internal/peer` HTTP callback
package is removed — inter-service communication is gRPC + events.

---

## Milestones

Milestone 1 is domain-agnostic and starts now. Milestone 3 (the domain reframe)
needs your sign-off before it begins.

### M1 — Runtime hardening foundation `done`

Everything later builds on this. No domain changes. Shipped:

1. `internal/platform/logging` — `log/slog` JSON on stderr, tagged with
   service and version; every `log.Printf` / `log.Fatal` replaced.
2. `internal/platform/httpserver` — explicit `*http.Server` with
   read/read-header/write/idle timeouts; a per-service `*http.ServeMux`;
   `signal.NotifyContext` + `server.Shutdown` with a drain deadline.
3. `internal/platform/health` — a `Probe` that starts not-ready, splits
   liveness from readiness, runs dependency checks, and on `SIGTERM` reports
   **draining** and pauses so Kubernetes de-registers the pod before
   connections close. The fixed 2-second warmup timer is gone.
4. `internal/platform/config` — one typed, validated struct per service;
   the `Loader` accumulates every error and fails fast at boot.
   notification-service now *requires* `DATABASE_URL` / `REDIS_URL`.
5. `internal/platform/middleware` — composable `Chain` of `RequestID`
   (UUID correlation IDs on context + request + response headers),
   `Recover` (panic → logged 500), `AccessLog` (one structured line +
   request-scoped logger).
6. `internal/platform/problem` — RFC 9457 `application/problem+json` on the
   notification-service edge.
7. Table-driven `-race` tests for every platform package.
8. `Makefile` (`build`/`test`/`lint`/`vet`/`fmt-check`/`tidy`/`vulncheck`/
   `run-*`/`compose-*`) and a PR CI workflow (gofmt, vet, build, race tests,
   staticcheck, govulncheck, `go mod tidy` gate, docker build per service).
9. Dockerfile hardened onto `distroless/static:nonroot` with cache mounts,
   `-trimpath`, `go.sum`, and OCI labels.

Deferred from M1: extracting io/pong handlers out of `main` for testing
(they are replaced wholesale in M3/M4); base-image digest pinning (M8);
`-ldflags -X` version stamping.

### M2 — Explicit contracts at the edge

OpenAPI 3.1 spec per REST surface, served and contract-tested; `/v1` path
versioning; schema-driven request validation; consistent pagination and error
model; backward-compatibility policy (expand/contract).

### M3 — Domain reframe `needs sign-off`

Repurpose `io` (accounts) and `pong-service` (links); each gets its own schema
and **versioned SQL migrations** (`golang-migrate`); remove `internal/peer`;
introduce domain models, aggregates, and repository interfaces; per-service DB
users and credentials.

### M4 — Synchronous inter-service calls: gRPC

`api/proto/**` contracts managed with **buf** (lint + breaking-change detection +
codegen); gRPC server in `pong-service`, client in `io`; interceptors for
correlation propagation, logging, metrics, panic recovery, deadlines; gRPC
health checking protocol; resilience — per-call deadlines, retry with backoff,
circuit breaker (`sony/gobreaker`); mTLS delegated to the mesh
([ADR-0004](adr/0004-transport-security.md)).

### M5 — Event-driven collaboration: Kafka

Redpanda locally, Kafka in-cluster; event schemas in `api/events/**` with a
schema registry; **transactional outbox** on every producer (fixes the
`notification-service` dual-write — see
[concepts/dual-write-and-outbox.md](concepts/dual-write-and-outbox.md));
idempotent consumers with an inbox table; consumer groups, at-least-once, retry
topic + DLQ; partition key = link code for per-link ordering;
`notification-service` becomes a real consumer and the Redis pub/sub path is
removed.

### M6 — Observability end to end

OpenTelemetry SDK — traces and metrics over OTLP; instrument HTTP, gRPC, Kafka
produce/consume, and DB; W3C `traceparent` propagation across gRPC and Kafka
headers (replacing the ad-hoc B3 forwarding); RED metrics; `trace_id` in every
log line; local Prometheus + Tempo + Grafana compose profile; an SLO document
with example alert rules.

### M7 — Resilience & progressive delivery

Timeout budgets down the call chain; edge rate limiting; bulkheads (bounded
pools); load shedding via readiness; the Istio shadow setup documented as
Newman's **parallel run**; weighted canary + rollback note; fault-injection
toggle with a test.

### M8 — Platform & supply chain

`securityContext` (runAsNonRoot, readOnlyRootFilesystem, drop `ALL` caps,
seccomp), resource requests/limits, PDB, HPA, topology spread; **default-deny
`NetworkPolicy`** with explicit allows (enforces "no surprise east–west");
ServiceAccount per service; Kustomize base + dev/prod overlays; secrets via
sealed-secrets / external-secrets; SBOM (syft), image signing (cosign), SLSA
provenance in the release workflow; Renovate.

### M9 — Testing strategy, made explicit

The test pyramid written down; consumer-driven contract tests (Pact) for gRPC
and events; integration tests with `testcontainers-go` (Postgres, Kafka); one
end-to-end smoke in CI via compose.

### M10 — Docs as the throughline

`docs/adr/` (one per decision), `docs/concepts/` (one per Newman idea mapped to
its commits), a C4 context + container diagram, a README rewrite, and an ordered
"read this repo as a course" index.

---

## Conventions

* **One concept per commit.** Conventional Commit prefix; body explains the
  trade-off, not just the change.
* **ADRs** for anything a reader might question: numbered, immutable once
  accepted, superseded rather than edited.
* **`internal/platform/`** holds infrastructure only — logging, config, HTTP
  server, middleware, telemetry. No domain logic ever. It is a shared library
  with the coupling that implies, accepted deliberately
  ([ADR-0003](adr/0003-shared-platform-package.md)).
* Every service: `/health/live`, `/health/ready`, `/metrics`, graceful
  shutdown, structured logs, typed config.
