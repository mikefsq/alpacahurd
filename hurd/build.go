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
				"run its standalone cmd, or use the Go \"astrocam\" driver for ZWO cameras", spec.Driver)
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

// deviceNumbers hands out ASCOM device numbers for the entries sharing one
// Alpaca port. Numbers are scoped per device type — the URL is
// /api/v1/{type}/{number}/ — so a camera and a focuser on one port are both
// device 0, while two cameras there are 0 and 1.
//
// An entry pins its own number with "device"; the rest take the lowest free one
// in config order. Numbers are claimed as the entries are walked, so pinning a
// number an earlier unpinned entry already took is an error rather than a silent
// reshuffle: pin ascending, or pin none.
type deviceNumbers struct {
	used map[alpacadev.DeviceType]map[int]bool
}

// assign returns the device number for spec, claiming it.
func (n *deviceNumbers) assign(spec DeviceSpec, typ alpacadev.DeviceType) (int, error) {
	if n.used == nil {
		n.used = map[alpacadev.DeviceType]map[int]bool{}
	}
	taken := n.used[typ]
	if taken == nil {
		taken = map[int]bool{}
		n.used[typ] = taken
	}
	if spec.Device != nil {
		num := *spec.Device
		if num < 0 {
			return 0, fmt.Errorf(`"device": %d is negative`, num)
		}
		if taken[num] {
			return 0, fmt.Errorf(`"device": %d is already taken by another %s on port %d`, num, typ, spec.Port)
		}
		taken[num] = true
		return num, nil
	}
	num := 0
	for taken[num] {
		num++
	}
	taken[num] = true
	return num, nil
}

// registerDevice constructs spec's device and registers it on srv under its
// ASCOM type, at the device number nums hands out for it.
func registerDevice(srv *alpacadev.Server, spec DeviceSpec, nums *deviceNumbers) (alpacadev.Device, int, error) {
	drv, dev, err := buildDevice(spec)
	if err != nil {
		return nil, 0, err
	}
	num, err := nums.assign(spec, drv.Type)
	if err != nil {
		return nil, 0, err
	}
	return dev, num, srv.Register(drv.Type, num, dev)
}
