package hurd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// subSpecs expands MultiKey blocks using their positions as device numbers.
// Flat entries remain unchanged; an empty multi-device entry defaults to two blocks.
func subSpecs(spec DeviceSpec) ([]DeviceSpec, error) {
	drv, ok := registry.Lookup(spec.Driver)
	if !ok || drv.MultiKey == "" {
		return []DeviceSpec{spec}, nil
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(spec.Raw, &m)
	blocksRaw, has := m[drv.MultiKey]
	var raws []json.RawMessage
	if has {
		if err := json.Unmarshal(blocksRaw, &raws); err != nil {
			return nil, fmt.Errorf("%q: %w", drv.MultiKey, err)
		}
	} else if len(pinnedKeys(spec.Raw)) > 0 {
		return []DeviceSpec{spec}, nil
	}
	if len(raws) == 0 {
		raws = []json.RawMessage{json.RawMessage("{}"), json.RawMessage("{}")}
	}
	subs := make([]DeviceSpec, 0, len(raws))
	for i, r := range raws {
		var b struct {
			Name   string `json:"name"`
			Enable *bool  `json:"enable"`
		}
		if err := json.Unmarshal(r, &b); err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", drv.MultiKey, i, err)
		}
		sub := spec
		sub.Raw = append(json.RawMessage(nil), r...)
		sub.Name = b.Name
		num, block := i, i
		sub.Device, sub.Block = &num, &block
		sub.Enable = nil
		if !spec.enabled() || (b.Enable != nil && !*b.Enable) {
			off := false
			sub.Enable = &off
		}
		// Recompute pinned fields from this block in setupFormFor.
		sub.Pinned = nil
		subs = append(subs, sub)
	}
	return subs, nil
}

// wireFrontEnd starts the optional driver front-end and returns its cancel function.
// The device getter follows registration changes during reload.
func wireFrontEnd(ctx context.Context, drv registry.Driver, srv *alpacadev.Server, num int, spec DeviceSpec, hosts []string) context.CancelFunc {
	if drv.FrontEnd == nil {
		return func() {}
	}
	fctx, stop := context.WithCancel(ctx)
	getDev := func() alpacadev.Device {
		d, _ := srv.Device(drv.Type, num)
		return d
	}
	if err := drv.FrontEnd(fctx, getDev, spec.Raw, hosts); err != nil {
		log.Printf("alpacahurd: %s: front-end: %v", spec.Instance, err)
	}
	return stop
}

// buildDevice constructs a registered driver without opening hardware.
func buildDevice(spec DeviceSpec) (registry.Driver, alpacadev.Device, error) {
	drv, ok := registry.Lookup(spec.Driver)
	if !ok {
		switch strings.ToLower(spec.Driver) {
		case "asiccd", "asicaa":
			return registry.Driver{}, nil, fmt.Errorf("%q needs the ZWO SDK (cgo) and is not built into alpacahurd; "+
				"run its standalone cmd, or use the Go \"astrocam\" driver for ZWO cameras", spec.Driver)
		}
		return registry.Driver{}, nil, fmt.Errorf("unknown driver %q: not compiled into this binary "+
			"(alpacahurd -drivers lists what is; add its module to hurd.conf and rebuild)", spec.Driver)
	}
	devNum := 0
	if spec.Device != nil {
		devNum = *spec.Device
	}
	dev, err := drv.New(registry.Spec{Driver: drv.Name, Name: spec.Name, Instance: spec.Instance, Raw: spec.Raw, Device: devNum})
	if err != nil {
		return registry.Driver{}, nil, err
	}
	return drv, dev, nil
}

// deviceNumbers allocates numbers per ASCOM type on one port.
// Entries claim numbers in order; a later pin cannot displace an earlier assignment.
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

// registerDevice constructs and registers a device with its setup form.
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

// attachSetupForm registers a generated form unless the device supplies its own.
func attachSetupForm(srv *alpacadev.Server, drv registry.Driver, dev alpacadev.Device, num int, spec DeviceSpec) error {
	sc, err := setupFormFor(drv, dev, spec)
	if err != nil {
		return err
	}
	if sc == nil {
		return nil
	}
	// Set the path before RegisterConfigurable loads persisted settings.
	// Multi-device blocks after the first use a position suffix.
	if spec.Instance != "" {
		stem := spec.Instance
		if spec.Block != nil && *spec.Block > 0 {
			stem += "." + strconv.Itoa(*spec.Block)
		}
		if err := srv.SettingsPath(drv.Type, num, filepath.Join(stateDevicesDir(), stem+".json")); err != nil {
			return err
		}
	}
	return srv.RegisterConfigurable(drv.Type, num, sc)
}

// setupFormFor builds a generated form, or returns nil if none is needed.
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

// reloaderFor rebuilds a device and its form from the current file and state.
// Inline entries use the loaded spec. Driver changes and disabled entries require a restart.
func reloaderFor(spec DeviceSpec) alpacadev.Reloader {
	return func(context.Context) (alpacadev.Device, alpacadev.Configurable, error) {
		cur := spec
		if spec.Instance != "" && spec.Source != "" {
			fresh, err := loadDeviceFile(spec.Source, stateDevicesDir())
			if err != nil {
				return nil, nil, err
			}
			cur = fresh
			if spec.Block != nil {
				subs, err := subSpecs(fresh)
				if err != nil {
					return nil, nil, err
				}
				if *spec.Block >= len(subs) || subs[*spec.Block].Block == nil {
					return nil, nil, fmt.Errorf("%s no longer has block %d; restart alpacahurd to remove the device", spec.Source, *spec.Block)
				}
				cur = subs[*spec.Block]
			}
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
		return dev, sc, nil
	}
}

// pinnedKeys returns the driver-owned keys present in a config entry.
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
