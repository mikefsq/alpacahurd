// Package hurd is the alpacahurd engine: it loads a device config, constructs
// each enabled device through the goalpaca driver registry, and serves the
// whole herd — per-device Alpaca servers, one shared discovery responder, and
// the optional INDI and LX200 front-ends — in a single process.
package hurd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

// Config is the hurd configuration: the devices in Devices, plus the shared
// discovery, INDI, and LX200 front-ends. A device is enabled by appearing in the
// list; remove it (or set "enable": false) to disable it.
type Config struct {
	Discovery string `json:"discovery"` // direct | off

	// Listen restricts which interfaces the hurd serves on, applied to the Alpaca
	// servers, LX200 bridges, INDI hub, and discovery. Each entry is an interface name
	// (e.g. "en0", "eth0", "lo") — expanding to all of its addresses, both IP stacks —
	// or an IP literal (a bare IPv4 literal is IPv4-only). Empty (the default) binds
	// every interface (":port") on both stacks. See resolveListen.
	Listen []string `json:"listen,omitempty"`

	// IPv6 also answers Alpaca discovery over IPv6 multicast (group ff12::a1:9aca),
	// alongside the IPv4 broadcast responder. Defaults to true (pointer field, omitted
	// means on); set "ipv6": false to bind IPv4 only. Best-effort: with no usable IPv6
	// it logs once and IPv4 discovery is unaffected.
	IPv6 *bool `json:"ipv6,omitempty"`

	// Debug enables verbose per-request traffic logging for the Alpaca servers (one
	// line per HTTP request) and the INDI hub (per-message). Defaults to false.
	// Lifecycle logs print regardless.
	Debug bool `json:"debug,omitempty"`

	// SetupPort is the TCP port of the orchestrator's own page: every device on
	// every port, with links to each one's setup page and the supervisor's
	// actions. Default 32227, the number every Alpaca client already knows for
	// this host, free on TCP since discovery is UDP. This is a convention of ours,
	// not the specification's, and a firewall rule for UDP 32227 does not open
	// TCP 32227. If the port is taken the page falls back to a scan and the log
	// says where it landed. 0 keeps the default; -1 turns the page off.
	SetupPort int `json:"setupPort,omitempty"`

	// Indi optionally hosts a single in-process INDI server (one port, devices that
	// opt in via "indi": true, multiplexed by device name) for INDI clients — added
	// for PHD2. Omit or disable to leave it off.
	Indi IndiConfig `json:"indi,omitempty"`

	// LX200 optionally serves a Meade-LX200 TCP server (Stellarium/SkySafari) per
	// mount. LX200 needs one port per mount, so enabling it assigns each mount a port
	// from BasePort upward (a device can pin its own with "lx200Port").
	LX200 LX200Config `json:"lx200,omitempty"`

	Devices []DeviceSpec `json:"devices"`
}

// LX200Config configures the optional per-mount LX200 servers.
type LX200Config struct {
	Enable   bool `json:"enable,omitempty"`
	BasePort int  `json:"basePort,omitempty"` // default 4030 when Enable is set

	// ReadOnlySite makes the bridge ACK a client's site/time set commands without
	// writing them to the mount, so an atlas can't overwrite a modeled mount's surveyed
	// site/clock. Reads still report the mount's real values. Off by default.
	ReadOnlySite bool `json:"readOnlySite,omitempty"`
}

// basePort returns the LX200 base port, defaulting to 4030.
func (l LX200Config) basePort() int {
	if l.BasePort == 0 {
		return 4030
	}
	return l.BasePort
}

// IndiConfig configures the optional shared INDI server. INDI has no discovery, so
// the port is static (conventionally 7624) and clients are pointed at host:port plus
// a device name.
type IndiConfig struct {
	Enable bool `json:"enable,omitempty"`
	Port   int  `json:"port,omitempty"` // default 7624 when Enable is set
}

// port returns the INDI port, defaulting to the conventional 7624.
func (i IndiConfig) port() int {
	if i.Port == 0 {
		return 7624
	}
	return i.Port
}

// setupPort resolves the orchestrator page's port: the default when unset, or 0
// when the page is turned off.
func (c *Config) setupPort() int {
	switch {
	case c.SetupPort < 0:
		return 0
	case c.SetupPort == 0:
		return defaultSetupPort
	}
	return c.SetupPort
}

// defaultSetupPort is TCP 32227: the discovery number, free on TCP.
const defaultSetupPort = 32227

// ipv6Enabled reports whether IPv6 discovery should be answered (default true).
func (c *Config) ipv6Enabled() bool { return c.IPv6 == nil || *c.IPv6 }

// DeviceSpec declares one device: the engine-owned common fields, plus the raw
// config entry the selected driver decodes its own fields from (registry.Spec.
// Decode — strict, so a typo in a driver field is an error there). The common
// field set must match registry.CommonKeys; a test enforces it.
//
// Each device gets its own acquire/monitor/re-acquire goroutine, so it is
// picked up whenever its hardware appears and survives unplug/replug
// independently of the others. That holds however entries are spread over
// ports: sharing a port shares only the HTTP server.
type DeviceSpec struct {
	deviceCommon

	// Raw is the entire JSON entry, handed to the driver via registry.Spec. For
	// a devices.d entry it is the admin file overlaid by the state file.
	Raw json.RawMessage `json:"-"`

	// Instance is the entry's identity: the devices.d filename without its
	// extension, or "" for an entry from the inline "devices" array. It names
	// the state file the setup page writes.
	Instance string `json:"-"`

	// Pinned lists the driver-owned keys the admin file names. Those render
	// locked on the setup page and are never overridden by the state file.
	// Nil for an inline entry, whose every key is treated as pinned.
	Pinned map[string]bool `json:"-"`

	// Source names the file the entry came from, for messages and the setup
	// page's locked-field note.
	Source string `json:"-"`
}

// deviceCommon are the engine-owned keys of a device entry (registry.CommonKeys).
type deviceCommon struct {
	Driver string `json:"driver"` // registry driver name, e.g. "tenmicron", "sim-camera"
	Name   string `json:"name,omitempty"`

	// Enable toggles this device without removing its entry. Defaults to true (pointer
	// field, omitted means enabled); set "enable": false to skip it at startup.
	Enable *bool `json:"enable,omitempty"`

	// Port is this device's Alpaca HTTP port. Required for enabled devices.
	// Entries that name the same port share one Alpaca server, appearing on it as
	// device 0, 1, … of their ASCOM type, the layout a client needs to see two
	// cameras under one address.
	Port int `json:"port,omitempty"`

	// Exec names the executable for a driver that runs as a separate binary,
	// when the binary is not named for the driver or not on PATH. Ignored for a
	// driver compiled in.
	Exec string `json:"exec,omitempty"`

	// Device pins this entry's ASCOM device number within its port. Omitted
	// numbers are assigned in config order, lowest free number per type, so a lone
	// device on a port is always 0. Pin them once clients have stored device URLs:
	// otherwise disabling one entry renumbers the ones after it.
	Device *int `json:"device,omitempty"`

	// Indi opts a device into the shared INDI hub (default out, Alpaca-only). Set
	// "indi": true to expose it over INDI.
	Indi *bool `json:"indi,omitempty"`

	// LX200Port pins this mount's LX200 server to a specific port, overriding the
	// hurd's auto-assignment. Setting it also enables LX200 for just this mount even
	// when the top-level "lx200" block is off.
	LX200Port int `json:"lx200Port,omitempty"`

	Aperture     float64 `json:"aperture,omitempty"`     // optics (telescopes): mm (e.g. 130)
	ApertureArea float64 `json:"apertureArea,omitempty"` // optics: m² (default from diameter)
	FocalLength  float64 `json:"focalLength,omitempty"`  // optics: mm (e.g. 1000)

	// Guide-scope optics in mm for INDI TELESCOPE_INFO / GUIDER_*. Omitted defaults to
	// the main scope (the OAG case). A client can also push these at runtime via the
	// mount's setoptics Action.
	GuiderAperture    float64 `json:"guiderAperture,omitempty"`
	GuiderFocalLength float64 `json:"guiderFocalLength,omitempty"`

	// GuideRate is the mount's guide speed as a fraction of sidereal (e.g. 0.5),
	// reported over INDI so PHD2 can scale calibration. Defaults to 0.5 when omitted.
	GuideRate float64 `json:"guideRate,omitempty"`
}

// UnmarshalJSON decodes the common fields loosely (driver-owned keys are not
// errors here — the driver's strict Decode covers them) and keeps the whole
// entry for the driver.
func (d *DeviceSpec) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &d.deviceCommon); err != nil {
		return err
	}
	d.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// indiEnabled reports whether this device should join the INDI hub. Opt-in: a device
// joins only when it sets "indi": true; the default is Alpaca-only.
func (d DeviceSpec) indiEnabled() bool { return d.Indi != nil && *d.Indi }

// enabled reports whether this device should be registered (default true).
func (d DeviceSpec) enabled() bool { return d.Enable == nil || *d.Enable }

// resolveConfigPath decides which config file to load when the -config flag is
// not given an explicit value. An explicit flag always wins. Otherwise
// $ALPACAHURD_CONFIG overrides the search, and failing that the first existing
// file among these locations is used, in order:
//
//	./hurd.json                        the current directory (a source or dev tree)
//	<ConfigDir>/hurd.json              the platform config directory: per-user for
//	                                   an interactive run, system-wide under a service
//	<SystemConfigDir>/hurd.json        the system-wide location whatever the user, so
//	                                   an interactive run finds an installed service's file
//
// server.ConfigDir resolves the platform directory (/etc/alpacahurd on Linux
// under a service, ~/.config/alpacahurd for a user, and the macOS and Windows
// equivalents), honouring $ALPACA_CONFIG_DIR and systemd's
// $CONFIGURATION_DIRECTORY first. Under systemd the working directory is /, so
// ./hurd.json is absent and the service's file is found through ConfigDir.
func resolveConfigPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if env := os.Getenv("ALPACAHURD_CONFIG"); env != "" {
		return env, nil
	}
	// ./hurd.json first (a source or dev tree), then the resolved config
	// directory: the per-user location for an interactive run, the system-wide
	// one under a service (server.ConfigDir; /etc/alpacahurd on Linux). A
	// system-wide file is also tried explicitly, so an interactive run on a host
	// with an installed service finds the service's config.
	candidates := []string{"hurd.json", filepath.Join(alpacadev.ConfigDir(serverName), "hurd.json")}
	if sys := filepath.Join(alpacadev.SystemConfigDir(serverName), "hurd.json"); sys != candidates[1] {
		candidates = append(candidates, sys)
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no config file found (looked in %s); pass -config or set $ALPACAHURD_CONFIG",
		strings.Join(candidates, ", "))
}

// LoadConfig reads and validates a hurd config file. Unknown top-level JSON fields
// are rejected so a typo in the config is reported rather than silently ignored;
// unknown keys inside a device entry are rejected by that driver's strict Decode
// when the device is constructed.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Discovery == "" {
		c.Discovery = "direct"
	}
	// Inline entries are the original layout. Each is treated as wholly
	// admin-owned: every driver key it names is pinned, and it has no state file.
	for i := range c.Devices {
		c.Devices[i].Source = path
		c.Devices[i].Pinned = pinnedKeys(c.Devices[i].Raw)
	}
	if len(c.Devices) > 0 {
		log.Printf("alpacahurd: %s: the inline \"devices\" array still works; the %s directory beside it is the newer layout, one device per file", path, devicesSubdir)
	}
	// devices.d entries follow, in filename order, overlaid by their state files.
	fromDir, err := loadDevicesDir(devicesDirFor(path), stateDevicesDir())
	if err != nil {
		return nil, err
	}
	c.Devices = append(c.Devices, fromDir...)
	return &c, nil
}
