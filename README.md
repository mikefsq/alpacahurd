# alpacahurd

A herd of open source [ASCOM Alpaca](https://ascom-standards.org/) astronomy
device drivers, built for the low power mini pc at the telescope. Drivers run
compiled into one static binary or as separate binaries under the platform
supervisor; both layouts are supported and may be mixed. Configure your
hardware once, and NINA, PHD2, Stellarium, or any other Alpaca client discovers
every device over the network. Hotplug is handled automatically.

- **Typed, standard interfaces.** Every device is a standard ASCOM device with 
  the standard members. Clients need no per-vendor code.
- **Hotplug by design.** Every configured device runs its own
  acquire/monitor/re-acquire loop: start the herd on an empty bus, plug things
  in whenever, unplug one without disturbing the rest.
- **One discovery responder.** Clients auto-discover every device via UDP 32227
  (IPv4 broadcast + IPv6 multicast). Devices running as separate binaries, on
  this host or another, register with it and are discovered through it too.
- **Browser setup pages.** Every device has a configuration page at
  `/setup/v1/{type}/{n}/setup`, generated from the driver's config struct.
  Keys the config file names render locked; what the page changes persists to
  a state file and survives restart.
- **Open-source drivers.** `hurd.conf` lists the driver packages compiled in;
  anyone can publish a driver module (see [DRIVERS.md](DRIVERS.md)).

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

Which devices run is declared in JSON. The server config `hurd.json` holds the
shared blocks (`discovery`, `listen`), and a `devices.d`
directory beside it holds one file per device. Pass `-config <path>`
explicitly, or let it search (first found wins): `./hurd.json`, then the
platform config directory (`~/.config/alpacahurd` for a user,
`/etc/alpacahurd` under the service on Linux; see the table below).
`$ALPACAHURD_CONFIG` overrides the search.

```
/etc/alpacahurd/hurd.json                  server blocks
/etc/alpacahurd/devices.d/main-camera.json one device
/etc/alpacahurd/devices.d/mount.json       another
```

A device file is one JSON object. The filename is the device's identity and
names its state file:

```json
{ "driver": "astrocam", "port": 11111, "device": 0, "serial": "1a2b3c4d", "name": "Main camera" }
```

Every entry is one device on an Alpaca `"port"` (required). `"enable": false`
turns an entry off without deleting it. Bind by `serial` or `addr` or by its
discovery `index` depending on the driver.

Pin `serial`, `port`, and `device`. Those three fix a client's connect string:
`serial` selects the hardware whatever the enumeration order, `port` the
address, and `device` the number within that port. Left unpinned, a replug or a
disabled entry can move a device.

The inline `"devices"` array in `hurd.json` still works and is loaded first;
`devices.d` is the newer layout and the one the install scripts seed.

### The setup page and the state directory

Each device has a browser configuration page at
`/setup/v1/{type}/{number}/setup`, generated from the driver's config struct.
Every key the device file names renders locked there, with the file named; the
page changes only what the file left unset. What the page changes is written to
a state file of the same name under the state directory, and read back over the
device file at the next start. The admin file always wins, with one
exception: `enable`, which the orchestrator page's enable and disable buttons
write to the state file, wins from there, since the seeded file's `enable:
false` is the installer's default rather than a decision.

| role | Linux | macOS | Windows |
|---|---|---|---|
| config | `/etc/alpacahurd/` | `/Library/Application Support/alpacahurd/` | `%ProgramData%\alpacahurd\` |
| state | `/var/lib/alpacahurd/devices/` | `…/alpacahurd/state/devices/` | `…\alpacahurd\state\devices\` |
| logs | journal | `/Library/Logs/alpacahurd/` | `…\alpacahurd\logs\` |

An interactive run uses the per-user equivalents. `$ALPACA_CONFIG_DIR` and
`$ALPACA_STATE_DIR` override either.

### Reload without a restart

A device file edit takes effect on a reload: the device is rebuilt from the
file and its state overlay, its hardware closed and reopened, and its port
kept, while the other devices carry on. The Reload button on a device's setup
page and on the orchestrator page (`http://host:32227/setup`) does it per
device; `systemctl reload alpacahurd` or `kill -HUP` does it for the whole
herd. A port change or a driver change still needs a restart.

Enable and disable act without a restart too, from the orchestrator page: a
disabled entry is constructed and served on its port (a new server is started
for a new port), an enabled one has its hardware closed and is removed from
its port; a separate binary is started or stopped through the supervisor. The
switch is recorded in the entry's state file, so the next start agrees. A
device the add form writes appears in the table at once, disabled; its `edit`
link opens the device file itself in the page (JSON with comments, checked
before it is written), so the keys it needs are set there and the row enabled,
all without leaving the browser. A running device picks up an edit on reload.

### Several devices on one port

Entries naming the same `"port"` share one Alpaca server and are numbered 0, 1, …
in config order. Numbering is per ASCOM type, since the URL is
`/api/v1/{type}/{number}/`: two cameras on a port are `camera/0` and `camera/1`,
and a focuser beside them is still `focuser/0`.

Separate ports remain the default, and are the better layout for anything you
restart or replug independently. Reach for a shared port when a client shows only
one server per address. ZWO's ASIStudio lists a single Alpaca entry per IP, so
two cameras must share a port for it to offer both.

Pin a number with `"device": N` once clients have stored device URLs; otherwise
disabling an entry renumbers the ones after it. Pinning a number an earlier entry
already took is a config error rather than a silent reshuffle, so pin ascending or
pin none. `alpacahurd -check` prints the resolved `type/number` for every device.

A `devices.d` file naming a driver that is not compiled in is reported by
`-check` and skipped at start rather than treated as fatal, since removing a
driver package can leave its file behind.

The binary documents itself:

```sh
alpacahurd -drivers                        # list the drivers compiled into this binary
alpacahurd -example                        # print the server config (no devices)
alpacahurd -example astrocam               # print one driver's device file
alpacahurd -example-devices /etc/alpacahurd/devices.d   # seed one disabled file per driver
alpacahurd -check                          # validate the config without touching hardware
```

An example server config is in
[`config/hurd.example.json`](config/hurd.example.json). Device files are not
kept here: each driver carries its own example and schema in goalpaca-devices,
and `alpacahurd -example-devices <dir>` writes one commented file per compiled-in
driver from them (`alpacahurd -example <driver>` prints one entry).

## Choosing drivers (hurd.conf)

[`hurd.conf`](hurd.conf) lists the driver packages compiled into the binary.
Append any third-party driver's public module path, and rebuild. `make` runs
`make gen` first, which regenerates the checked-in `drivers_gen.go` from the
file. To pin a driver to a specific version: `go get <module>@<version>`.

Drivers register themselves with `goalpaca/registry` at init. Writing one is a
small amount of glue over a hardware library; see [DRIVERS.md](DRIVERS.md).

## Drivers as separate binaries

A driver need not be compiled in. A `devices.d` entry whose driver is not in
the binary resolves to an installed one: the entry's `"exec"` path, a binary
named for the driver beside `alpacahurd`, or one on `PATH`. The orchestrator
runs it through the platform supervisor as `alpacahurd -launch <instance>`,
which resolves the entry and replaces itself with the driver binary in
register discovery mode, so the orchestrator answers discovery for it and the
supervisor's record never names the driver:

- Linux: the template unit `alpacahurd-device@.service` (installed by
  `install.sh`), one instance per device file, `PartOf=alpacahurd.service`,
  restart on failure, `journalctl -u alpacahurd-device@<instance>` for logs.
- macOS: one plist per device under `/Library/LaunchDaemons`, written by the
  orchestrator, `KeepAlive` on failure, logs in `/Library/Logs/alpacahurd/`.
- Windows: one service per device (`alpacahurd-<instance>`) with recovery
  options; the launched `alpacahurd` stays as the service host and runs the
  driver as its child, logs under `%ProgramData%\alpacahurd\logs\`.

The orchestrator page starts, stops, restarts, enables, disables, and shows the
logs of each; a compiled-in driver always wins over a binary of the same name,
so a driver moves out of process by leaving `hurd.conf`. Without a supervisor
(a hand run, or a platform without one) such entries are reported and skipped;
`alpacahurd -launch <instance>` runs one from a console.

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

- `"listen": ["lo", "eth0"]` restricts every server (Alpaca, discovery)
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
