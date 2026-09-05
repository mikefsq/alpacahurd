# alpacahurd — build and install the herd. Run `make help` for the targets.
#

BIN := alpacahurd

UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
CGO ?= 1
else
CGO ?= 0
endif

.PHONY: all help gen build fat deb tidy deps-head test install uninstall clean

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

deps-head: ## Update mikefsq dependencies to their latest main commits
	@export GOWORK=off; \
	self="$$(go list -m)" || exit $$?; \
	mods="$$(grep -oE 'github.com/mikefsq/[a-zA-Z0-9./-]+' go.mod | sort -u | grep -vxF "$$self")"; \
	[ -n "$$mods" ] || { echo "deps-head: no github.com/mikefsq dependencies in go.mod"; exit 0; }; \
	echo "$$mods" | sed 's/^/  /'; \
	go get $$(echo "$$mods" | sed 's/$$/@main/' | tr '\n' ' ')

tidy: ## Regenerate imports, update dependencies to main, and tidy modules
	go run ./internal/gendrivers
	$(MAKE) deps-head
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
