# alpacahurd — build and install the herd. Run `make help` for the targets.
#

# Local go.work (git-ignored) selects sibling checkouts when present. GOWORK=off make
# builds the versions recorded in go.mod.
export GOWORK ?= auto

BIN := alpacahurd

UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
CGO ?= 1
else
CGO ?= 0
endif

.PHONY: all help gen build fat deb tidy deps test install seed uninstall clean

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
	GOWORK=off go mod download

tidy: ## Regenerate imports and tidy the recorded module dependencies
	go run ./internal/gendrivers
	GOWORK=off go mod tidy

test: ## Run the Go test suite
	go test ./...

install: build ## Build the standard binary and install it, the service, and config
ifeq ($(UNAME_S),Darwin)
	./deploy/install-macos.sh ./$(BIN)
else
	./deploy/install.sh ./$(BIN)
endif

seed: ## Write disabled templates for the compiled-in drivers; keeps existing files
ifeq ($(UNAME_S),Darwin)
	./$(BIN) -example-devices "/Library/Application Support/alpacahurd/devices.d"
else
	./$(BIN) -example-devices /etc/alpacahurd/devices.d
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
