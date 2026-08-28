# =====================================================================
# STAGE 1: Build the Go binary
# =====================================================================
FROM golang:1.27.0-trixie AS builder

WORKDIR /app

# Copy dependency manifests first for efficient layer caching
COPY go.mod ./
RUN go mod download

# Copy the entire source code
COPY . .

# Build the binary with flags optimized for a lightweight runtime container:
# - CGO_ENABLED=0 disables dynamic C links
# - GOOS=linux targets Linux environment
# - -ldflags="-w -s" strips debugging information to reduce size
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /out/server ./cmd/io

# =====================================================================
# STAGE 2: Run the binary inside a minimal image
# =====================================================================
FROM alpine:3.20

# Create a non-root user for security
RUN adduser -D -g '' appuser

WORKDIR /app

# Copy the compiled binary from the builder stage
COPY --from=builder /out/server .

USER appuser

EXPOSE 8080

CMD ["./server"]
