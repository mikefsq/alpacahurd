# alpacahurd — build and install the herd. Run `make help` for the targets.
#
# Pre-release, the libraries aren't tagged: everything tracks HEAD through the
# go.work workspace, so `make` needs the sibling repos checked out next to this
# one and a go.work over them (`make workspace`). `make tidy` is for later, once
# the modules are published and a plain clone can resolve them from the proxy.
#
# On Windows (no make): go work use over the siblings, then `go build`.

BIN := alpacahurd

# macOS drivers reach USB/HID through IOKit and need cgo (Xcode command-line
# tools); the Linux (usbfs/hidraw) and Windows (WinUSB) transports are pure Go,
# so those builds are static.
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
CGO ?= 1
else
CGO ?= 0
endif

# Sibling module checkouts the workspace overlays, resolved relative to this
# repo. The goalpaca-devices drivers are the default hurd.conf set; the rest are
# the libraries they and the engine depend on. asiccd/asicaa (ZWO SDK, cgo) and
# the deprecated fleet are intentionally omitted.
WS_DIRS := . \
	../goalpaca ../lx200 ../goindi ../astrocam ../goasi \
	../oasis-astro ../optec ../pegasus-astro ../astromi.ch ../unihedron \
	../goalpaca-devices/tenmicron ../goalpaca-devices/asiam5 \
	../goalpaca-devices/onstep ../goalpaca-devices/rst \
	../goalpaca-devices/astrocam ../goalpaca-devices/asieaf \
	../goalpaca-devices/oasisfoc ../goalpaca-devices/focuscube \
	../goalpaca-devices/focuslynx ../goalpaca-devices/asiefw \
	../goalpaca-devices/oasisfw ../goalpaca-devices/mgpbox \
	../goalpaca-devices/unihedron ../goalpaca-devices/sim

.PHONY: all help gen workspace build tidy test install uninstall clean

all: gen build ## regenerate drivers_gen.go from hurd.conf, then build (default)

help: ## list the targets
	@echo "alpacahurd make targets:"
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[1m%-10s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "Pre-release: run 'make workspace' once on a fresh box (needs the sibling"
	@echo "repos checked out next to this one); go.work is gitignored. 'make tidy' is"
	@echo "for later, once the modules are published."

gen: ## regenerate drivers_gen.go from hurd.conf
	go run ./internal/gendrivers

# workspace (re)creates the gitignored go.work over whichever sibling checkouts
# are present, so a fresh box resolves every internal dep to its local HEAD
# without any module tags. Missing siblings are reported, not fatal — clone them
# next to this repo and re-run.
workspace: ## (re)write go.work over the present sibling checkouts
	@rm -f go.work go.work.sum
	@go work init
	@for d in $(WS_DIRS); do \
		if [ -d "$$d" ]; then go work use "$$d"; \
		else echo "  missing (skipped): $$d — clone it next to alpacahurd"; fi; \
	done
	@echo "go.work written over the present siblings"

build: ## build only (skip regeneration)
	CGO_ENABLED=$(CGO) go build -o $(BIN) .

# tidy resolves module requirements from the network — only meaningful once the
# libraries are published/tagged. Pre-release this fails on the untagged deps;
# use `make workspace` instead.
tidy: ## resolve module versions from the network (publish prep only)
	go run ./internal/gendrivers
	go mod tidy

test: ## run the test suite
	go test ./...

install: ## install as a service (systemd on Linux, launchd on macOS) — needs root
ifeq ($(UNAME_S),Darwin)
	./deploy/install-macos.sh ./$(BIN)
else
	./deploy/install.sh ./$(BIN)
endif

uninstall: ## stop and remove the service (config is kept) — needs root
ifeq ($(UNAME_S),Darwin)
	./deploy/uninstall-macos.sh
else
	./deploy/uninstall.sh
endif

clean: ## remove the built binary
	rm -f $(BIN)
