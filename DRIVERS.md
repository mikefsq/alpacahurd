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
  index), and the acquire loop connects later. `alpacahurd -check` constructs
  every configured device, and users run it freely.
- **The driver decodes its own config strictly.** `spec.Decode` returns the
  entry with the engine-owned common keys stripped (`driver`, `name`, `enable`,
  `port`, `lx200Port`, and the optics block — the full list is
  `registry.CommonKeys()`), and rejects unknown keys so that user typos are
  reported. A driver must not name its own fields after a common key, or they
  are stripped before its decode runs.
- **`ConfigExample` is a complete JSON entry** for the driver, without `"port"`
  (the host injects one). It is what `alpacahurd -example` prints, and it seeds
  `/etc/alpacahurd/hurd.json`, so it should be ready to copy into a config.
- A driver binds by a stable identity (serial or address) rather than
  enumeration index where the hardware allows, and documents both in the
  example when it supports both.

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
over Meade-LX200 by implementing an optional interface that the engine detects:
`LiveMount() (lx200.Mount, error)`, which returns the mount only once it is
acquired. The engine then runs an LX200 bridge in front of it. A driver that
implements `UseOptics(server.OpticsStore)` shares one optics holder between
Alpaca's `setoptics` Action and the reported aperture and focal length.

Non-mount devices, and mounts that do not implement `LiveMount`, remain
Alpaca-only.
