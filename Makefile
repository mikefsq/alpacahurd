# alpacahurd — build and install the herd. Run `make help` for the targets.
#
# Two ways to resolve the internal dependencies:
#   make tidy       pins everything from the module proxy into go.mod/go.sum — no
#                   sibling checkouts needed (the modules are published). Run once,
#                   then `make`; committing go.mod/go.sum makes a plain clone build.
#   make workspace  overlays a gitignored go.work on the sibling repos checked out
#                   next to this one, tracking their local HEAD (for library dev).
#
# On Windows (no make): use make.ps1 (tidy/workspace/build/...).

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
# the libraries they and the engine depend on. asiccd/asicaa (ZWO SDK, cgo) are
# intentionally omitted.
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
	@echo "'make tidy' resolves every dependency from the module proxy (no sibling"
	@echo "checkouts needed). 'make workspace' instead overlays a gitignored go.work"
	@echo "on the sibling repos next to this one, tracking their local HEAD."

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

# tidy resolves every module requirement from the proxy into go.mod/go.sum, so a
# fresh clone builds with no sibling checkouts. (`make workspace` is the alternative:
# track the siblings' local HEAD through go.work instead.)
tidy: ## resolve module versions from the module proxy (no siblings needed)
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
