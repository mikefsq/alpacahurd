# alpacahurd

A herd of [ASCOM Alpaca](https://ascom-standards.org/) astronomy
device drivers intended to run on a Raspberry Pi. This is a
platform supervisor that runs independent goalpaca driver binaries. It also
supports linking the supervisor and driver modules together into one static
binary.

## Platforms

This works on any platform that can compile Go.

| Platform | Transport | Build |
|---|---|---|
| Linux | usbfs / hidraw / serial | `go build`, no C toolchain  |
| macOS | IOKit / IOUSBHost — **cgo** | `go build` with the Xcode command-line tools for cgo |
| Windows | WinUSB / HID / serial | `go build`, no C toolchain |


## Building from source

The default `make build` builds the bare orchestrator `./alpacahurd`, which
runs every device as a separate driver binary. The optional `make fat` regenerates
`drivers_gen.go` from `hurd.conf` and compiles those drivers into the one
binary.

```sh
#get the source
git clone https://github.com/mikefsq/alpacahurd
cd alpacahurd
make build

#optional, for a single binary 
nano hurd.conf        # adjust the driver list, or add third-party drivers
make fat              # builds with the hurd.conf drivers compiled in

#install 
sudo make install     # binary + systemd/launchd + config

#restart 
sudo systemctl restart alpacahurd # or 
sudo launchctl kickstart -k system/com.mikefsq.alpacahurd

```

## Install on macOS/Linux

Needs Go ≥ 1.23. Debian 13 Trixie ships Go 1.24; a Mac can use `brew install
go`. The official toolchain is at [go.dev/dl](https://go.dev/dl/).

```sh
git clone https://github.com/mikefsq/alpacahurd
cd alpacahurd
nano hurd.conf        # optional: trim the driver list, or add third-party drivers
make workspace        # pre-release only: writes go.work over the sibling checkouts
make fat              # builds ./alpacahurd with the hurd.conf drivers compiled in
sudo make install     # binary + systemd service + udev rules + seeded config
sudo nano /etc/alpacahurd/devices.d/astrocam.json   # enable YOUR devices (see below)
sudo systemctl restart alpacahurd
journalctl -u alpacahurd -f            # watch it acquire your hardware
```

`make install` seeds the server config `hurd.json` with the server blocks and
`devices.d/` with **one disabled file per compiled-in driver**. These directories
are located in `/etc/alpacahurd/` on Linux and `/Library/Application Support/alpacahurd/` on
macOS. Edit the files for the hardware you are using and restart the service. 

The default `make build` target includes no hardware drivers. Each driver
prints a config template with `<driver> -schema commented`. The `make
install` target for each driver (in goalpaca-devices) installs the driver
binary beside `alpacahurd` and writes a config file into `devices.d` if one
does not already exist.
## Install on Windows

Needs Go ≥ 1.23 ([go.dev/dl](https://go.dev/dl/)); no C toolchain. `make.ps1`
is the Windows counterpart of the Makefile (`.\make.ps1 help` lists the
targets). From PowerShell in the repo directory:

```powershell
.\make.ps1 fat         # builds alpacahurd.exe with the hurd.conf drivers compiled in
.\make.ps1 install     # elevated: install binary + config, startup task, firewall rule
```

`install` seeds `%ProgramData%\alpacahurd\hurd.json` with the server blocks
and `devices.d\` with one disabled file per compiled-in driver, validates
the config, registers a SYSTEM startup task that restarts on failure (the
Windows analogue of the systemd service), and opens the firewall for the binary.
Then edit the config and restart:

```powershell
notepad $env:ProgramData\alpacahurd\devices.d\astrocam.json
Restart-ScheduledTask -TaskName alpacahurd
```

Run it as `powershell -ExecutionPolicy Bypass -File .\make.ps1 <target>` if
execution policy blocks the script. Bind USB cameras to the generic WinUSB
driver with [Zadig](https://zadig.akeo.ie/) first; vendor drivers are
usually not WinUSB-compatible.

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
/etc/alpacahurd/devices.d/mount.json       one device
/etc/alpacahurd/devices.d/main-camera.json another
```

A device file is one JSON object. The filename is the device's identity and
names its state file:

```json
{
  "driver": "tenmicron",
  "port": 11100,
  "addr": "10.0.1.51:3492",
  "name": "Mount",
  "lx200Port": 4030,
  "enable": true
}
```

The entry binds its hardware by `serial`, `addr`, or enumeration `index`,
depending on the driver. `"enable": false` turns an entry off without
deleting it. `"port"` names the entry's Alpaca port; an entry that omits it
scans for a free one at first start, and the state file records the pick.

A multi-device driver serves several devices of its type from one file on one
port. astrocam's `"cameras"` array holds one block per camera, numbered
0, 1, … in order:

```json
{
  "driver": "astrocam",
  "port": 11201,
  "cameras": [
    { "serial": "2e19c40425000900", "name": "Main" },
    { "serial": "1810190e15000900", "name": "Guide" }
  ],
  "enable": true
}
```

Note: separate ports per device are better for anything you restart or
replug independently, so the other device is not reset. Shared ports exist
for older client software that expects several devices at one address.

Note: pin `serial`, `port`, and `device`. These three fix a client's connect
string: `serial` selects a unique device out of the enumeration, and the IP
address, `port`, and `device` identify the device to the client. A change
means updating the client configuration.

### The setup page and the state directory

alpacahurd serves an orchestrator page (`http://host:32227/setup`) on the
same port number that answers Alpaca UDP discovery. The page shows every
known device and lets you see status, edit configurations, and restart each
device. Each device also has a browser configuration page
(`http://host:<port>/setup`), generated from the driver's config struct.

| role | Linux | macOS | Windows |
|---|---|---|---|
| config | `/etc/alpacahurd/` | `/Library/Application Support/alpacahurd/` | `%ProgramData%\alpacahurd\` |
| state | `/var/lib/alpacahurd/devices/` | `…/alpacahurd/state/devices/` | `…\alpacahurd\state\devices\` |
| logs | journal | `/Library/Logs/alpacahurd/` | `…\alpacahurd\logs\` |

An interactive run uses the per-user equivalents. `$ALPACA_CONFIG_DIR` and
`$ALPACA_STATE_DIR` override either.

### Reload without a restart

A device file edit takes effect on a reload. The Reload button rebuilds a
device from its file, closing and reopening its hardware. A whole-herd
reload is `systemctl reload alpacahurd` or `kill -HUP`. A port change
requires a restart.


## LX200 (Stellarium, SkySafari)

The mount drivers (tenmicron, rst, onstep, asiam5) serve a Meade-LX200 TCP
bridge for clients like Stellarium. Enable it with `"lx200Port": 4020` in
the device file; the bridge stops when the mount is disabled.

## Simulated devices

The `sim` module provides a full set of `sim-*` drivers. They are compiled
into every flavor, the bare orchestrator included, so any install can serve
a complete no-hardware herd for verifying itself and developing clients.
[`config/hurd.sim.json`](config/hurd.sim.json) is the ready-made sim herd:

```sh
./alpacahurd -config config/hurd.sim.json
```

## Restricting interfaces, IPv6, logging

- `"listen": ["lo", "eth0"]` restricts every server (Alpaca, discovery, and
  driver front-ends such as LX200) to those interfaces. An interface name 
  serves both IP stacks; a bare IPv4 literal is IPv4-only. Omit to bind everything.
- `"ipv6": false` turns off the IPv6 discovery responder (multicast group
  `ff12::a1:9aca`); IPv4 broadcast is unaffected.
- Everything logs to stderr → the journal. `"debug": true` adds per-request
  Alpaca logging; lifecycle lines (listening, device acquired/lost with reason,
  client connects) print regardless.

## USB permissions (udev)

USB/HID drivers need usbfs/hidraw access; the Go ASI camera driver reads
the factory serial via a vendor control transfer (it's not a USB descriptor),
so serial binding needs read-write access to the device node.
`deploy/99-alpacahurd.rules` covers ZWO, PlayerOne, Astroasis, and
FTDI-serial devices; `make install` installs it (then replug). Add other
vendors' `idVendor` lines as you wire their drivers in.

## Running it yourself (no service)

```sh
make fat                          # the hardware drivers compiled in
./alpacahurd -config hurd.json
```

The fat build is the simplest to run by hand. It serves everything in one process
and needs no supervisor. The bare build serves the sims itself and hands the
hardware entries to the platform supervisor, which a hand run usually lacks
the rights to drive; `alpacahurd -launch <instance>` runs one such entry from
a console instead.

Linux and Windows binaries cross-compile from anywhere (pure Go):

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -tags fat -o alpacahurd .    # 64-bit Pi
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags fat -o alpacahurd.exe .
```

macOS binaries must be built on a Mac (the IOKit transports need cgo).

`sudo make uninstall` removes the service and binary on Linux (systemd + udev
rules) and macOS (launchd); the config in `/etc/alpacahurd` is kept.
