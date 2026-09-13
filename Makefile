# alpacahurd — build and install the herd. Run `make help` for the targets.
#

export GOWORK := off

BIN := alpacahurd

UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
CGO ?= 1
else
CGO ?= 0
endif

.PHONY: all help gen build fat deb tidy deps test install uninstall clean

all: build ## Build the orchestrator with simulators

help: ## Show available targets
	@echo "Usage: make <target>"
	@echo
	@grep -hE '^[a-z-]+:.*## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN{FS=":.*## "}{printf "  %-10s %s\n", $$1, $$2}'

gen: ## Regenerate driver imports from hurd.conf
	go run ./internal/gendrivers

build: ## Build ./alpacahurd; hardware drivers run as separate binaries
	CGO_ENABLED=$(CGO) go build -o $(BIN) .

fat: gen ## Build ./alpacahurd with the drivers listed in hurd.conf
	CGO_ENABLED=$(CGO) go build -tags fat -o $(BIN) .

deb: ## Build Debian packages in dist/
	build/build-deb

deps: ## Download the dependency versions recorded in go.mod
	go mod download

tidy: ## Regenerate imports and tidy the recorded module dependencies
	go run ./internal/gendrivers
	go mod tidy

test: ## Run the Go test suite
	go test ./...

install: ## Install the built binary, service, and config
ifeq ($(UNAME_S),Darwin)
	./deploy/install-macos.sh ./$(BIN)
else
	./deploy/install.sh ./$(BIN)
endif

uninstall: ## Remove the service and binary; keep config
ifeq ($(UNAME_S),Darwin)
	./deploy/uninstall-macos.sh
else
	./deploy/uninstall.sh
endif

clean: ## Remove the built binary and dist/
	rm -f $(BIN)
	rm -rf dist
