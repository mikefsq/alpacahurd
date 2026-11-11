package hurd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mikefsq/goalpaca/registry"
)

// multi-widget is a test driver with a MultiKey, delegating construction to
// the sim focuser: one entry may carry a "units" array, one device per block.
func init() {
	sim, ok := registry.Lookup("sim-focuser")
	if !ok {
		panic("sim-focuser is not registered")
	}
	registry.Register(registry.Driver{
		Name:          "multi-widget",
		Type:          sim.Type,
		Description:   "test multi-device driver",
		ConfigExample: `{ "driver": "multi-widget" }`,
		MultiKey:      "units",
		Config:        sim.Config,
		New:           sim.New,
	})
}

func TestSubSpecs(t *testing.T) {
	subs, err := subSpecs(parseSpec(t, `{"driver":"multi-widget","port":11200,"units":[{"name":"a"},{"enable":false},{}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 3 {
		t.Fatalf("blocks = %d, want 3", len(subs))
	}
	if subs[0].Name != "a" || *subs[0].Device != 0 || !subs[0].enabled() {
		t.Errorf("block 0: name %q device %d enabled %v", subs[0].Name, *subs[0].Device, subs[0].enabled())
	}
	if subs[1].enabled() || *subs[1].Device != 1 {
		t.Errorf("block 1 should be disabled device 1")
	}
	if *subs[2].Device != 2 || subs[2].Block == nil || *subs[2].Block != 2 {
		t.Errorf("block 2 should pin device and block 2")
	}

	// A flat entry (driver keys at top level) is itself, Block nil.
	flat, err := subSpecs(parseSpec(t, `{"driver":"multi-widget","port":11200,"somekey":1}`))
	if err != nil || len(flat) != 1 || flat[0].Block != nil {
		t.Fatalf("flat entry should stay one un-expanded spec: %v %v", flat, err)
	}

	// A bare entry defaults to two blocks, devices 0 and 1.
	def, err := subSpecs(parseSpec(t, `{"driver":"multi-widget","port":11200}`))
	if err != nil || len(def) != 2 || *def[0].Device != 0 || *def[1].Device != 1 {
		t.Fatalf("bare MultiKey entry should default to two blocks: %v %v", def, err)
	}

	// A disabled entry disables every block.
	off, err := subSpecs(parseSpec(t, `{"driver":"multi-widget","port":11200,"enable":false,"units":[{},{}]}`))
	if err != nil || off[0].enabled() || off[1].enabled() {
		t.Fatalf("a disabled entry should disable its blocks: %v %v", off, err)
	}
}

func TestCheckConfigMultiDeviceEntry(t *testing.T) {
	cfg := &Config{Devices: []DeviceSpec{
		parseSpec(t, `{"driver":"multi-widget","port":11200,"units":[{"name":"first"},{"enable":false},{"name":"third"}]}`),
	}}
	var out bytes.Buffer
	fatal, errs := checkConfig(&out, cfg)
	if fatal != 0 || errs != 0 {
		t.Fatalf("fatal, errs = %d, %d, want 0, 0:\n%s", fatal, errs, out.String())
	}
	for _, needle := range []string{"focuser/0 on port 11200", "device 1 disabled", "focuser/2 on port 11200"} {
		if !strings.Contains(out.String(), needle) {
			t.Errorf("checkConfig output missing %q:\n%s", needle, out.String())
		}
	}
}

// extra-widget is a test driver with an Extra kind: a sim focuser that is also a
// sim switch, one switch per "switches" block, one by default.
func init() {
	foc, _ := registry.Lookup("sim-focuser")
	sw, ok := registry.Lookup("sim-switch")
	if !ok {
		panic("sim-switch is not registered")
	}
	registry.Register(registry.Driver{
		Name:          "extra-widget",
		Type:          foc.Type,
		Description:   "test driver with an Extra kind",
		ConfigExample: `{ "driver": "extra-widget" }`,
		Config:        foc.Config,
		New:           foc.New,
		Extra:         []registry.Kind{{Type: sw.Type, MultiKey: "switches", New: sw.New, Config: sw.Config, Default: true}},
	})
}

func TestSubSpecsExtraKind(t *testing.T) {
	// No array: the primary flat, plus the Default extra kind's one block.
	subs, err := subSpecs(parseSpec(t, `{"driver":"extra-widget","port":11201}`))
	if err != nil || len(subs) != 2 {
		t.Fatalf("subs = %v, %v; want primary plus one switch", subs, err)
	}
	if subs[0].Extra != "" || subs[0].Block != nil {
		t.Errorf("primary should stay flat: extra %q block %v", subs[0].Extra, subs[0].Block)
	}
	if subs[1].Extra != "switch" || *subs[1].Device != 0 || *subs[1].Block != 0 || !subs[1].enabled() {
		t.Errorf("default switch block: extra %q device %d block %d", subs[1].Extra, *subs[1].Device, *subs[1].Block)
	}

	// An array: one switch per block, pinned to its position, own name and enable.
	subs, err = subSpecs(parseSpec(t, `{"driver":"extra-widget","port":11201,"switches":[{"name":"s"},{"enable":false}]}`))
	if err != nil || len(subs) != 3 {
		t.Fatalf("subs = %v, %v; want primary plus two switches", subs, err)
	}
	if subs[1].Name != "s" || *subs[1].Device != 0 || !subs[1].enabled() {
		t.Errorf("switch 0: name %q device %d", subs[1].Name, *subs[1].Device)
	}
	if subs[2].enabled() || *subs[2].Device != 1 {
		t.Errorf("switch 1 should be disabled device 1")
	}

	// An entry without Extra kinds expands as before.
	plain, err := subSpecs(parseSpec(t, `{"driver":"sim-focuser","port":11201}`))
	if err != nil || len(plain) != 1 || plain[0].Block != nil || plain[0].Extra != "" {
		t.Fatalf("plain entry should stay one flat spec: %v %v", plain, err)
	}
}

func TestCheckConfigExtraKind(t *testing.T) {
	cfg := &Config{Devices: []DeviceSpec{
		parseSpec(t, `{"driver":"extra-widget","port":11201,"switches":[{},{"enable":false},{}]}`),
	}}
	var out bytes.Buffer
	fatal, errs := checkConfig(&out, cfg)
	if fatal != 0 || errs != 0 {
		t.Fatalf("fatal, errs = %d, %d, want 0, 0:\n%s", fatal, errs, out.String())
	}
	for _, needle := range []string{"focuser/0 on port 11201", "switch/0 on port 11201", "switch device 1 disabled", "switch/2 on port 11201"} {
		if !strings.Contains(out.String(), needle) {
			t.Errorf("checkConfig output missing %q:\n%s", needle, out.String())
		}
	}
}
