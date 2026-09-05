// Package hurd serves and supervises ASCOM Alpaca device drivers.
package hurd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/mikefsq/goalpaca/devicemain"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// Config holds device entries and shared server settings.
type Config struct {
	Discovery string `json:"discovery"` // direct | off

	// Listen restricts servers and discovery to interface names or IP addresses.
	// Interface names include both IP stacks; an empty list binds all interfaces.
	Listen []string `json:"listen,omitempty"`

	// IPv6 enables multicast discovery. Nil defaults to true.
	// IPv6 failures do not affect IPv4 discovery.
	IPv6 *bool `json:"ipv6,omitempty"`

	// Debug enables per-request Alpaca logging.
	Debug bool `json:"debug,omitempty"`

	// SetupPort selects the setup page TCP port. Zero defaults to 32227;
	// a negative value disables the page. Occupied ports trigger a port scan.
	SetupPort int `json:"setupPort,omitempty"`

	Devices []DeviceSpec `json:"devices"`
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

const defaultSetupPort = 32227

// ipv6Enabled reports whether IPv6 discovery should be answered (default true).
func (c *Config) ipv6Enabled() bool { return c.IPv6 == nil || *c.IPv6 }

// DeviceSpec holds common device settings and the driver configuration.
type DeviceSpec struct {
	deviceCommon

	// Raw is the complete entry after merging the admin and state files.
	Raw json.RawMessage `json:"-"`

	// Instance is the device filename stem and state file identity.
	// It is empty for inline entries.
	Instance string `json:"-"`

	// Pinned identifies driver fields locked by the admin file.
	// Nil treats every driver field in Raw as pinned.
	Pinned map[string]bool `json:"-"`

	// Source is the configuration filename shown in errors and locked fields.
	Source string `json:"-"`

	// Block is the array position and device number in a multi-device entry.
	// Nil denotes a flat entry.
	Block *int `json:"-"`
}

// deviceCommon are the engine-owned keys of a device entry (registry.CommonKeys).
type deviceCommon struct {
	Driver string `json:"driver"` // registry driver name, e.g. "tenmicron", "sim-camera"
	Name   string `json:"name,omitempty"`

	// Enable controls startup. Nil defaults to true.
	Enable *bool `json:"enable,omitempty"`

	// Port is the Alpaca HTTP port. Devices on the same port share a server.
	// Zero enables port scanning for devices.d entries.
	Port int `json:"port,omitempty"`

	// Exec overrides the executable path for a driver not compiled in.
	Exec string `json:"exec,omitempty"`

	// Device pins the ASCOM number within its port and type.
	// Nil assigns the lowest free number in configuration order.
	Device *int `json:"device,omitempty"`
}

// UnmarshalJSON decodes common fields and preserves the entry for the driver.
func (d *DeviceSpec) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &d.deviceCommon); err != nil {
		return err
	}
	d.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// enabled reports whether this device should be registered (default true).
func (d DeviceSpec) enabled() bool { return d.Enable == nil || *d.Enable }

// resolveConfigPath prefers the explicit path, then ALPACAHURD_CONFIG.
// Otherwise it searches the current, user, and system config directories.
func resolveConfigPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if env := os.Getenv("ALPACAHURD_CONFIG"); env != "" {
		return env, nil
	}
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

// LoadConfig reads the server config and device files with their state overlays.
// Server and device files accept line and block comments.
// It rejects unknown server fields; drivers validate their own fields on construction.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(devicemain.StripComments(b)))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Discovery == "" {
		c.Discovery = "direct"
	}
	// Inline entries have no state overlay; their driver fields are pinned.
	for i := range c.Devices {
		c.Devices[i].Source = path
		c.Devices[i].Pinned = pinnedKeys(c.Devices[i].Raw)
	}
	if len(c.Devices) > 0 {
		log.Printf("alpacahurd: %s: the inline \"devices\" array still works; the %s directory beside it is the newer layout, one device per file", path, devicesSubdir)
	}
	fromDir, err := loadDevicesDir(devicesDirFor(path), stateDevicesDir())
	if err != nil {
		return nil, err
	}
	c.Devices = append(c.Devices, fromDir...)
	return &c, nil
}
