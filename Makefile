# genestack-cli — build & dev tasks. Run `make help` for the list.

BINARY    := genestack
PKG       := ./cmd/genestack
DIST      := dist
GOFLAGS   ?=
LDFLAGS   := -s -w
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.DEFAULT_GOAL := build

.PHONY: build
build: ## Build the binary for the host platform (./genestack)
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BINARY) $(PKG)

.PHONY: build-all
build-all: ## Cross-compile for all platforms into dist/
	@mkdir -p $(DIST)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		out=$(DIST)/$(BINARY)-$$os-$$arch; \
		echo "  $$out"; \
		GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $$out $(PKG) || exit 1; \
	done

.PHONY: install
install: ## go install into $GOBIN (or $GOPATH/bin)
	go install -ldflags '$(LDFLAGS)' $(PKG)

.PHONY: run
run: build ## Build then launch the TUI
	./$(BINARY)

.PHONY: test
test: ## Run all tests
	go test ./...

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: fmt
fmt: ## Format Go sources
	gofmt -w ./cmd ./internal

.PHONY: tidy
tidy: ## go mod tidy
	go mod tidy

.PHONY: check
check: fmt vet test ## fmt + vet + test

.PHONY: clean
clean: ## Remove build artifacts
	rm -f $(BINARY)
	rm -rf $(DIST)

.PHONY: lab-up
lab-up: ## Create the OrbStack lab (deployer + targets)
	scripts/orbstack-lab.sh up

.PHONY: lab-status
lab-status: ## Show OrbStack lab machines + IPs
	scripts/orbstack-lab.sh status

.PHONY: lab-down
lab-down: ## Delete the OrbStack lab
	scripts/orbstack-lab.sh down

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
