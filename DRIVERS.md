# Writing an alpacahurd driver

A driver is a Go package that implements a goalpaca device interface and
registers a `registry.Driver`. It can run as a standalone binary or be compiled
into alpacahurd. The hardware library and its API are yours to design.

The examples below use a focuser. See
[goalpaca-devices/oasisfw](https://github.com/mikefsq/goalpaca-devices/tree/main/oasisfw)
for an existing driver.

## Implement the device

Implement the appropriate interface from `github.com/mikefsq/goalpaca/server`,
such as `server.Focuser`, `server.Camera`, or `server.Telescope`. goalpaca
handles Alpaca requests, validation, and protocol responses; the driver
implements device behavior.

Embed `server.BaseDevice` for identity and logical connection methods. Set
`ID` to a stable device identifier, and fill in `DevName`, `Desc`, `Info`,
`Version`, and `IfaceVer`. Copy `registry.Spec.Instance` to `BaseDevice.Instance`
and use `Label()` in logs to identify the configured instance.

Implement `server.Hardware` for hardware access:

```go
type Hardware interface {
    Open(ctx context.Context) error
    Close(ctx context.Context) error
}
```

The constructor must not open hardware. `Open` should start a background loop
that acquires the device, monitors it, and retries after disconnection. Missing
hardware should not make `Open` fail. `Close` must stop the loop and release
resources, including during reload. Logical `Connect` and `Disconnect` calls
must not control hardware acquisition.

Optional interfaces include:

- `server.Busyable`: report transient states such as moving or exposing.
  Keep `Busy()` cheap; the server uses it to gate requests.
- `server.Reconfigurable`: apply accepted live configuration changes through
  `Reconfigure(cfg any) error`.

## Register the driver

Add a registration file, conventionally `hurd.go`. This example assumes
`NewMyWidget` constructs your device and embeds `server.BaseDevice`:

```go
package driver

import (
    "github.com/mikefsq/goalpaca/registry"
    "github.com/mikefsq/goalpaca/server"
)

// Config selects the focuser hardware.
type Config struct {
    Serial string `json:"serial,omitempty" alpaca:"label=Serial,when=start"`
    Index  int    `json:"index,omitempty" alpaca:"label=Enumeration index,min=0,when=start"`
}

func init() {
    registry.Register(registry.Driver{
        Name:          "mywidget",
        Type:          server.FocuserType,
        Description:   "ACME MyWidget focuser",
        ConfigExample: `{ "driver": "mywidget", "serial": "MW0001" }`,
        Config:        func() any { return &Config{} },
        New: func(spec registry.Spec) (server.Device, error) {
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

`Name` is the configuration's `driver` value. Use a distinct lowercase name.
`Type` must match the implemented device interface, and `Description` is shown
in driver listings.

The host calls `New` during startup, configuration checks, and reload.
Validate configuration there, but leave hardware access to `Open`.
Prefer a stable serial or address for hardware selection when available.

`registry.Spec` supplies:

| Field | Meaning |
|---|---|
| `Driver` | Registered driver name |
| `Name` | Optional display-name override |
| `Instance` | Host-assigned identity, normally the device filename stem |
| `Raw` | Complete JSON entry, including common keys |
| `Device` | Device number when known during construction; zero otherwise |

### Configuration and setup forms

Use `spec.Decode` to decode driver settings. It removes common keys and rejects
unknown driver keys. Do not reuse names from `registry.CommonKeys()`, which
includes `driver`, `name`, `enable`, `port`, `device`, `exec`, and protocol and
optics settings.

`Config` returns a pointer to the zero value of the same struct decoded by
`New`. Its `json` tags name configuration keys; `alpaca` tags describe setup
controls, validation, and when settings apply. For the tag syntax, see
[SETUP_FORMS.md](https://github.com/mikefsq/goalpaca-devices/blob/main/SETUP_FORMS.md).
Use `when=start` for hardware selection and implement `Reconfigure` for live
settings. The host locks fields supplied by the admin configuration and
persists editable settings.

Leave `Config` nil if there are no driver settings. A device can also supply
its own form through `server.Configurable`.

`ConfigExample` must be a valid JSON object containing `driver` and example
driver settings. Omit `port`; the host supplies it for
`alpacahurd -example <driver>`. The installer uses `-example` for server settings
and `-example-devices <directory>` for disabled driver templates.

### Multiple devices per entry

Set `MultiKey` to the array key holding per-device configurations, such as
astrocam's `cameras`. Each block becomes a separate `Spec`: its body is `Raw`,
its position is `Device`, and its `name` and `enable` apply to that device.
Disabling the parent entry disables every block. Put device-specific front-end
settings inside each block.

A flat entry with driver settings constructs one device. In alpacahurd, an
empty multi-device entry or an empty array defaults to two empty blocks.
If enumeration position is a supported hardware default, read `Spec.Device`
when no explicit selector is supplied.

### Additional protocols

Use `registry.Driver.FrontEnd` to start an additional protocol server, such as
an LX200 bridge. The hook receives a context, a device getter, the entry JSON,
and the Alpaca server's bind addresses.

- Return promptly; run servers in goroutines that stop when the context ends.
- Resolve the device through the getter on each use and handle nil during reload.
- Bind the supplied addresses; an empty list means all interfaces.
- Return without starting anything when the protocol is disabled in the entry.

The host starts the hook after the Alpaca server binds and cancels it when the
device is disabled or the host stops. A front-end error is reported without
stopping the Alpaca device. See the `FrontEnd` field in `goalpaca/registry`
for the full contract and the mount drivers for LX200 examples.

### Platform support

Use Go build constraints in the driver package for platform-specific code.
For example, put Linux registration in `hurd_linux.go`. Keep an unconstrained
file so the package can still be blank-imported on other platforms without
registering the driver there. Apply constraints to all files that depend on
platform-specific APIs or SDKs.

This lets `hurd.conf` use one package list across platforms. A driver that is
not registered on a platform is omitted from listings and templates; entries
that cannot resolve to a binary are reported and skipped.

## Build a standalone binary

Create `cmd/mywidget/main.go`:

```go
package main

import (
    "github.com/mikefsq/goalpaca/devicemain"
    _ "example.com/mywidget-alpaca"
)

func main() { devicemain.Run("mywidget") }
```

Replace the example import with your package path, then build:

```sh
go build -o mywidget ./cmd/mywidget
./mywidget -schema commented
```

`devicemain.Run` provides configuration flags, generated setup forms, state
persistence, discovery, SIGHUP reload on Unix, and front-end startup. Its flags
include `-config`, `-port`, driver settings, `-check`,
`-schema json|commented`, and `-discovery direct|register|off`.

Install the binary under its registered driver name beside alpacahurd or on
`PATH`. Alternatively, set `exec` in the device file to its executable path.
alpacahurd launches it with that file as `-config` and discovery in register
mode through the platform service manager.

## Compile into alpacahurd

Publish the driver as a Go module. In an alpacahurd checkout, add its package
import path to `hurd.conf`, one path per line. Add the module dependency and build:

```sh
go get example.com/mywidget-alpaca@v0.1.0
make fat
./alpacahurd -drivers
./alpacahurd -example mywidget
```

`make fat` regenerates `drivers_gen.go` and builds with the `fat` tag.
`make build` excludes that file. Use `make deps` to download the recorded
release versions. To update the bundled drivers, use
`go get github.com/mikefsq/goalpaca-devices@<release-tag>` and then `make tidy`.
All bundled driver packages share the root module version.

A compiled-in driver takes precedence over an installed binary with the same
name. To run it as a separate process, remove its import from `hurd.conf` and
rebuild with `make fat`, or use `make build`.

## Check the integration

Confirm that configuration checks succeed without attached hardware and reject
misspelled or invalid driver settings. With hardware attached, exercise
acquisition, unplug/replug, shutdown, and reload in both standalone and
compiled-in builds. Check that setup changes persist and admin-supplied fields
remain locked. Run ConformU against the resulting device to verify its ASCOM
behavior.
