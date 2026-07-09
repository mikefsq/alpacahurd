# alpacahurd — build and install the herd.
#
#   make            regenerate drivers_gen.go from hurd.conf, resolve modules, build
#   make build      build only (skip regeneration)
#   sudo make install    install as a service (systemd on Linux, launchd on macOS)
#   sudo make uninstall  stop and remove the service (config is kept)
#
# On Windows (no make): go run ./internal/gendrivers && go mod tidy && go build

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

.PHONY: all gen build test install uninstall clean

all: gen build

gen:
	go run ./internal/gendrivers
	go mod tidy

build:
	CGO_ENABLED=$(CGO) go build -o $(BIN) .

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
