// Package hurd is the alpacahurd engine: it loads a device config, constructs
// each enabled device through the goalpaca driver registry, and serves the
// whole herd — per-device Alpaca servers, one shared discovery responder, and
// the optional INDI and LX200 front-ends — in a single process.
package hurd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// ipv6Enabled reports whether IPv6 discovery should be answered (default true).
func (c *Config) ipv6Enabled() bool { return c.IPv6 == nil || *c.IPv6 }

// DeviceSpec declares one device: the engine-owned common fields, plus the raw
// config entry the selected driver decodes its own fields from (registry.Spec.
// Decode — strict, so a typo in a driver field is an error there). The common
// field set must match registry.CommonKeys; a test enforces it.
//
// Each device gets its own acquire/monitor/re-acquire goroutine, so it is
// picked up whenever its hardware appears and survives unplug/replug
// independently of the others.
type DeviceSpec struct {
	deviceCommon

	// Raw is the entire JSON entry, handed to the driver via registry.Spec.
	Raw json.RawMessage `json:"-"`
}

// deviceCommon are the engine-owned keys of a device entry (registry.CommonKeys).
type deviceCommon struct {
	Driver string `json:"driver"` // registry driver name, e.g. "tenmicron", "sim-camera"
	Name   string `json:"name,omitempty"`

	// Enable toggles this device without removing its entry. Defaults to true (pointer
	// field, omitted means enabled); set "enable": false to skip it at startup.
	Enable *bool `json:"enable,omitempty"`

	// Port is this device's own Alpaca HTTP port. Required for enabled devices.
	Port int `json:"port,omitempty"`

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

// resolveConfigPath decides which config file to load when the -config flag is not
// given an explicit value. An explicit flag always wins; otherwise $ALPACAHURD_CONFIG
// overrides the search, and failing that the first existing file among these standard
// locations is used, most-specific first:
//
//	./hurd.json                              — current dir (running from a source/dev tree)
//	$XDG_CONFIG_HOME/alpacahurd/hurd.json    — per-user (~/.config/alpacahurd/hurd.json)
//	/etc/alpacahurd/hurd.json                — system-wide (the deploy/install.sh target)
//
// Under systemd the working dir is /, so ./hurd.json is absent and it falls through to
// /etc — matching where install.sh puts the config, so the unit needs no -config flag.
func resolveConfigPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if env := os.Getenv("ALPACAHURD_CONFIG"); env != "" {
		return env, nil
	}
	candidates := []string{"hurd.json"}
	if dir, err := os.UserConfigDir(); err == nil {
		candidates = append(candidates, filepath.Join(dir, "alpacahurd", "hurd.json"))
	}
	candidates = append(candidates, "/etc/alpacahurd/hurd.json")
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
	return &c, nil
}
