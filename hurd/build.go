package hurd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
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
	dev, err := drv.New(registry.Spec{Driver: drv.Name, Name: spec.Name, Instance: spec.Instance, Raw: spec.Raw})
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

// release frees a number claimed by assign, when its device is unregistered.
func (n *deviceNumbers) release(typ alpacadev.DeviceType, num int) {
	if n.used != nil && n.used[typ] != nil {
		delete(n.used[typ], num)
	}
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
	if err := srv.Register(drv.Type, num, dev); err != nil {
		return nil, 0, err
	}
	if err := attachSetupForm(srv, drv, dev, num, spec); err != nil {
		return nil, 0, err
	}
	return dev, num, nil
}

// attachSetupForm gives a device a generated browser setup form when its driver
// supplies a Config struct and the device has no form of its own. Every key the
// config entry names is a host-supplied value and renders locked, so the setup
// page changes only what the entry left unset; the config file stays the
// authority for what it says.
func attachSetupForm(srv *alpacadev.Server, drv registry.Driver, dev alpacadev.Device, num int, spec DeviceSpec) error {
	sc, err := setupFormFor(drv, dev, spec)
	if err != nil {
		return err
	}
	if sc == nil {
		return nil
	}
	// A devices.d entry persists setup-page changes to its own state file, the
	// one the overlay reads back at the next start. An inline entry has no
	// instance name and keeps the server's default path under the state dir.
	// The path goes first: on a running server RegisterConfigurable applies
	// the persisted settings at once, from the key set by then.
	if spec.Instance != "" {
		if err := srv.SettingsPath(drv.Type, num, filepath.Join(stateDevicesDir(), spec.Instance+".json")); err != nil {
			return err
		}
	}
	return srv.RegisterConfigurable(drv.Type, num, sc)
}

// setupFormFor builds the generated form for dev, or nil when the driver has
// no Config struct or the device has a form of its own. It is what
// attachSetupForm registers and what a reload rebuilds.
func setupFormFor(drv registry.Driver, dev alpacadev.Device, spec DeviceSpec) (alpacadev.Configurable, error) {
	if drv.Config == nil {
		return nil, nil
	}
	if _, own := dev.(alpacadev.Configurable); own {
		return nil, nil
	}
	pinned := spec.Pinned
	if pinned == nil {
		pinned = pinnedKeys(spec.Raw) // an inline entry: every driver key is the admin's
	}
	source := "set in the config file"
	if spec.Source != "" {
		source = "set in " + spec.Source
	}
	sc, err := alpacadev.NewStructConfig(dev, drv.Config, spec.Raw, pinned, source)
	if err != nil {
		return nil, fmt.Errorf("setup form for %s: %w", spec.Driver, err)
	}
	return sc, nil
}

// reloaderFor returns the Reloader for a compiled-in device: it re-reads the
// entry's device file with its state overlay (an inline entry has no file of
// its own and is rebuilt from the entry as loaded), constructs the device
// again through the same registry driver, and rebuilds its setup form. The
// server closes the old hardware and opens the new. optics, when the entry has
// a shared holder, is injected again so the INDI front-end keeps reading it.
//
// The entry has to still name the same driver and be enabled; a change of
// driver or a disabled entry is a restart matter, since the device's type and
// number are fixed for the server's lifetime.
func reloaderFor(spec DeviceSpec, optics *opticsHolder) alpacadev.Reloader {
	return func(context.Context) (alpacadev.Device, alpacadev.Configurable, error) {
		cur := spec
		if spec.Instance != "" && spec.Source != "" {
			fresh, err := loadDeviceFile(spec.Source, stateDevicesDir())
			if err != nil {
				return nil, nil, err
			}
			cur = fresh
		}
		if !strings.EqualFold(cur.Driver, spec.Driver) {
			return nil, nil, fmt.Errorf("%s now names driver %q, was %q; restart alpacahurd to change a device's driver", spec.Source, cur.Driver, spec.Driver)
		}
		if !cur.enabled() {
			return nil, nil, fmt.Errorf("%s is disabled; restart alpacahurd to remove the device", spec.Source)
		}
		drv, dev, err := buildDevice(cur)
		if err != nil {
			return nil, nil, err
		}
		sc, err := setupFormFor(drv, dev, cur)
		if err != nil {
			return nil, nil, err
		}
		if optics != nil {
			if oc, ok := dev.(opticsConfigurable); ok {
				oc.UseOptics(optics)
			}
		}
		return dev, sc, nil
	}
}

// pinnedKeys returns the driver-owned keys present in a config entry, which
// are the ones the host pinned. Common keys are the host's own and never reach
// a driver's form, so they are left out.
func pinnedKeys(raw json.RawMessage) map[string]bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	common := map[string]bool{}
	for _, k := range registry.CommonKeys() {
		common[strings.ToLower(k)] = true
	}
	pinned := map[string]bool{}
	for k := range m {
		if !common[strings.ToLower(k)] {
			pinned[k] = true
		}
	}
	return pinned
}
