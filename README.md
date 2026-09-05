# alpacahurd

Run [ASCOM Alpaca](https://ascom-standards.org/) astronomy devices on Linux,
macOS, or Windows, including Raspberry Pi. alpacahurd manages driver processes
and provides a shared discovery service and browser setup page. Drivers can
also be compiled into a single binary.

## Install on Debian or Raspberry Pi OS

See the [apt archive](https://mikefsq.github.io/apt/) for installation instructions,
or download packages from [GitHub releases](https://github.com/mikefsq/alpacahurd/releases).

## Build from source

Requires Go 1.25 or later. Hardware drivers on macOS also need the Xcode
command-line tools for cgo. Linux and Windows builds need no C toolchain.

```sh
git clone https://github.com/mikefsq/alpacahurd
cd alpacahurd
make build
```

`make build` includes simulators and runs hardware drivers as separate
binaries. To bundle hardware drivers, edit the package list in `hurd.conf`
and run `make fat`.

To run a bundled build without installing a service:

```sh
make fat
./alpacahurd -config config/hurd.sim.json
```

Use your own `hurd.json` to run hardware devices. Separate driver processes
normally require access to the platform service manager; to launch one from
a console, use `./alpacahurd -config <hurd.json> -launch <instance>`.

To build Debian packages locally, run `make deb`. This needs `dpkg-deb` and
`file` as well as Go, and writes packages to `dist/`. Run
`build/build-deb -h` for architecture and version options.

For driver development and integration, see [DRIVERS.md](DRIVERS.md).

### Linux and macOS service

After building:

```sh
sudo make install
```

The installer seeds `hurd.json` and one disabled file in `devices.d/` per
compiled-in hardware driver, preserving existing files. Edit the device
files for your hardware, then restart the service:

```sh
# Linux
sudo systemctl restart alpacahurd
journalctl -u alpacahurd -f

# macOS
sudo launchctl kickstart -k system/com.mikefsq.alpacahurd
```

`sudo make uninstall` removes the service and binary and keeps your configuration.

### Windows

From PowerShell in the repository directory:

```powershell
.\make.ps1 fat
.\make.ps1 install
```

Run `install` in an elevated shell. It installs the binary and configuration,
creates a SYSTEM startup task, and adds a firewall rule. Edit files under
`$env:ProgramData\alpacahurd\devices.d`, then restart the task:

```powershell
Stop-ScheduledTask -TaskName alpacahurd
Start-ScheduledTask -TaskName alpacahurd
```

Use `.\make.ps1 build` for an orchestrator that runs hardware drivers separately. If execution
policy blocks the script, run it as
`powershell -ExecutionPolicy Bypass -File .\make.ps1 <target>`.
USB cameras using the WinUSB transport need a WinUSB-compatible driver binding.

## Configure devices

`hurd.json` holds shared server settings. A `devices.d` directory beside it
holds one JSON file per device instance:

```text
/etc/alpacahurd/hurd.json
/etc/alpacahurd/devices.d/mount.json
/etc/alpacahurd/devices.d/main-camera.json
```

A minimal server configuration is:

```json
{
  "discovery": "direct"
}
```

Pass `-config <path>` to select it explicitly. Otherwise, alpacahurd uses
`$ALPACAHURD_CONFIG`, or searches for `hurd.json` in the current directory,
the platform configuration directory, then the system configuration directory.
Interactive runs use per-user paths; services use the paths below.

| Location | Linux | macOS | Windows |
|---|---|---|---|
| Configuration | `/etc/alpacahurd/` | `/Library/Application Support/alpacahurd/` | `%ProgramData%\alpacahurd\` |
| Device state | `/var/lib/alpacahurd/devices/` | `/Library/Application Support/alpacahurd/state/devices/` | `%ProgramData%\alpacahurd\state\devices\` |
| Logs | systemd journal | `/Library/Logs/alpacahurd/` | `%ProgramData%\alpacahurd\logs\` |

`$ALPACA_CONFIG_DIR` and `$ALPACA_STATE_DIR` override configuration and state
paths.

### Device files

The filename identifies the instance and its state file. For example,
`mount.json` might contain:

```json
{
  "driver": "tenmicron",
  "port": 11100,
  "device": 0,
  "addr": "10.0.1.51:3492",
  "name": "Mount",
  //"lx200Port": 4020,  //optional lx200 port
  "enable": true
}
```

Use `serial`, `addr`, or `index` to select hardware, as supported by the driver.
Prefer a stable serial or address. Set `port` and `device` explicitly to keep
the address stored by your Alpaca client stable. A device file without `port`
gets an available port on first start and saves it in state.

Both `hurd.json` and device files accept JSONC (`//` and `/* */` comments),
with no trailing commas. To see available settings, run
`<driver> -schema commented` for an installed driver, or
`alpacahurd -example <driver>` for a compiled-in driver.
`alpacahurd -drivers` lists compiled-in drivers.

Configuration-file values take precedence over saved state and appear locked
in the device setup form. Settings left out of the file can be changed in the
browser and are saved in the state directory. The `enable` switch is an
exception: the browser's saved value takes precedence over the file, so it can
enable an initially disabled device.

Use `"exec": "/path/to/driver"` when a separate driver's executable has a
different name or is outside alpacahurd's directory and `PATH`. A compiled-in
driver takes precedence over an installed binary with the same name.

### Multiple devices on one port

Drivers with multi-device support can serve several devices from one file.
For example, astrocam numbers cameras in array order, starting at zero:

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

Use separate files and ports when devices need independent process restarts.
A shared port is useful for clients that expect several devices at one address.

## Manage devices

Open `http://host:32227/setup` to view devices, edit configuration files,
enable or disable instances, and reload devices. Separate driver processes
also have service controls and logs. Each device has its own setup page at
`http://host:<port>/setup`.

The shared setup page uses TCP port 32227 by default. If it is occupied,
alpacahurd selects another port and logs the address. Set `setupPort` in
`hurd.json` to choose a different starting port, or `-1` to disable the page.

Use a device's Reload button after editing its file. Reload closes and reopens
hardware. On Unix, `systemctl reload alpacahurd` or SIGHUP reloads compiled-in
devices. Port, driver, and shared server setting changes require a restart.

To check configuration without opening hardware:

```sh
alpacahurd -config /path/to/hurd.json -check
```

Read the reported device errors as well as the exit status: per-device errors
do not make `-check` exit nonzero or prevent other devices from starting.

### Network and logging

- `"listen": ["lo", "eth0"]` restricts in-process servers, discovery, and driver
  front-ends to those interfaces. Interface names include IPv4 and IPv6; an
  IPv4 literal binds only that address. Omit `listen` to bind all interfaces.
- `"ipv6": false` disables IPv6 multicast discovery; IPv4 discovery still runs.
- `"discovery": "off"` disables the shared discovery responder.
- `"debug": true` adds per-request Alpaca logs. Lifecycle events are always logged.

### LX200 clients

The tenmicron, rst, onstep, and asiam5 mount drivers provide an LX200 TCP bridge
for clients such as Stellarium and SkySafari. Set `"lx200Port": 4020` in the
device file to enable it. Disabling the device stops the bridge.

### USB permissions on Linux

USB and HID drivers need read-write access to their device nodes.
[The supplied udev rules](deploy/99-alpacahurd.rules) cover ZWO, PlayerOne,
Astroasis, and FTDI serial devices. The package and source installer install
these rules; replug devices afterward. Additional hardware may need its own
vendor rule.

### Simulated devices

Every build includes the `sim-*` drivers. Run the supplied configuration to
try a complete set without hardware:

```sh
./alpacahurd -config config/hurd.sim.json
```
