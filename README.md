# alpacahurd

A herd of [ASCOM Alpaca](https://ascom-standards.org/) astronomy device drivers
in **one binary, one process, one config file** — built for the small, silent
box at the telescope. Point it at your hardware once; NINA, PHD2, Stellarium,
or any Alpaca client then finds every device over the network, with hotplug
handled for you.

(Yes, *hurd*: it's a herd of alpaca daemons. Not a typo.)

- **Typed, standard interfaces.** Every device is a standard ASCOM device
  (telescope, camera, focuser, …) with the standard members. Clients need no
  per-vendor code.
- **Hotplug by design.** Every configured device runs its own
  acquire/monitor/re-acquire loop: start the herd on an empty bus, plug things
  in whenever, unplug one without disturbing the rest.
- **One discovery responder.** Clients auto-discover every device via UDP 32227
  (IPv4 broadcast + IPv6 multicast).
- **Extra front-ends included.** The same device objects can also serve INDI
  (for PHD2) and LX200 over TCP (for Stellarium/SkySafari) — no
  `indiserver`, no translation layers.
- **Open-source drivers, chosen at build time.** `hurd.conf` lists the driver
  packages compiled in; anyone can publish a driver module (see
  [DRIVERS.md](DRIVERS.md)).

## Platforms

The targets are the machines that live at the telescope:

- **Any Linux mini-PC or SBC** — a Raspberry Pi (CM5/5/4), an N100-class box,
  whatever you have. One static arm64/amd64 binary, systemd, udev; a few MB of
  RAM for the whole herd.
- **Repurposed appliance hardware** — an ASIAIR or a StellarMate is a Raspberry
  Pi in a nice weatherproof case. Reflash it with stock Raspberry Pi OS
  (arm64) and it's a first-class alpacahurd host running an all-open stack on
  the vendor's own hardware.
- **A Mac mini** at the scope — same appliance model, launchd instead of
  systemd.

**Windows** works too (alpacahurd + NINA on one box is a fine setup), but it
isn't *necessary* there the way it is on Linux/macOS: ASCOM ships its own
orchestration and simulators, and every vendor ships a Windows installer. The
reason to run alpacahurd on Windows is wanting an open-source driver stack.
Otherwise, a Windows machine is typically the network client of a herd
running elsewhere.

The USB/HID transports underneath the drivers are per-platform:

| Platform | Transport | Build |
|---|---|---|
| Linux (incl. Raspberry Pi) | usbfs / hidraw / serial — pure Go | `CGO_ENABLED=0`, static binary, easy cross-compile |
| macOS | IOKit / IOUSBHost — **cgo** | build natively on a Mac with the Xcode command-line tools |
| Windows | WinUSB / HID / serial — pure Go | plain `go build`, no C toolchain |

The Linux and macOS paths are hardware-validated; the Windows transports are
compile-checked mirrors of the Linux ones and want a hardware shakedown —
reports welcome. Networked mounts (e.g. `tenmicron`) and the `sim-*` devices
are plain TCP and identical everywhere.

**Windows + ZWO cameras:** the drivers here contain no ZWO code, but Windows
gives a USB device to exactly one kernel driver, and the pure-Go transport
speaks the WinUSB user-space API — so the camera must be bound to the generic
`winusb.sys` (or libusbK) driver with [Zadig](https://zadig.akeo.ie/). ZWO's
own installer binds their proprietary `ASICAMUSB3.sys`, which WinUSB cannot
open. The bind is exclusive: while on WinUSB, ZWO's native software (ASIStudio,
their ASCOM driver) won't see the camera until you revert it in Device Manager.
Linux needs none of this — usbfs coexists with everything (just udev
permissions, which `make install` handles).

## Install on Linux (Raspberry Pi, mini-PC, reflashed ASIAIR/StellarMate)

Needs Go ≥ 1.25. Distro apt versions are usually too old — install the
official toolchain once (swap `arm64` for `amd64` on an x86 mini-PC):

```sh
curl -LO https://go.dev/dl/go1.25.0.linux-arm64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.25.0.linux-arm64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.profile && source ~/.profile
```

Then:

```sh
git clone https://github.com/mikefsq/alpacahurd
cd alpacahurd
nano hurd.conf        # optional: trim the driver list, or add third-party drivers
make                  # regenerates the driver imports and builds ./alpacahurd
sudo make install     # binary + systemd service + udev rules + starter config
sudo nano /etc/alpacahurd/hurd.json    # enable YOUR devices (see below)
sudo systemctl restart alpacahurd
journalctl -u alpacahurd -f            # watch it acquire your hardware
```

The starter config seeded by `make install` lists **every compiled-in driver,
disabled**. Enable the entries you own, fill in serials/addresses, restart.
That's the whole deployment.

## Install on macOS

Needs Go ≥ 1.25 (`brew install go` or [go.dev/dl](https://go.dev/dl/)) and the
Xcode command-line tools (`xcode-select --install`) — the macOS USB/HID
transports use IOKit via cgo.

```sh
git clone https://github.com/mikefsq/alpacahurd
cd alpacahurd
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

Needs Go ≥ 1.25 ([go.dev/dl](https://go.dev/dl/)); no C toolchain, no make.
From PowerShell or cmd in the repo directory:

```powershell
go run ./internal/gendrivers      # only needed if you edited hurd.conf
go mod tidy
go build -o alpacahurd.exe .
.\alpacahurd.exe -example > hurd.json    # then edit: enable your devices
.\alpacahurd.exe -check
.\alpacahurd.exe                          # finds .\hurd.json automatically
```

Allow it through Windows Firewall when prompted (Alpaca HTTP on your device
ports + UDP 32227 discovery). To start it at boot without a logged-in session,
register a scheduled task (run as an elevated prompt):

```powershell
schtasks /Create /TN alpacahurd /SC ONSTART /RU SYSTEM ^
  /TR "C:\path\to\alpacahurd.exe -config C:\path\to\hurd.json"
```

Bind ZWO cameras to the WinUSB driver with Zadig first (see
[Platforms](#platforms) — ZWO's own driver is not WinUSB-compatible).

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

Each entry is one device on its own Alpaca `"port"` (required, unique), always
as device number 0 of its type there. `"enable": false` turns an entry off
without deleting it. Prefer `serial`/`addr` binding where a driver supports it —
a stable identity that survives replugs and index shuffles; `index` selects the
Nth attached unit and is fine for a single device.

The binary documents itself:

```sh
alpacahurd -drivers            # list the drivers compiled into this binary
alpacahurd -example            # print a full starter config (all drivers, disabled)
alpacahurd -example asicam     # print one driver's entry
alpacahurd -check              # validate the config without touching hardware
```

`-check` also runs as the systemd `ExecStartPre`, so a typo'd config fails
fast with a readable journal message instead of a crash loop. Unknown config
keys are rejected — top-level by the loader, per-entry by each driver — so
typos are reported, not silently ignored.

A full worked example (INDI, LX200, optics, weather→mount feed) is in
[`config/hurd.example.json`](config/hurd.example.json).

## Choosing drivers (hurd.conf)

[`hurd.conf`](hurd.conf) lists the driver packages compiled into the binary,
one Go import path per line — comment out what you don't need, append any
third-party driver's public module path, and `make`. The generated
`drivers_gen.go` is checked in with the full default set, so an untouched
clone builds with plain `go build`. To pin a driver version:
`go get <module>@<version>` after `make gen`.

Drivers register themselves with `goalpaca/registry` at init; writing one is
a small amount of glue over your hardware library — see
[DRIVERS.md](DRIVERS.md).

## Extra front-ends: INDI and LX200

Beyond Alpaca, the same device objects can be served over other native
protocols — sibling front-ends onto one device object, so state stays
consistent across all of them.

- **INDI** (PHD2): set a top-level `"indi": { "enable": true, "port": 7624 }`.
  One in-process INDI server multiplexes devices by **device name** on a single
  port. Membership is opt-in per device: `"indi": true`. Mounts join as a
  telescope+guider; `asicam` and `sim-camera` join as CCD guide cameras
  (`asicam` with gain/offset/subframe as `CCD_CONTROLS`/`CCD_FRAME`). Give INDI
  devices an explicit `"name"` — that's what PHD2 shows, and names must be
  unique. A mount's `"guideRate"` (fraction of sidereal, default 0.5) is
  reported to PHD2; tenmicron/asiam5 report the mount's actual rate instead.
- **LX200** (Stellarium, SkySafari): set `"lx200": { "enable": true, "basePort": 4030 }`.
  LX200 can't multiplex, so every mount gets its own port from `basePort`
  upward; a mount can pin one with `"lx200Port": N` (which also enables LX200
  for just that mount). `"readOnlySite": true` stops an atlas from overwriting
  a modeled mount's surveyed site/clock.

Alpaca clients still auto-discover via UDP 32227 as before; these are additive.

## Simulated devices

Every binary includes a full set of `sim-*` drivers, one per ASCOM type —
testability is how this ecosystem stays verifiably reliable on installed
machines. [`config/hurd.sim.json`](config/hurd.sim.json) serves a complete
no-hardware herd:

```sh
./alpacahurd -config config/hurd.sim.json
```

`sim-telescope` and `sim-camera` share one simulated sky with a drifting star
and a closed guide loop, and they join the INDI/LX200 front-ends — so PHD2 can
connect the sim mount and sim guide camera and run a real calibration, and
Stellarium can slew the mount, before any hardware exists.

## Restricting interfaces, IPv6, logging

- `"listen": ["lo", "eth0"]` restricts every server (Alpaca, INDI, LX200,
  discovery) to those interfaces. An interface name serves both IP stacks; a
  bare IPv4 literal is IPv4-only. Omit to bind everything.
- `"ipv6": false` turns off the IPv6 discovery responder (multicast group
  `ff12::a1:9aca`); IPv4 broadcast is unaffected.
- Everything logs to stderr → the journal. `"debug": true` adds per-request
  Alpaca and per-message INDI logging; lifecycle lines (listening, device
  acquired/lost with reason, client connects) print regardless.

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

## Development

Local development against unpublished sibling checkouts uses a `go.work`
workspace (gitignored; see the comment in this repo's `go.work` for the
layout). `make gen` runs `go mod tidy`, which needs the driver modules'
registration changes published — during development, build with `make build`
or `go build` instead.

alpacahurd is the successor of the `fleet` (astrofleet) prototype that lived
in `goalpaca-devices`; the engine is the same, with driver knowledge moved
into the driver modules via `goalpaca/registry`.
