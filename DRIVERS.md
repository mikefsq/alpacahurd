# Writing an alpacahurd driver

A driver is an ordinary public Go module. Users compile it into their herd by
adding one line to `hurd.conf`; nothing in this repo needs to change. Four
steps:

## 1. Write the hardware library

Talk to your device however it needs (USB, serial, TCP). Keep this free of any
Alpaca/ASCOM knowledge — it's just a Go library for the instrument. (See
`github.com/mikefsq/oasis-astro`, `optec`, `pegasus-astro` for examples.)

## 2. Write the Alpaca device

Implement the typed device interface for your ASCOM type from
`github.com/mikefsq/goalpaca/server` (`server.Focuser`, `server.Camera`, …) by
embedding `server.BaseDevice` and implementing the hardware-specific members.
The library enforces the device-independent ASCOM rules (validation, gating,
async semantics, image transport) before your methods run — drivers built on
it pass ConformU without protocol code.

Own your hardware lifecycle: construction must **not** touch hardware. Run an
acquire → monitor → re-acquire loop (start it from `Open`, the
`server.Hardware` interface) so the device is picked up whenever it's plugged
in and survives unplugs. `github.com/mikefsq/goalpaca-devices/oasisfw` is a
compact reference; the other modules there follow the same shape.

## 3. Register with the driver registry

Add a registration file (conventionally `hurd.go`) to the Alpaca device
package:

```go
package driver

import (
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

func init() {
	registry.Register(registry.Driver{
		Name:          "mywidget",                   // the config "driver" key
		Type:          alpacadev.FocuserType,
		Description:   "ACME MyWidget focuser",      // shown by alpacahurd -drivers
		ConfigExample: `{ "driver": "mywidget", "serial": "MW0001" }`,
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg struct {
				Serial string `json:"serial,omitempty"`
				Index  int    `json:"index,omitempty"`
			}
			if err := spec.Decode(&cfg); err != nil {
				return nil, err
			}
			d := NewMyWidget(cfg.Index, cfg.Serial)
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
	})
}
```

The rules:

- **`New` must not touch hardware.** It binds identity (serial, address,
  index); the acquire loop connects later. `alpacahurd -check` constructs
  every configured device, and users run it freely.
- **Decode your own config strictly.** `spec.Decode` hands you the entry with
  the engine-owned common keys stripped (`driver`, `name`, `enable`, `port`,
  `indi`, `lx200Port`, and the optics block — `registry.CommonKeys()`), and
  rejects unknown keys so user typos are reported. Don't name your fields
  after a common key; they'd be stripped before you see them.
- **`ConfigExample` is a complete JSON entry** for your driver, without
  `"port"` (the host injects one). It's what `alpacahurd -example` prints and
  what seeds `/etc/alpacahurd/hurd.json`, so make it copy-paste-ready.
- Prefer a stable identity binding (serial/address) over enumeration index,
  and document both in the example if you support both.

## 4. Publish and use

Push the module to a public repo. Users add it to `hurd.conf`:

```
github.com/you/mywidget-alpaca
```

and run `make`. Your driver appears in `alpacahurd -drivers`, its example in
`alpacahurd -example`, and its entries construct through `-check` like every
built-in.

## Optional: the INDI / LX200 front-ends

Devices are Alpaca-first; the extra front-ends are opt-in seams the engine
detects structurally:

- **Mounts**: implement `LiveMount() (lx200.Mount, error)` (return the mount
  only once acquired) and the device joins the INDI hub and gets an LX200
  bridge. Implement `UseOptics(server.OpticsStore)` to share one optics holder
  between Alpaca's `setoptics` Action and INDI's `TELESCOPE_INFO`.
- **Cameras**: implement `LiveCamera() (ccd.Camera, error)` from
  `github.com/mikefsq/goindi/ccd`, gated on connection. Also implement
  `ccd.GainController` / `ccd.OffsetController` / `ccd.Subframer` on your
  frame source if the hardware has them — INDI then advertises
  `CCD_CONTROLS`/`CCD_FRAME`. See `goalpaca-devices/astrocam/hurd.go` for the
  full pattern.

Skip this entirely for devices INDI can't represent; they're simply
Alpaca-only, and `-check` warns if a user sets `"indi": true` on one.
