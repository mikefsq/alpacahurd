# Writing an alpacahurd driver

A driver is an ordinary public Go module with three parts. The hardware
library is the author's own; its interface is not prescribed. The Alpaca
device implements the `goalpaca/server` device contract. The registration
file implements the `goalpaca/registry` contract, which is the whole
interface with alpacahurd. A `cmd` directory of one file makes the module a
standalone binary. A user compiles the driver into a herd by adding one line
to `hurd.conf`, or installs the binary beside alpacahurd; nothing in this
repository changes either way.

## 1. The hardware library

The hardware library communicates with the device however the device requires
(USB, serial, TCP). It holds no Alpaca or ASCOM knowledge; it is only a Go
library for the instrument, and its API is the author's to design. (See
`github.com/mikefsq/oasis-astro`, `optec`, and `pegasus-astro` for examples.)

## 2. The Alpaca device: the goalpaca/server contract

The device satisfies two interfaces from `github.com/mikefsq/goalpaca/server`.

**The typed device interface** (`server.Focuser`, `server.Camera`,
`server.Telescope`, …) is `server.Device` plus the ASCOM members of that
type; `server/focuser.go` and its siblings list them. The driver implements
the hardware members only. The library enforces the device-independent ASCOM
rules (validation, gating, async semantics, image transport) before those
methods run, so a driver built on it passes ConformU without protocol code.

`server.Device` itself carries the identity members (`UniqueID`, `Name`,
`Description`, `DriverInfo`, `DriverVersion`, `InterfaceVersion`) and the
logical connection (`Connect`, `Disconnect`, `Connected`), which marks client
sessions and never touches hardware. Embedding `server.BaseDevice` supplies
all of them from its fields: set `ID` (a stable GUID; the hardware serial
works), `DevName`, `Desc`, `Info`, `Version`, and `IfaceVer` at construction.
`BaseDevice.Instance` holds the host's name for the device; its `Label`
method returns it for log lines, so the driver's messages and the host's
share one identifier.

**`server.Hardware`** is the hardware lifecycle:

```go
type Hardware interface {
	Open(ctx context.Context) error  // start the device's hardware loop
	Close(ctx context.Context) error // release on shutdown only
}
```

Construction must not touch hardware, and `Open` must not fail on absent
hardware: `Open` starts an acquire → monitor → re-acquire loop, so the device
is picked up whenever it is plugged in and keeps working across unplugs.
`github.com/mikefsq/goalpaca-devices/oasisfw` is a compact reference, and the
other modules there follow the same shape.

Two server interfaces are optional. `server.Busyable` makes the server reject
mutating requests while the device is in a transitory state (exposing,
moving); `Busy()` must be cheap. `server.Reconfigurable` receives accepted
live setting changes; see `Config` below.

## 3. Registration: the interface with alpacahurd

The registration file, conventionally `hurd.go`, registers a
`registry.Driver`. That struct is the whole contract: alpacahurd, the
standalone binary, and any other host drive the driver only through it.

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
			d.Instance = spec.Instance
			if spec.Name != "" {
				d.DevName = spec.Name
			}
			return d, nil
		},
	})
}
```

The host calls `New` once per device entry, and again on every reload.
`registry.Spec` carries everything the host knows about the entry:

| field | content |
|---|---|
| `Driver` | the entry's `"driver"` key, in the registered casing |
| `Name` | the entry's `"name"` display override; `""` when unset |
| `Instance` | the entry's identity (a `devices.d` file's stem); copy it to `BaseDevice.Instance` |
| `Raw` | the whole JSON entry, common keys included |
| `Device` | the ASCOM device number the host will register under, when known |

The rules:

- **`New` must not touch hardware.** It binds identity (serial, address,
  index), and the acquire loop connects later. `alpacahurd -check` constructs
  every configured device, and users run it freely. A reload constructs it
  again while the previous instance is being closed, so `Open` and `Close`
  have to run cleanly in sequence in one process; a driver that grabs
  hardware in `New` cannot be reloaded.
- **The driver decodes its own config strictly.** `spec.Decode` returns the
  entry with the engine-owned common keys stripped (`driver`, `name`, `enable`,
  `port`, `device`, `lx200Port`, and the optics block; `registry.CommonKeys()`
  is the full list), and rejects unknown keys so that user typos are
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
  (the host injects one). `alpacahurd -example` prints it and seeds
  `/etc/alpacahurd/hurd.json` from it, so it should be ready to copy into a
  config.
- A driver binds by a stable identity (serial or address) rather than
  enumeration index where the hardware allows, and documents both in the
  example when it supports both.

### Multi-device entries (MultiKey)

A driver that serves several devices of its type from one entry sets
`MultiKey` to the entry key holding an array of per-device blocks (astrocam's
`"cameras"`). The host expands such an entry into one `Spec` per block: the
block body becomes `Raw`, the block's position becomes `Device`, and a
block's `"name"` and `"enable"` are its own. A driver whose devices default
their hardware binding to their position reads `Spec.Device` for that
default. An entry without the key stays the flat one-device form.

### Platform-specific drivers

A driver whose hardware exists only on some platforms declares that in its own
package with Go build constraints, not in `hurd.conf`: the registration file
carries the constraint (`hurd_linux.go`, or a `//go:build` line for a
combination), and the rest of the package keeps at least one unconstrained
file so a blank import compiles everywhere. `hurd.conf` then stays
platform-neutral: the fat build compiles the same list on every platform, and
each host's registry holds the drivers that can work there. The driver is
absent from `-drivers`, the add picker, and `-example-devices` on a foreign
platform, and a device entry naming it there is reported and skipped like
any unresolvable fragment. smpro and asiair (`hurd_linux.go`: Linux SBC
buses) are the in-tree examples. A driver whose code only compiles on some
platforms (a vendor SDK, a cgo transport) is the same case with more files
constrained.

### Driver front-ends (the LX200 bridge)

Devices are Alpaca-first. A driver that also speaks another protocol supplies
`registry.Driver.FrontEnd`: every host calls it once per registered device
whose Alpaca server is up, so an entry means the same thing compiled into
alpacahurd and as a separate binary. The hook reads its own switch from the
entry and does nothing when it is absent. Its context is the device's: the
host cancels it when the device is disabled and calls the hook again when
the device comes back. The device getter resolves the current registration
(nil while there is none). The hosts argument names the addresses the
Alpaca server binds, which the front-end binds too. The full contract is on
the field's doc comment in `registry`.

The mount drivers use it for Meade-LX200 (Stellarium, SkySafari): the driver
implements `LiveMount() (lx200.Mount, error)`, returning the mount once it is
acquired, and its `FrontEnd` serves `lx200/bridge` on the entry's `lx200Port`
with the mount type and identity only the driver knows. Non-mount devices,
and mounts without `LiveMount`, remain Alpaca-only.

## 4. The driver binary

One file makes the module a standalone Alpaca server:

```go
package main

import (
	"github.com/mikefsq/goalpaca/devicemain"
	_ "example.com/mywidget-alpaca" // registers the driver
)

func main() { devicemain.Run("mywidget") }
```

`devicemain.Run` supplies everything that is not device-specific: the flag
set (`-config <device file>`, `-port`, one flag per `Config` key,
`-discovery direct|register|off`, `-check`, `-schema json|commented`), the
generated setup form, settings persistence under the state directory,
discovery, reload on SIGHUP, and the front-end call. The binary behaves the
same standalone and under alpacahurd, because both paths construct the device
through the same registry entry.

The same binary is what alpacahurd runs for a `devices.d` entry whose driver
is not compiled in. The supervisor's command line is `alpacahurd -launch
<instance>`, which resolves the entry and replaces itself with the driver
binary in register discovery mode, passing the entry's device file as
`-config`. Resolution finds the binary by the entry's `"exec"` path, by the
driver's name beside the `alpacahurd` binary, or by that name on `PATH`.

## 5. Publishing

The module is pushed to a public repository. A user deploys it either way:

- **Compiled in**: add the module path to `hurd.conf` and run `make`. The
  driver then appears in `alpacahurd -drivers`, its example in `alpacahurd
  -example`, and its entries construct through `-check` like every built-in.
- **Separate binary**: install the driver binary as `<driver>` beside
  `alpacahurd` (or on `PATH`, or name it with the entry's `"exec"` key). The
  orchestrator launches and supervises it per `devices.d` entry.

A compiled-in driver wins over an installed binary of the same name, so a
driver moves out of process by leaving `hurd.conf`.
