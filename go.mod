module github.com/mikefsq/alpacahurd

go 1.25.0

// The driver-module requirements (github.com/mikefsq/goalpaca-devices/...,
// selected by hurd.conf -> drivers_gen.go) are added by `make gen`, which runs
// `go mod tidy`. During local development the go.work workspace supplies the
// sibling checkouts instead, so this file only pins what tidy has confirmed
// published.
require (
	github.com/mikefsq/goalpaca v0.3.1
	github.com/mikefsq/goindi v0.0.0-20260623000347-2dda0b2dec05
	github.com/mikefsq/lx200 v0.2.1-0.20260713175537-92b30a2f2ba4
	golang.org/x/net v0.46.0
)

require (
	github.com/mikefsq/goalpaca-devices/asiam5 v0.0.0-20260713180420-dc7fa975374b
	github.com/mikefsq/goalpaca-devices/asieaf v0.0.0-20260713180420-dc7fa975374b
	github.com/mikefsq/goalpaca-devices/asiefw v0.0.0-20260713180420-dc7fa975374b
	github.com/mikefsq/goalpaca-devices/astrocam v0.0.0-20260713180420-dc7fa975374b
	github.com/mikefsq/goalpaca-devices/focuscube v0.0.0-20260713180420-dc7fa975374b
	github.com/mikefsq/goalpaca-devices/focuslynx v0.0.0-20260713180420-dc7fa975374b
	github.com/mikefsq/goalpaca-devices/mgpbox v0.0.0-20260713180420-dc7fa975374b
	github.com/mikefsq/goalpaca-devices/oasisfoc v0.0.0-20260713180420-dc7fa975374b
	github.com/mikefsq/goalpaca-devices/oasisfw v0.0.0-20260713180420-dc7fa975374b
	github.com/mikefsq/goalpaca-devices/onstep v0.0.0-20260713180420-dc7fa975374b
	github.com/mikefsq/goalpaca-devices/rst v0.0.0-20260713180420-dc7fa975374b
	github.com/mikefsq/goalpaca-devices/sim v0.0.0-20260713180420-dc7fa975374b
	github.com/mikefsq/goalpaca-devices/tenmicron v0.0.0-20260713180420-dc7fa975374b
	github.com/mikefsq/goalpaca-devices/unihedron v0.0.0-20260713180420-dc7fa975374b
)

require (
	github.com/adrianmo/go-nmea v1.10.0 // indirect
	github.com/mikefsq/astrocam v0.0.0-20260707042246-a133a20b0bd3 // indirect
	github.com/mikefsq/astromi.ch v0.1.0 // indirect
	github.com/mikefsq/goasi v0.2.0 // indirect
	github.com/mikefsq/oasis-astro v0.0.0-20260613070221-c6e70b94291f // indirect
	github.com/mikefsq/optec v0.0.0-20260707021816-df3786ba6eb4 // indirect
	github.com/mikefsq/pegasus-astro v0.0.0-20260610070031-afd43eb66e1d // indirect
	github.com/mikefsq/unihedron v0.1.0 // indirect
	go.bug.st/serial v1.7.1 // indirect
	golang.org/x/sys v0.43.0 // indirect
)
