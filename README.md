# alpacahurd

A herd of open source [ASCOM Alpaca](https://ascom-standards.org/) astronomy 
device drivers in **one static binary, one config file**, built for the
low power mini pc at the telescope. Configure it your hardware once, and NINA, 
PHD2, Stellarium, or any other Alpaca client discovers every device over the 
network. Hotplug is handled automatically.

- **Typed, standard interfaces.** Every device is a standard ASCOM device with 
  the standard members. Clients need no per-vendor code.
- **Hotplug by design.** Every configured device runs its own
  acquire/monitor/re-acquire loop: start the herd on an empty bus, plug things
  in whenever, unplug one without disturbing the rest.
- **One discovery responder.** Clients auto-discover every device via UDP 32227
  (IPv4 broadcast + IPv6 multicast).
- **LX200 front-end included.** Each mount object can serve a Meade-LX200 over
  TCP for Stellarium and SkySafari.
- **Open-source drivers, chosen at build time.** `hurd.conf` lists the driver
  packages compiled in; anyone can publish a driver module (see
  [DRIVERS.md](DRIVERS.md)).

## Platforms

The targets are the low power mini pcs at the telescope:

- **Any mini-PC or SBC**, such as a Raspberry Pi or an N100 mini pc. 
  The whole herd compiles to one static arm64/amd64 binary that runs
  under systemd, uses udev for device access, and needs only a few MB of RAM.
- **Repurposed appliance hardware.** An ASIAIR or a StellarMate is a Raspberry
  Pi in a weatherproof case. Reflash it with stock Raspberry Pi OS (arm64) to
  get a first-class alpacahurd host that runs an all-open stack on the vendor's
  own hardware.
- **A Mac mini** at the scope runs the same appliance model, under launchd in
  place of systemd.

The USB/HID transports underneath the drivers are per-platform:

| Platform | Transport | Build |
|---|---|---|
| Linux | usbfs / hidraw / serial | plain `go build`, no C toolchain  |
| macOS | IOKit / IOUSBHost — **cgo** | plain `go build` with the Xcode command-line tools for cgo |
| Windows | WinUSB / HID / serial | plain `go build`, no C toolchain |

**Windows + ZWO cameras:** the drivers here contain no ZWO code, but Windows
gives a USB device to exactly one kernel driver, and the pure-Go transport
speaks the WinUSB user-space API — so the camera must be bound to the generic
`winusb.sys` (or libusbK) driver with [Zadig](https://zadig.akeo.ie/). ZWO's
own installer binds their proprietary `ASICAMUSB3.sys`, which WinUSB cannot
open. The bind is exclusive: while on WinUSB, ZWO's native software (ASIStudio,
their ASCOM driver) won't see the camera until you revert it in Device Manager.
Linux needs none of this — usbfs coexists with everything (just udev
permissions, which `make install` handles).

## Building from source

`make help` lists every target. The default `make` regenerates `drivers_gen.go`
from `hurd.conf` and builds `./alpacahurd` — no network access.

Not all library modules are tagged, so a development build needs track the
source through a Go workspace rather than the module proxy. Check out the sibling
repos next to this one (the set is listed as `WS_DIRS` in the `Makefile`) and
write a `go.work` over them once:

```sh
make workspace   # (re)writes go.work over whichever sibling checkouts it finds
make             # regenerate drivers_gen.go, then build
```

`make workspace` names any sibling it can't find, so clone that one and re-run.
Once the modules are published this step disappears and a plain `git clone &&
make` resolves everything from the proxy — the install sections below are written
for that end state, so run `make workspace` first until then.

## Install on Linux

Needs Go ≥ 1.23. Debian 13 Trixie ships go 1.24. If needed install the
official toolchain [go.dev/dl](https://go.dev/dl/)).

Then:

```sh
git clone https://github.com/mikefsq/alpacahurd
cd alpacahurd
nano hurd.conf        # optional: trim the driver list, or add third-party drivers
make workspace        # pre-release only — see "Building from source" above
make                  # regenerates the driver imports and builds ./alpacahurd
sudo make install     # binary + systemd service + udev rules + starter config
sudo nano /etc/alpacahurd/hurd.json    # enable YOUR devices (see below)
sudo systemctl restart alpacahurd
journalctl -u alpacahurd -f            # watch it acquire your hardware
```

The starter config seeded by `make install` lists **every compiled-in driver,
disabled**. Enable the entries for the hardware you own, fill in their serials
or addresses, and restart the service.

## Install on macOS

Needs Go ≥ 1.23 (`brew install go` or [go.dev/dl](https://go.dev/dl/)) and the
Xcode command-line tools (`xcode-select --install`) — the macOS USB/HID
transports use IOKit via cgo.

```sh
git clone https://github.com/mikefsq/alpacahurd
cd alpacahurd
nano hurd.conf        # optional: trim the driver list, or add third-party drivers
make workspace        # pre-release only — see "Building from source" above
make                  # builds with cgo automatically on Darwin
sudo make install     # launchd daemon (com.mikefsq.alpacahurd) + starter config
sudo nano /etc/alpacahurd/hurd.json
sudo launchctl kickstart -k system/com.mikefsq.alpacahurd
tail -f /var/log/alpacahurd.log
```

Same layout as Linux: binary in `/usr/local/bin`, config in `/etc/alpacahurd`,
restart-on-failure daemon (launchd instead of systemd). No udev equivalent is
needed — the daemon runs as root and IOKit handles device access.

## Install on Windows

Needs Go ≥ 1.23 ([go.dev/dl](https://go.dev/dl/)); no C toolchain. `make.ps1`
is the Windows counterpart of the Makefile (`.\make.ps1 help` lists the
targets). From PowerShell in the repo directory:

```powershell
.\make.ps1 workspace   # pre-release only: writes go.work over the sibling checkouts
.\make.ps1             # gen + build -> alpacahurd.exe
.\make.ps1 install     # elevated: install binary + config, startup task, firewall rule
```

`install` seeds `%ProgramData%\alpacahurd\hurd.json` (every driver, disabled),
validates it, registers a SYSTEM startup task that restarts on failure — the
Windows analogue of the systemd service — and opens the firewall for the binary.
Then edit the config and restart:

```powershell
notepad $env:ProgramData\alpacahurd\hurd.json
Restart-ScheduledTask -TaskName alpacahurd
```

If the script is blocked by execution policy, run it as
`powershell -ExecutionPolicy Bypass -File .\make.ps1 <target>`. Once the modules
are published the `workspace` step is unnecessary. Bind ZWO cameras to the
WinUSB driver with Zadig first (see [Platforms](#platforms) — ZWO's own driver
is not WinUSB-compatible).

## Configure devices

Which devices run is declared in a JSON config. Pass `-config <path>`
explicitly, or let it search (first found wins): `./hurd.json`, then
`~/.config/alpacahurd/hurd.json`, then `/etc/alpacahurd/hurd.json` (the
install target; the systemd unit passes it explicitly). `$ALPACAHURD_CONFIG`
overrides the search.

```json
{
  "discovery": "direct",
  "devices": [
    { "driver": "tenmicron", "port": 11110, "addr": "10.0.1.51:3492", "aperture": 200, "focalLength": 1600 },
    { "driver": "asicam",    "port": 11111, "serial": "1a2b3c4d", "name": "Main camera" },
    { "driver": "oasisfoc",  "port": 11120, "index": 0 },
    { "driver": "oasisfw",   "port": 11123, "index": 0, "enable": false }
  ]
}
```

Each entry is one device on its own Alpaca `"port"` (required and unique), and is
registered as device number 0 of its type on that port. `"enable": false` turns
an entry off without deleting it. Bind by `serial` or `addr` or by its discovery
`index` depending on the driver. 

The binary documents itself:

```sh
alpacahurd -drivers            # list the drivers compiled into this binary
alpacahurd -example            # print a full starter config (all drivers, disabled)
alpacahurd -example asicam     # print one driver's entry
alpacahurd -check              # validate the config without touching hardware
```

An example config (LX200, optics, weather→mount feed) is in
[`config/hurd.example.json`](config/hurd.example.json).

## Choosing drivers (hurd.conf)

[`hurd.conf`](hurd.conf) lists the driver packages compiled into the binary.
Append any third-party driver's public module path, and rebuild. `make` runs
`make gen` first, which regenerates the checked-in `drivers_gen.go` from the
file. To pin a driver to a specific version: `go get <module>@<version>`.

Drivers register themselves with `goalpaca/registry` at init. Writing one is a
small amount of glue over a hardware library; see [DRIVERS.md](DRIVERS.md).

## LX200 front-end (Stellarium, SkySafari)

Each mount object can also serve a Meade-LX200 TCP server. Set
`"lx200": { "enable": true, "basePort": 4030 }`. LX200 cannot multiplex, so every
mount gets its own port counting up from `basePort`, and a mount can pin one with
`"lx200Port": N` (which also enables LX200 for that mount alone).
`"readOnlySite": true` prevents an atlas from overwriting a modeled mount's
surveyed site and clock.

## Simulated devices

The `sim` module provides a full set of `sim-*` drivers. It is listed in 
`hurd.conf` by default, so every stock build can serve a complete
no-hardware herd for verifying an install and developing clients. Comment the
`sim` line out of `hurd.conf` to disable this.
[`config/hurd.sim.json`](config/hurd.sim.json) is the ready-made sim herd:

```sh
./alpacahurd -config config/hurd.sim.json
```

## Restricting interfaces, IPv6, logging

- `"listen": ["lo", "eth0"]` restricts every server (Alpaca, LX200, discovery)
  to those interfaces. An interface name serves both IP stacks; a bare IPv4
  literal is IPv4-only. Omit to bind everything.
- `"ipv6": false` turns off the IPv6 discovery responder (multicast group
  `ff12::a1:9aca`); IPv4 broadcast is unaffected.
- Everything logs to stderr → the journal. `"debug": true` adds per-request
  Alpaca logging; lifecycle lines (listening, device acquired/lost with reason,
  client connects) print regardless.

## USB permissions (udev)

USB/HID drivers need usbfs/hidraw access; the pure-Go ASI camera driver reads
the factory serial via a vendor control transfer (it's not a USB descriptor),
so serial binding needs read-write access to the device node.
`deploy/99-alpacahurd.rules` covers ZWO, PlayerOne, Astroasis, and
FTDI-serial devices; `make install` installs it (then replug). Add other
vendors' `idVendor` lines as you wire their drivers in.

## Running it yourself (no service)

```sh
go build -o alpacahurd .
./alpacahurd -config hurd.json
```

Linux and Windows binaries cross-compile from anywhere (pure Go):

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o alpacahurd .      # 64-bit Pi
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o alpacahurd.exe .
```

macOS binaries must be built on a Mac (the IOKit transports need cgo).

`sudo make uninstall` removes the service and binary on Linux (systemd + udev
rules) and macOS (launchd); the config in `/etc/alpacahurd` is kept.
