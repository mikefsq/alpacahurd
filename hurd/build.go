package hurd

import (
	"fmt"
	"strings"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// buildDevice constructs the device named by spec.Driver through the driver
// registry. Construction touches no hardware; the device's hardware loop is
// started later by its Alpaca server's Run.
func buildDevice(spec DeviceSpec) (registry.Driver, alpacadev.Device, error) {
	drv, ok := registry.Lookup(spec.Driver)
	if !ok {
		switch strings.ToLower(spec.Driver) {
		case "asiccd", "asicaa":
			// The ZWO-SDK (cgo) devices are deliberately not part of the vendor-free herd.
			return registry.Driver{}, nil, fmt.Errorf("%q needs the ZWO SDK (cgo) and is not built into alpacahurd; "+
				"run its standalone cmd, or use the Go \"asicam\" driver for ZWO cameras", spec.Driver)
		}
		return registry.Driver{}, nil, fmt.Errorf("unknown driver %q — not compiled into this binary "+
			"(alpacahurd -drivers lists what is; add its module to hurd.conf and rebuild)", spec.Driver)
	}
	dev, err := drv.New(registry.Spec{Driver: drv.Name, Name: spec.Name, Raw: spec.Raw})
	if err != nil {
		return registry.Driver{}, nil, err
	}
	return drv, dev, nil
}

// registerDevice constructs spec's device and registers it on srv under its
// ASCOM type. Every device is number 0 of its type on its own per-port server.
func registerDevice(srv *alpacadev.Server, spec DeviceSpec) (alpacadev.Device, error) {
	drv, dev, err := buildDevice(spec)
	if err != nil {
		return nil, err
	}
	return dev, srv.Register(drv.Type, 0, dev)
}
