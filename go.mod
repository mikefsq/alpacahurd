module github.com/mikefsq/alpacahurd

go 1.25.0

// The driver-module requirements (github.com/mikefsq/goalpaca-devices/...,
// selected by hurd.conf -> drivers_gen.go) are added by `make gen`, which runs
// `go mod tidy`. During local development the go.work workspace supplies the
// sibling checkouts instead, so this file only pins what tidy has confirmed
// published.
require (
	github.com/mikefsq/goalpaca v0.2.0
	github.com/mikefsq/goindi v0.0.0-20260623000347-2dda0b2dec05
	github.com/mikefsq/lx200 v0.2.0
	golang.org/x/net v0.46.0
)
