# IO — developer entrypoints. Run `make help` for the list.

GO          ?= go
SERVICES    := io pong-service notification-service
STATICCHECK ?= go run honnef.co/go/tools/cmd/staticcheck@v0.8.1
GOVULNCHECK ?= go run golang.org/x/vuln/cmd/govulncheck@v1.7.0

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build every service binary into ./bin
	@mkdir -p bin
	@for s in $(SERVICES); do echo "build $$s"; $(GO) build -trimpath -o bin/$$s ./cmd/$$s; done

.PHONY: test
test: ## Run unit tests with the race detector and coverage
	$(GO) test -race -cover ./...

.PHONY: fmt
fmt: ## Format all Go code
	gofmt -w $(shell find . -name '*.go' -not -path './vendor/*')

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: fmt-check
fmt-check: ## Fail if any file is not gofmt-clean
	@unformatted=$$(gofmt -l $(shell find . -name '*.go' -not -path './vendor/*')); \
	if [ -n "$$unformatted" ]; then echo "not gofmt-clean:"; echo "$$unformatted"; exit 1; fi

.PHONY: lint
lint: ## Run staticcheck
	$(STATICCHECK) ./...

.PHONY: tidy
tidy: ## Tidy and verify go.mod / go.sum
	$(GO) mod tidy
	$(GO) mod verify

.PHONY: vulncheck
vulncheck: ## Scan dependencies for known vulnerabilities
	$(GOVULNCHECK) ./...

.PHONY: check
check: fmt-check vet lint test vulncheck ## Everything CI runs

.PHONY: run-io run-pong run-notification
run-io: ## Run io locally (PORT 8080, peer = pong on 8081)
	PORT=8080 PEER_URL=http://localhost:8081 APP_VERSION=dev $(GO) run ./cmd/io
run-pong: ## Run pong-service locally (PORT 8081, peer = io on 8080)
	PORT=8081 PEER_URL=http://localhost:8080 APP_VERSION=dev $(GO) run ./cmd/pong-service
run-notification: ## Run notification-service locally (needs DATABASE_URL, REDIS_URL)
	PORT=8082 $(GO) run ./cmd/notification-service

.PHONY: compose-up compose-down
compose-up: ## Start the full system with Docker Compose
	docker compose up --build
compose-down: ## Stop and remove the Compose stack
	docker compose down -v

.PHONY: images
images: ## Build a container image per service
	@for s in $(SERVICES); do echo "image $$s"; docker build --build-arg SERVICE=$$s -t $$s:local .; done
