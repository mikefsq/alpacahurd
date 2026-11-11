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

// subSpecs expands MultiKey blocks using their positions as device numbers, then
// appends the blocks of the driver's Extra kinds, each pinned to its block position.
// Flat entries remain unchanged; an empty multi-device entry defaults to two blocks.
func subSpecs(spec DeviceSpec) ([]DeviceSpec, error) {
	drv, ok := registry.Lookup(spec.Driver)
	if !ok {
		return []DeviceSpec{spec}, nil
	}
	subs, err := primarySubs(drv, spec)
	if err != nil {
		return nil, err
	}
	for _, k := range drv.Extra {
		blocks, err := registry.ExtraBlocks(drv, k, spec.Raw)
		if err != nil {
			return nil, err
		}
		for _, b := range blocks {
			sub := blockSpec(spec, b.Raw, b.Name, b.Index, b.Enable)
			sub.Extra = k.Type
			subs = append(subs, sub)
		}
	}
	return subs, nil
}

// primarySubs expands the primary kind's MultiKey blocks.
func primarySubs(drv registry.Driver, spec DeviceSpec) ([]DeviceSpec, error) {
	if drv.MultiKey == "" {
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
	} else {
		// Extra kinds' arrays do not make the entry flat.
		keys := pinnedKeys(spec.Raw)
		for _, k := range drv.Extra {
			for key := range keys {
				if strings.EqualFold(key, k.MultiKey) {
					delete(keys, key)
				}
			}
		}
		if len(keys) > 0 {
			return []DeviceSpec{spec}, nil
		}
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
		subs = append(subs, blockSpec(spec, append(json.RawMessage(nil), r...), b.Name, i, b.Enable))
	}
	return subs, nil
}

// blockSpec is spec narrowed to one block at index, numbered by it.
func blockSpec(spec DeviceSpec, raw json.RawMessage, name string, index int, enable *bool) DeviceSpec {
	sub := spec
	sub.Raw = raw
	sub.Name = name
	num, block := index, index
	sub.Device, sub.Block = &num, &block
	sub.Enable = nil
	if !spec.enabled() || (enable != nil && !*enable) {
		off := false
		sub.Enable = &off
	}
	// Recompute pinned fields from this block in setupFormFor.
	sub.Pinned = nil
	return sub
}

// kindOf returns the kind spec serves: the Extra kind it names, else the primary.
func kindOf(drv registry.Driver, spec DeviceSpec) registry.Kind {
	if spec.Extra != "" {
		for _, k := range drv.Extra {
			if k.Type == spec.Extra {
				return k
			}
		}
	}
	return drv.Kinds()[0]
}

// wireFrontEnd starts the optional driver front-end and returns its cancel function.
// The device getter follows registration changes during reload.
func wireFrontEnd(ctx context.Context, drv registry.Driver, srv *alpacadev.Server, num int, spec DeviceSpec, hosts []string) context.CancelFunc {
	// The front-end drives the primary device only.
	if drv.FrontEnd == nil || spec.Extra != "" {
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
	dev, err := kindOf(drv, spec).New(registry.Spec{Driver: drv.Name, Name: spec.Name, Instance: spec.Instance, Raw: spec.Raw, Device: devNum})
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
	typ := kindOf(drv, spec).Type
	num, err := nums.assign(spec, typ)
	if err != nil {
		return nil, 0, err
	}
	if err := srv.Register(typ, num, dev); err != nil {
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
	// Extra kinds add a type suffix; blocks after the first a position suffix.
	k := kindOf(drv, spec)
	if spec.Instance != "" {
		stem := spec.Instance + drv.StateSuffix(k)
		if spec.Block != nil && *spec.Block > 0 {
			stem += "." + strconv.Itoa(*spec.Block)
		}
		if err := srv.SettingsPath(k.Type, num, filepath.Join(stateDevicesDir(), stem+".json")); err != nil {
			return err
		}
	}
	return srv.RegisterConfigurable(k.Type, num, sc)
}

// setupFormFor builds a generated form, or returns nil if none is needed.
func setupFormFor(drv registry.Driver, dev alpacadev.Device, spec DeviceSpec) (alpacadev.Configurable, error) {
	newCfg := kindOf(drv, spec).Config
	if newCfg == nil {
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
	sc, err := alpacadev.NewStructConfig(dev, newCfg, spec.Raw, pinned, source)
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
				found := false
				for _, s := range subs {
					if s.Extra == spec.Extra && s.Block != nil && *s.Block == *spec.Block {
						cur, found = s, true
						break
					}
				}
				if !found {
					return nil, nil, fmt.Errorf("%s no longer has %sblock %d; restart alpacahurd to remove the device", spec.Source, extraLabel(spec), *spec.Block)
				}
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

// extraLabel is "<type> " for an Extra-kind spec, "" for the primary.
func extraLabel(spec DeviceSpec) string {
	if spec.Extra == "" {
		return ""
	}
	return string(spec.Extra) + " "
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
