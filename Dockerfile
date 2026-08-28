# =====================================================================
# STAGE 1: Build the Go binary
# =====================================================================
FROM golang:1.27.0-trixie AS builder

# Set the working directory inside the container
WORKDIR /app

# Install git and SSL certificates (needed for external net/http requests)
# RUN apk add --no-cache git ca-certificates

# Copy dependency manifests first for efficient caching
COPY go.mod ./
# Copy go.sum as well if you have external dependencies:
# COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the entire source code
COPY . .

# Build the binary with flags optimized for a lightweight scratch container:
# - CGO_ENABLED=0 disables dynamic C++ links
# - GOOS=linux targets Linux environment
# - -ldflags="-w -s" strips debugging information to reduce size
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o server .

# =====================================================================
# STAGE 2: Run the binary inside a minimal image
# =====================================================================
FROM alpine:3.20

# Create a non-root user for security
RUN adduser -D -g '' appuser

WORKDIR /app

# Copy SSL certificates from the builder stage so net/http client can make HTTPS calls
# COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy the compiled binary from the builder stage
COPY --from=builder /app/server .

# Use the non-root user to run the application
USER appuser

# Expose the port your net/http server listens on (e.g., 8080)
EXPOSE 8080

# Command to run the application
CMD ["./server"]
