BINARY  := medication-api
CMD_DIR := ./cmd/medication-api
BIN_DIR := ./bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
IMAGE   ?= medication-api:$(VERSION)

# Database used by the integration tests (the PostgreSQL from compose.yaml).
TEST_DATABASE_URL ?= postgres://medication:medication@localhost:5432/medication?sslmode=disable

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show available commands
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: run
run: ## Run the API locally with in-memory storage
	LOG_FORMAT=text go run -ldflags "$(LDFLAGS)" $(CMD_DIR)

.PHONY: build
build: ## Build a static binary into ./bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(CMD_DIR)

.PHONY: test
test: ## Run unit tests with the race detector (integration tests are skipped)
	go test -race -count=1 ./...

.PHONY: test-integration
test-integration: db-up ## Run all tests, including PostgreSQL integration tests
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -race -count=1 ./...

.PHONY: cover
cover: ## Run tests and show coverage per function
	go test -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

.PHONY: fuzz
fuzz: ## Fuzz the domain validation for 30 seconds
	go test -run=^$$ -fuzz=FuzzNew -fuzztime=30s ./internal/medication/

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run ./...

.PHONY: fmt
fmt: ## Format code (gofumpt + goimports)
	golangci-lint fmt ./...

.PHONY: vuln
vuln: ## Check dependencies for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: tidy
tidy: ## Sync go.mod and go.sum
	go mod tidy

.PHONY: docker-build
docker-build: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

.PHONY: up
up: ## Start the full stack (API + PostgreSQL + Grafana LGTM)
	docker compose up --build -d
	@echo "API: http://localhost:8080   Grafana: http://localhost:3000"

.PHONY: down
down: ## Stop the stack (data is kept; use "docker compose down -v" to delete it)
	docker compose down

.PHONY: db-up
db-up: ## Start only PostgreSQL and wait until it is healthy
	docker compose up -d --wait postgres

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out
