# Writing an alpacahurd driver

A driver is an ordinary public Go module. A user compiles it into a herd by
adding one line to `hurd.conf`, and nothing in this repository changes. There
are four steps.

## 1. The hardware library

The hardware library communicates with the device however the device requires
(USB, serial, TCP). It holds no Alpaca or ASCOM knowledge; it is only a Go
library for the instrument. (See `github.com/mikefsq/oasis-astro`, `optec`, and
`pegasus-astro` for examples.)

## 2. The Alpaca device

The Alpaca device implements the typed device interface for its ASCOM type from
`github.com/mikefsq/goalpaca/server` (`server.Focuser`, `server.Camera`, …),
embedding `server.BaseDevice` and implementing the hardware-specific members.
The library enforces the device-independent ASCOM rules (validation, gating,
async semantics, image transport) before those methods run, so a driver built
on it passes ConformU without protocol code.

The device owns its hardware lifecycle, and construction must **not** touch
hardware. An acquire → monitor → re-acquire loop, started from `Open` (the
`server.Hardware` interface), picks the device up whenever it is plugged in and
keeps it working across unplugs. `github.com/mikefsq/goalpaca-devices/oasisfw`
is a compact reference, and the other modules there follow the same shape.

## 3. Registration with the driver registry

The Alpaca device package adds a registration file, conventionally `hurd.go`:

```go
package driver

import (
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// Config is the entry's driver-owned keys. The json tag names the key; the
// alpaca tag describes the setup-form control (see goalpaca-devices'
// SETUP_FORMS.md). Both fields select the hardware, so they apply at the next
// start and render read-only.
type Config struct {
	Serial string `json:"serial,omitempty" alpaca:"label=Serial,when=start"`
	Index  int    `json:"index,omitempty"  alpaca:"label=Enumeration index,min=0,when=start"`
}

func init() {
	registry.Register(registry.Driver{
		Name:          "mywidget",                   // the config "driver" key
		Type:          alpacadev.FocuserType,
		Description:   "ACME MyWidget focuser",      // shown by alpacahurd -drivers
		ConfigExample: `{ "driver": "mywidget", "serial": "MW0001" }`,
		Config:        func() any { return &Config{} },
		New: func(spec registry.Spec) (alpacadev.Device, error) {
			var cfg Config
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
  index), and the acquire loop connects later. `alpacahurd -check` constructs
  every configured device, and users run it freely. A reload constructs it
  again while the previous instance is being closed, so `Open` and `Close`
  (the `server.Hardware` interface) have to run cleanly in sequence in one
  process; a driver that grabs hardware in `New` cannot be reloaded.
- **The driver decodes its own config strictly.** `spec.Decode` returns the
  entry with the engine-owned common keys stripped (`driver`, `name`, `enable`,
  `port`, `device`, `lx200Port`, and the optics block — the full list is
  `registry.CommonKeys()`), and rejects unknown keys so that user typos are
  reported. A driver must not name its own fields after a common key, or they
  are stripped before its decode runs.
- **`Config` returns the driver's config struct.** The same tagged struct `New`
  decodes into, hoisted to a named type; `Config` returns a pointer to its zero
  value. The framework renders the driver's browser setup form from its `json`
  and `alpaca` tags with no form code in the driver, and delivers accepted live
  changes through the optional `Reconfigure(cfg any) error` method on the
  device. The tag grammar and the start-time versus live distinction are in
  goalpaca-devices' `SETUP_FORMS.md`. A driver with no driver-owned keys leaves
  `Config` nil and gets the "no configurable settings" page.
- **`ConfigExample` is a complete JSON entry** for the driver, without `"port"`
  (the host injects one). It is what `alpacahurd -example` prints, and it seeds
  `/etc/alpacahurd/hurd.json`, so it should be ready to copy into a config.
- A driver binds by a stable identity (serial or address) rather than
  enumeration index where the hardware allows, and documents both in the
  example when it supports both.

### Platform-specific drivers

A driver whose hardware exists only on some platforms declares that in its own
package with Go build constraints, not in `hurd.conf`: the registration file
carries the constraint (`hurd_linux.go`, or a `//go:build` line for a
combination), and the rest of the package keeps at least one unconstrained
file so a blank import compiles everywhere. `hurd.conf` then stays
platform-neutral: the fat build compiles the same list on every platform, and
each host's registry holds exactly the drivers that can work there. On a
foreign platform the driver is absent from `-drivers`, the add picker, and
`-example-devices`, and a device entry naming it is reported and skipped like
any unresolvable fragment — never a build break, never a driver that lists but
cannot run. smpro and asiair (`hurd_linux.go`: Linux SBC buses) are the
in-tree examples. A driver whose *code* only compiles on some platforms (a
vendor SDK, a cgo transport) is the same case with more files constrained; the
registration constraint is what keeps the story uniform.

## 4. Publishing

The module is pushed to a public repository, and a user adds it to `hurd.conf`:

```
github.com/example/mywidget-alpaca
```

After `make`, the driver appears in `alpacahurd -drivers`, its example in
`alpacahurd -example`, and its entries construct through `-check` like every
built-in.

## Optional: the LX200 front-end

Devices are Alpaca-first. A mount driver can also serve Stellarium and SkySafari
over Meade-LX200 by implementing `LiveMount() (lx200.Mount, error)`, which
returns the mount only once it is acquired; `lx200/bridge` runs a stateless
TCP server over it. The bridge is the mount binary's to host (alpacahurd no
longer runs one): the driver's own cmd wires it up from the entry's
`lx200Port` key, with the mount type and identity only the driver knows.

Non-mount devices, and mounts that do not implement `LiveMount`, remain
Alpaca-only.
