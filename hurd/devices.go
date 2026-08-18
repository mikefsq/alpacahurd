package hurd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mikefsq/goalpaca/devicemain"
	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// serverName is the name every alpacahurd server registers under; it selects
// the config, state, and log directories.
const serverName = "alpacahurd"

// stateDirRoot is the resolved state directory for this host.
func stateDirRoot() string { return alpacadev.StateDir(serverName) }

// Device entries come from two places. The inline "devices" array in hurd.json
// is the original layout and still works. The devices.d directory beside
// hurd.json holds one JSON object per file, and is the layout the setup page
// and the packaging plan build on. Both feed the same DeviceSpec.
//
// A devices.d entry has two layers. The admin file under the config directory
// (/etc/alpacahurd/devices.d/<name>.json) says what the admin decided; every
// driver-owned key it names is pinned. The state file under the state
// directory (/var/lib/alpacahurd/devices/<name>.json) holds what the setup page
// wrote; its keys fill in whatever the admin file left unset and never override
// a pinned key. The overlay of the two is the entry's effective config.

// devicesSubdir is the directory beside hurd.json holding one device per file.
const devicesSubdir = "devices.d"

// stateDevicesSubdir is the directory under the state dir holding the setup
// page's per-device writes, mirroring devices.d by filename.
const stateDevicesSubdir = "devices"

// loadDevicesDir reads every *.json file in dir as one device entry, in name
// order, with the filename stem as the entry's Instance. A missing dir yields
// no entries and no error, since a fresh install may not have one. stateDir,
// when non-empty, is overlaid: the file of the same name there supplies keys the
// admin file left unset.
func loadDevicesDir(dir, stateDir string) ([]DeviceSpec, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []DeviceSpec
	for _, path := range names {
		spec, err := loadDeviceFile(path, stateDir)
		if err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	return out, nil
}

// loadDeviceFile reads one admin device file and overlays its state file.
func loadDeviceFile(path, stateDir string) (DeviceSpec, error) {
	admin, err := readJSONObject(path)
	if err != nil {
		return DeviceSpec{}, err
	}
	instance := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	var state map[string]json.RawMessage
	if stateDir != "" {
		sp := filepath.Join(stateDir, instance+".json")
		if state, err = readJSONObject(sp); err != nil && !os.IsNotExist(err) {
			return DeviceSpec{}, err
		}
	}
	merged, pinned := overlay(admin, state)
	raw, err := json.Marshal(merged)
	if err != nil {
		return DeviceSpec{}, err
	}
	var spec DeviceSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return DeviceSpec{}, fmt.Errorf("%s: %w", path, err)
	}
	spec.Instance = instance
	spec.Pinned = pinned
	spec.Source = path
	return spec, nil
}

// readJSONObject reads a file holding one JSON object, with // and /* */
// comments allowed (devicemain.ReadDeviceFile), so a seeded file can document
// every key at its default. A missing file returns os.ErrNotExist so a caller
// can treat absence as empty.
func readJSONObject(path string) (map[string]json.RawMessage, error) {
	return devicemain.ReadDeviceFile(path)
}

// overlay merges a state file over an admin file. Every key in the admin file
// wins and, when driver-owned, is pinned. A state key absent from the admin
// file is taken. Common keys (driver, port, name, and the rest of
// registry.CommonKeys) are the host's own and never pinned for the driver's
// form, since they never reach it.
//
// One key runs the other way: a state "enable" wins over the admin file's.
// The seeded file writes "enable": false as its one live line, so the admin
// value is the installer's default rather than a decision, and the operator's
// switch on the orchestrator page (which writes the state file) has to take
// effect over it. An admin who wants a device kept off removes its state file
// or the device file.
func overlay(admin, state map[string]json.RawMessage) (merged map[string]json.RawMessage, pinned map[string]bool) {
	merged = make(map[string]json.RawMessage, len(admin)+len(state))
	for k, v := range state {
		merged[k] = v
	}
	pinned = map[string]bool{}
	for k, v := range admin {
		if strings.EqualFold(k, "enable") {
			if _, fromState := merged[k]; fromState {
				continue
			}
		}
		merged[k] = v
		if !isCommonKey(k) {
			pinned[k] = true
		}
	}
	return merged, pinned
}

// writeStateEnable records the operator's enable switch for a devices.d entry
// in its state file, where the overlay reads it back at the next start.
func writeStateEnable(instance string, on bool) error {
	store := alpacadev.NewFileStore()
	path := filepath.Join(stateDevicesDir(), instance+".json")
	vals, err := store.Load(path)
	if err != nil {
		return err
	}
	if vals == nil {
		vals = map[string]any{}
	}
	vals["enable"] = on
	return store.Save(path, vals)
}

var commonKeySet = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range registry.CommonKeys() {
		m[strings.ToLower(k)] = true
	}
	return m
}()

func isCommonKey(k string) bool { return commonKeySet[strings.ToLower(k)] }

// devicesDirFor returns the devices.d directory beside a config file.
func devicesDirFor(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), devicesSubdir)
}

// stateDevicesDir returns the directory the setup page writes per-device state
// files to, under the resolved state directory.
func stateDevicesDir() string {
	return filepath.Join(stateDirRoot(), stateDevicesSubdir)
}

// portScanBase is where a devices.d entry with no port starts scanning. It sits
// above the example configs' 11200 block so a scanned port never collides with
// a documented fixed one. Each scanning entry gets its own window of
// portScanSpan ports above the base, so several can scan at once.
const (
	portScanBase = 11300
	portScanSpan = 10
)

// scanEntry pairs a scanning server with the entry it serves, so its bound port
// can be persisted once known.
type scanEntry struct {
	srv  *alpacadev.Server
	spec DeviceSpec
}

// waitBound waits until every server reports a bound port, or one of them
// fails, and returns the ports in server order. A server that exits before
// binding surfaces its error here rather than as a silent hang.
func waitBound(ctx context.Context, servers []*alpacadev.Server, errc <-chan error) ([]int, error) {
	ports := make([]int, len(servers))
	deadline := time.After(10 * time.Second)
	for {
		all := true
		for i, s := range servers {
			if ports[i] == 0 {
				if p := s.Port(); p != 0 {
					ports[i] = p
				} else {
					all = false
				}
			}
		}
		if all {
			return ports, nil
		}
		select {
		case err := <-errc:
			if err == nil {
				err = fmt.Errorf("a server stopped before binding")
			}
			return nil, err
		case <-deadline:
			return nil, fmt.Errorf("servers did not bind within 10s")
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// persistBoundPorts records each devices.d entry's bound port in its state
// file, so state describes the port the device is on. A scanned port is written
// on the first start and holds from then; a port the admin file pins is bound
// and the state file is brought to match, so a stale recorded port from before
// the pin does not linger. A state file already naming the bound port is left
// untouched. Inline entries have no state file and are skipped. The write is
// atomic (FileStore).
func persistBoundPorts(rows []boundEntry) {
	store := alpacadev.NewFileStore()
	for _, e := range rows {
		if e.spec.Instance == "" || e.port == 0 {
			continue
		}
		path := filepath.Join(stateDevicesDir(), e.spec.Instance+".json")
		vals, err := store.Load(path)
		if err != nil {
			log.Printf("alpacahurd: %s: read state: %v", e.spec.Instance, err)
			continue
		}
		if vals == nil {
			vals = map[string]any{}
		}
		if cur, ok := vals["port"]; ok {
			if n, isNum := cur.(float64); isNum && int(n) == e.port {
				continue
			}
		}
		vals["port"] = e.port
		if err := store.Save(path, vals); err != nil {
			log.Printf("alpacahurd: %s: persist port %d: %v", e.spec.Instance, e.port, err)
			continue
		}
		log.Printf("alpacahurd: %s: bound port %d, recorded in %s", e.spec.Instance, e.port, path)
	}
}

// boundEntry is one served entry with the port it bound.
type boundEntry struct {
	spec DeviceSpec
	port int
}
