# alpacahurd — build and install the herd. Run `make help` for the targets.
#

BIN := alpacahurd

UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
CGO ?= 1
else
CGO ?= 0
endif

WS_DIRS := . \
	../goalpaca ../lx200 ../goindi ../astrocam ../goasi \
	../goasi/asiair ../ptp ../stellarmate \
	../oasis-astro ../optec ../pegasus-astro ../astromi.ch ../unihedron \
	../goalpaca-devices/tenmicron ../goalpaca-devices/asiam5 \
	../goalpaca-devices/onstep ../goalpaca-devices/rst \
	../goalpaca-devices/astrocam ../goalpaca-devices/asieaf \
	../goalpaca-devices/oasisfoc ../goalpaca-devices/focuscube \
	../goalpaca-devices/focuslynx ../goalpaca-devices/asiefw \
	../goalpaca-devices/oasisfw ../goalpaca-devices/mgpbox \
	../goalpaca-devices/unihedron ../goalpaca-devices/sim \
	../goalpaca-devices/asiair ../goalpaca-devices/ptpcam \
	../goalpaca-devices/smpro

.PHONY: all help gen workspace build fat deb tidy deps-head test install uninstall clean

all: build 

help: 
	@echo "alpacahurd make targets:"
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[1m%-10s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "'make tidy' resolves every dependency from the module proxy (no sibling"
	@echo "checkouts needed). 'make workspace' instead overlays a gitignored go.work"
	@echo "on the sibling repos next to this one, tracking their local HEAD."

gen: 
	go run ./internal/gendrivers

workspace: 
	@rm -f go.work go.work.sum
	@go work init
	@for d in $(WS_DIRS); do \
		if [ -d "$$d" ]; then go work use "$$d"; \
		else echo "  missing (skipped): $$d — clone it next to alpacahurd"; fi; \
	done
	@echo "go.work written over the present siblings"

build: 
	CGO_ENABLED=$(CGO) go build -o $(BIN) .

fat: gen 
	CGO_ENABLED=$(CGO) go build -tags fat -o $(BIN) .

deb: 
	build/build-deb

deps-head: 
	@self="$$(go list -m)"; \
	mods="$$(grep -oE 'github.com/mikefsq/[a-zA-Z0-9./-]+' go.mod | sort -u | grep -vxF "$$self")"; \
	[ -n "$$mods" ] || { echo "deps-head: no github.com/mikefsq dependencies in go.mod"; exit 0; }; \
	echo "$$mods" | sed 's/^/  /'; \
	go get $$(echo "$$mods" | sed 's/$$/@main/' | tr '\n' ' ')

tidy: 
	go run ./internal/gendrivers
	$(MAKE) deps-head
	go mod tidy

test: 
	go test ./...

install: 
ifeq ($(UNAME_S),Darwin)
	./deploy/install-macos.sh ./$(BIN)
else
	./deploy/install.sh ./$(BIN)
endif

uninstall: 
ifeq ($(UNAME_S),Darwin)
	./deploy/uninstall-macos.sh
else
	./deploy/uninstall.sh
endif

clean: 
	rm -f $(BIN)
	rm -rf dist
