BINARY  := medication-api
CMD_DIR := ./cmd/medication-api
BIN_DIR := ./bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show available commands
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

.PHONY: run
run: ## Run the API locally
	go run -ldflags "$(LDFLAGS)" $(CMD_DIR)

.PHONY: build
build: ## Build a static binary into ./bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(CMD_DIR)

.PHONY: test
test: ## Run tests with the race detector
	go test -race -count=1 ./...

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run ./...

.PHONY: fmt
fmt: ## Format code (gofumpt + goimports)
	golangci-lint fmt ./...

.PHONY: tidy
tidy: ## Sync go.mod and go.sum
	go mod tidy

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out