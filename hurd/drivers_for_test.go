package hurd

// The engine tests exercise construction through the registry, so the test
// binary compiles in the same default driver set as the shipped binary
// (drivers_gen.go in the root package — which the hurd package deliberately
// does not import).
import (
	_ "github.com/mikefsq/goalpaca-devices/asiam5"
	_ "github.com/mikefsq/goalpaca-devices/asieaf"
	_ "github.com/mikefsq/goalpaca-devices/asiefw"
	_ "github.com/mikefsq/goalpaca-devices/astrocam"
	_ "github.com/mikefsq/goalpaca-devices/focuscube"
	_ "github.com/mikefsq/goalpaca-devices/focuslynx"
	_ "github.com/mikefsq/goalpaca-devices/mgpbox"
	_ "github.com/mikefsq/goalpaca-devices/oasisfoc"
	_ "github.com/mikefsq/goalpaca-devices/oasisfw"
	_ "github.com/mikefsq/goalpaca-devices/onstep"
	_ "github.com/mikefsq/goalpaca-devices/rst"
	_ "github.com/mikefsq/goalpaca-devices/sim" // sim-* devices (the root binary selects them via hurd.conf)
	_ "github.com/mikefsq/goalpaca-devices/tenmicron"
	_ "github.com/mikefsq/goalpaca-devices/unihedron"
)
