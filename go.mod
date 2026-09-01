module github.com/mikefsq/alpacahurd

go 1.25.0

// The driver-module requirements (github.com/mikefsq/goalpaca-devices/...,
// selected by hurd.conf -> drivers_gen.go) are added by `make gen`, which runs
// `go mod tidy`. During local development the go.work workspace supplies the
// sibling checkouts instead, so this file only pins what tidy has confirmed
// published.
require (
	github.com/mikefsq/goalpaca v0.3.2-0.20260830025450-2da7704371b4
	github.com/mikefsq/goindi v0.0.0-20260901000507-38e944df023c // indirect
	github.com/mikefsq/lx200 v0.2.2-0.20260828004623-148d3f4ede4b // indirect
	golang.org/x/net v0.46.0
)

require (
	github.com/mikefsq/goalpaca-devices/asiair v0.0.0-20260828004746-46e991ee96b4
	github.com/mikefsq/goalpaca-devices/asiam5 v0.0.0-20260828004746-46e991ee96b4
	github.com/mikefsq/goalpaca-devices/asieaf v0.0.0-20260828004746-46e991ee96b4
	github.com/mikefsq/goalpaca-devices/asiefw v0.0.0-20260828004746-46e991ee96b4
	github.com/mikefsq/goalpaca-devices/astrocam v0.0.0-20260828004746-46e991ee96b4
	github.com/mikefsq/goalpaca-devices/focuscube v0.0.0-20260828004746-46e991ee96b4
	github.com/mikefsq/goalpaca-devices/focuslynx v0.0.0-20260828004746-46e991ee96b4
	github.com/mikefsq/goalpaca-devices/mgpbox v0.0.0-20260828004746-46e991ee96b4
	github.com/mikefsq/goalpaca-devices/oasisfoc v0.0.0-20260828004746-46e991ee96b4
	github.com/mikefsq/goalpaca-devices/oasisfw v0.0.0-20260828004746-46e991ee96b4
	github.com/mikefsq/goalpaca-devices/onstep v0.0.0-20260828004746-46e991ee96b4
	github.com/mikefsq/goalpaca-devices/ptpcam v0.0.0-20260828004746-46e991ee96b4
	github.com/mikefsq/goalpaca-devices/rst v0.0.0-20260828004746-46e991ee96b4
	github.com/mikefsq/goalpaca-devices/sim v0.0.0-20260828004746-46e991ee96b4
	github.com/mikefsq/goalpaca-devices/smpro v0.0.0-20260828004746-46e991ee96b4
	github.com/mikefsq/goalpaca-devices/tenmicron v0.0.0-20260828004746-46e991ee96b4
	github.com/mikefsq/goalpaca-devices/unihedron v0.0.0-20260828004746-46e991ee96b4
)

require (
	github.com/mikefsq/goasi/asiair v0.0.0-20260821215032-7011c101a08c // indirect
	github.com/mikefsq/ptp v0.0.0-20260819023716-4dce94a699a8 // indirect
	github.com/mikefsq/stellarmate v0.0.0-20260821231638-f0de4fc3e094 // indirect
)

require (
	github.com/adrianmo/go-nmea v1.10.0 // indirect
	github.com/mikefsq/astrocam v0.0.0-20260828004017-1aa1992b84f1 // indirect
	github.com/mikefsq/astromi.ch v0.1.1-0.20260817194910-b02747c7688b // indirect
	github.com/mikefsq/goasi v0.2.1-0.20260821215032-7011c101a08c // indirect
	github.com/mikefsq/oasis-astro v0.0.0-20260817201104-00ec21705131 // indirect
	github.com/mikefsq/optec v0.0.0-20260713175428-9a276b61f41e // indirect
	github.com/mikefsq/pegasus-astro v0.0.0-20260713175352-7c8746528e1c // indirect
	github.com/mikefsq/unihedron v0.1.1-0.20260821215438-d164bf0a596c // indirect
	go.bug.st/serial v1.7.1 // indirect
	golang.org/x/sys v0.43.0
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
