BINARY   := bin/herald
MODULE   := github.com/nogo/herald
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
TAG      ?= $(shell git describe --tags --exact-match 2>/dev/null || echo "")
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
            -X $(MODULE)/cmd.commit=$(COMMIT) \
            -X $(MODULE)/cmd.tag=$(TAG) \
            -X $(MODULE)/cmd.date=$(DATE)

GOFLAGS  ?=
TESTFLAGS ?= -race -count=1

.PHONY: all build test vet fix lint clean install run help

all: vet test build ## Build after passing vet and tests

build: ## Build the herald binary
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) .

run: build ## Build and run herald serve
	./$(BINARY) serve

test: ## Run all tests
	go test $(TESTFLAGS) ./...

vet: ## Run go vet
	go vet ./...

fix: ## Auto-modernize code with go fix
	go fix ./...

lint: vet ## Run vet (add staticcheck/golangci-lint here if desired)

clean: ## Remove build artifacts
	rm -f $(BINARY)

install: build ## Install herald to GOBIN
	go install $(GOFLAGS) -ldflags "$(LDFLAGS)" .

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
