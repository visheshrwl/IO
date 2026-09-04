# syntax=docker/dockerfile:1

# =====================================================================
# Build stage
# =====================================================================
FROM golang:1.27.0-trixie AS builder

# Which cmd/ binary to build. All services share this Dockerfile since they
# live in the same Go module.
ARG SERVICE=io
# Stamped into the image labels; also handy to pass through to the binary.
ARG VERSION=dev

WORKDIR /src

# Dependency manifests first, so `go mod download` is only re-run when they
# change. go.sum must be present for a verifiable, reproducible download.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# CGO off => a static binary that runs on a distroless/scratch base.
# -trimpath keeps build-host paths out of the binary; -w -s drop DWARF and
# the symbol table. Module and build caches are mounted, not baked in.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-w -s" -o /out/service ./cmd/${SERVICE}

# =====================================================================
# Runtime stage — distroless: no shell, no package manager, runs as a
# non-root user (uid 65532) out of the box.
# =====================================================================
FROM gcr.io/distroless/static-debian12:nonroot

ARG SERVICE
ARG VERSION
LABEL org.opencontainers.image.title="${SERVICE}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/visheshrwl/IO"

COPY --from=builder /out/service /service

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/service"]
