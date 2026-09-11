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

Installed standalone drivers are listed in `drivers.conf` beside `hurd.json`
(`/etc/alpacahurd/drivers.conf` on Linux), one absolute executable path per line.
Blank lines and lines beginning with `#` are ignored. Installers add or remove
only their own path; device configuration files remain separate. For example:

```text
/usr/local/bin/asiam5
/usr/local/bin/astrocam
```

The web Add device page combines this catalogue with compiled-in drivers, without
requiring running devices or state files. It refreshes the catalogue on each
visit. For a standalone driver, it invokes only `-schema commented`, creates a
disabled configuration with the installed executable path, and opens the editor.
Existing configuration and state are preserved; an instance name with existing
state cannot be reused by Add device to avoid inheriting unrelated settings. Missing binaries or invalid registry entries are reported
on the Add device page.

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
browser and are saved in the state directory. Enable and Disable update the
`enable` flag directly in `devices.d/<instance>.json`, preserving comments,
then start or stop the device. Legacy state-file enable flags are ignored.

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
enable or disable instances, and restart devices. Separate driver processes
also have service controls and logs. Each device has its own setup page at
`http://host:<port>/setup`.

The device list shows the name, port, Driver State, and an Enabled switch.
The switch saves `enable` directly in `devices.d/<instance>.json` and starts or
stops the device. Filter the list with **All states**, **Enabled only**, or
**Disabled only**; the selection is remembered within the browser tab.

The **⋯** menu contains **Setup**, **Edit**, **Restart**, and **Delete**. It opens
on hover with a mouse, or by click, tap, or keyboard. Setup is available when
the device is enabled and running; the device name also links to Setup.
Delete requires a disabled, stopped device and removes only its configuration
file, preserving its installed binary, `drivers.conf` entry, and saved state.
On narrow screens, devices appear as compact rows with the switch, name, port,
status, and menu. The menu opens beside the selected row, above it if necessary.

Status checks run after the page loads; **Refresh status** in the top navigation
runs them again. Checks validate configuration and make read-only Alpaca requests:

| Driver State | Meaning |
|---|---|
| Enabled · Working | The daemon responds and its reported devices are connected. |
| Enabled · Disconnected | A device explicitly reports that it is disconnected. |
| Enabled · Not verified | Operation has not been checked successfully, or connection state is unavailable. |
| Enabled · Needs configuration | Configuration validation failed. |
| Enabled · Error | The service or device reported an error. |
| Disabled · Configured | Configuration validation passed; hardware operation is not tested while disabled. |
| Disabled · Needs configuration | Configuration validation failed. |
| Disabled · Not verified | Configuration has not been checked successfully yet. |

Working reflects the driver's reported connection state, not a test of every
hardware function. Systemd state is shown on **Logs** when a device is selected,
rather than in the device list. Logs opens in a new tab and offers a device
selector, All devices, pause/resume, refresh, and follow-latest controls.

**Add device** opens a separate page and creates a disabled configuration from
the selected driver's template, then opens the editor. The editor checks JSONC
syntax while typing. **Check configuration** validates the draft before Save is
available; edits invalidate the previous check, and Save validates again before
writing. Failed submissions preserve the draft. Disabled drafts may be saved
with driver validation warnings; enabling requires a passing configuration.
Successful saves return to the device list.

The shared setup page uses TCP port 32227 by default. If it is occupied,
alpacahurd selects another port and logs the address. Set `setupPort` in
`hurd.json` to choose a different starting port, or `-1` to disable the page.

Use Restart after editing a device file. For standalone drivers this restarts
the service process. Built-in simulators are recreated internally, reopening
their device without restarting alpacahurd or other instances. On Unix,
`systemctl reload alpacahurd` or SIGHUP reloads compiled-in devices. Port, driver, and shared server setting changes require a restart.

Installed driver binaries built with the current sources accept `-discover` to
print hardware candidates as JSON (`driver`, `supported`, `identity`, `devices`).
This command does not start a server or save configuration. Busy devices may
have incomplete identity information. It is separate from the Alpaca network
`-discovery` flag; the web editor does not yet invoke hardware discovery.

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
