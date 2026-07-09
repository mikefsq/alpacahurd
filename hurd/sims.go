package hurd

import (
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goalpaca/sim"
)

// init registers the simulated devices — one per ASCOM type — alongside the
// hardware drivers. They are always compiled in: testability is how this
// ecosystem stays verifiably reliable on installed machines, so every binary
// can serve a full no-hardware herd (see config/hurd.sim.json) for client
// development and installation checks.
//
// sim-telescope and sim-camera also drive the INDI/LX200 front-ends (via the
// adapters in simmount.go / simcamera.go), sharing one simulated sky so PHD2
// can run a closed guide loop against them. The rest are Alpaca-only.
func init() {
	// Every sim decodes an empty driver-config so a stray driver-owned key in a
	// sim entry is still reported as a typo.
	simNew := func(mk func(spec registry.Spec) alpacadev.Device) func(registry.Spec) (alpacadev.Device, error) {
		return func(spec registry.Spec) (alpacadev.Device, error) {
			if err := spec.Decode(&struct{}{}); err != nil {
				return nil, err
			}
			return mk(spec), nil
		}
	}
	setName := func(spec registry.Spec, name *string) {
		if spec.Name != "" {
			*name = spec.Name
		}
	}

	registry.Register(registry.Driver{
		Name: "sim-telescope", Type: alpacadev.TelescopeType,
		Description:   "simulated mount (Alpaca + INDI + LX200)",
		ConfigExample: `{ "driver": "sim-telescope", "name": "Sim Mount", "aperture": 130, "focalLength": 1000, "indi": true }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			// Backed by a lx200.Mount adapter, so the sim also drives the INDI/LX200
			// front-ends — not just Alpaca (unlike most other sim-* devices).
			return newSimMount(spec.Name)
		}),
	})
	registry.Register(registry.Driver{
		Name: "sim-camera", Type: alpacadev.CameraType,
		Description:   "simulated camera (Alpaca + INDI guide camera)",
		ConfigExample: `{ "driver": "sim-camera", "name": "Sim Guide Camera", "indi": true }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			// Backed by a ccd.Camera adapter, so the sim camera also drives the INDI
			// CCD device — PHD2 can use it as a guide camera, not just Alpaca.
			return newSimCamera(spec.Name)
		}),
	})
	registry.Register(registry.Driver{
		Name: "sim-focuser", Type: alpacadev.FocuserType,
		Description:   "simulated focuser",
		ConfigExample: `{ "driver": "sim-focuser", "name": "Sim Focuser" }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			d := sim.NewFocuser()
			setName(spec, &d.DevName)
			return d
		}),
	})
	registry.Register(registry.Driver{
		Name: "sim-filterwheel", Type: alpacadev.FilterWheelType,
		Description:   "simulated filter wheel",
		ConfigExample: `{ "driver": "sim-filterwheel", "name": "Sim Filter Wheel" }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			d := sim.NewFilterWheel()
			setName(spec, &d.DevName)
			return d
		}),
	})
	registry.Register(registry.Driver{
		Name: "sim-rotator", Type: alpacadev.RotatorType,
		Description:   "simulated rotator",
		ConfigExample: `{ "driver": "sim-rotator", "name": "Sim Rotator" }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			d := sim.NewRotator()
			setName(spec, &d.DevName)
			return d
		}),
	})
	registry.Register(registry.Driver{
		Name: "sim-switch", Type: alpacadev.SwitchType,
		Description:   "simulated switch bank",
		ConfigExample: `{ "driver": "sim-switch", "name": "Sim Switch" }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			d := sim.NewSwitch()
			setName(spec, &d.DevName)
			return d
		}),
	})
	registry.Register(registry.Driver{
		Name: "sim-dome", Type: alpacadev.DomeType,
		Description:   "simulated dome",
		ConfigExample: `{ "driver": "sim-dome", "name": "Sim Dome" }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			d := sim.NewDome()
			setName(spec, &d.DevName)
			return d
		}),
	})
	registry.Register(registry.Driver{
		Name: "sim-covercalibrator", Type: alpacadev.CoverCalibratorType,
		Description:   "simulated cover / flat panel",
		ConfigExample: `{ "driver": "sim-covercalibrator", "name": "Sim Flat Panel" }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			d := sim.NewCoverCalibrator()
			setName(spec, &d.DevName)
			return d
		}),
	})
	registry.Register(registry.Driver{
		Name: "sim-observingconditions", Type: alpacadev.ObservingConditionsType,
		Description:   "simulated weather station",
		ConfigExample: `{ "driver": "sim-observingconditions", "name": "Sim Weather" }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			d := sim.NewObservingConditions()
			setName(spec, &d.DevName)
			return d
		}),
	})
	registry.Register(registry.Driver{
		Name: "sim-safetymonitor", Type: alpacadev.SafetyMonitorType,
		Description:   "simulated safety monitor",
		ConfigExample: `{ "driver": "sim-safetymonitor", "name": "Sim Safety" }`,
		New: simNew(func(spec registry.Spec) alpacadev.Device {
			d := sim.NewSafetyMonitor()
			setName(spec, &d.DevName)
			return d
		}),
	})
}
