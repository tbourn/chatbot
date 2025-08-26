# ------ Config ------
APP            ?= go-chat-backend
PKG            ?= ./...
MAIN           ?= ./cmd/server
BIN            ?= server
PORT           ?= 8080

# Build metadata
VERSION        ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT         ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE           ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Go settings
GOFLAGS        ?= -trimpath
LDFLAGS        ?= -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)
CGO_ENABLED    ?= 1

# Docker
IMAGE_NAME     ?= $(APP)
IMAGE_TAG      ?= $(VERSION)
PLATFORM       ?= linux/amd64

# Tools
SWAG_BIN       ?= swag
LINTER         ?= golangci-lint

# ------ Helpers ------
.SILENT:
.SHELLFLAGS = -eu -o pipefail -c
.DEFAULT_GOAL := help

# Detect if golangci-lint exists (optional)
HAS_LINTER := $(shell command -v $(LINTER) >/dev/null 2>&1 && echo yes || echo no)
HAS_SWAG   := $(shell command -v $(SWAG_BIN) >/dev/null 2>&1 && echo yes || echo no)

# ------ Targets ------

help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<TARGET>\033[0m\n\nTargets:\n"} /^[a-zA-Z0-9_%-]+:.*##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

tidy: ## Sync go.mod/go.sum
	go mod tidy

deps: ## Download deps
	go mod download

fmt: ## Format code
	go fmt $(PKG)

vet: ## Static analysis (vet)
	go vet $(PKG)

lint: ## Run golangci-lint (if installed)
ifneq ($(HAS_LINTER),yes)
	@echo "golangci-lint not found. Install: https://golangci-lint.run/usage/install/"
else
	$(LINTER) run
endif

test: ## Run unit tests (race + coverage)
	go test $(PKG) -race -covermode=atomic -coverprofile=coverage.out

build: ## Build local binary ./dist/$(BIN)
	mkdir -p dist
	CGO_ENABLED=$(CGO_ENABLED) GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BIN) $(MAIN)

run: ## Run the API locally
	GOFLAGS="$(GOFLAGS)" CGO_ENABLED=$(CGO_ENABLED) go run -ldflags "$(LDFLAGS)" $(MAIN)

swag: ## Generate Swagger docs into ./docs (requires swag)
ifneq ($(HAS_SWAG),yes)
	@echo "swag not found. Install: go install github.com/swaggo/swag/cmd/swag@latest"
else
	$(SWAG_BIN) init -g cmd/server/main.go -o docs
endif

docker-build: ## Build Docker image
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) \
		--platform $(PLATFORM) \
		-t $(IMAGE_NAME):$(IMAGE_TAG) .

docker-run: ## Run container (binds $(PORT))
	mkdir -p .data
	MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL="*" \
	docker run --rm -p $(PORT):8080 \
		-e PORT=8080 \
		-e DB_PATH=/data/app.db \
		-e DATA_MD=/app/data/data.md \
		-e SWAGGER_ENABLED=1 \
		-e LOG_LEVEL=$(LOG_LEVEL) \
		-e DEBUG_INDEX_PROBE=1 \
		-e OTEL_ENABLED=1 \
		-e OTEL_EXPORTER_OTLP_ENDPOINT=host.docker.internal:4317 \
		-e OTEL_EXPORTER_OTLP_INSECURE=true \
		-v "$(PWD)/.data:/data" \
		-v "$(PWD)/data:/app/data" \
		$(IMAGE_NAME):$(IMAGE_TAG)

compose-up: ## docker-compose up -d
	docker compose up -d --build

compose-down: ## docker-compose down
	docker compose down

clean: ## Remove build artifacts
	rm -rf dist coverage.out

# Convenience meta-targets
ci: tidy deps fmt vet lint test build ## Run all checks for CI
