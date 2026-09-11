package hurd

import (
	"bytes"
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

// serverName selects the config, state, and log directories.
const serverName = "alpacahurd"

// stateDirRoot is the resolved state directory for this host.
func stateDirRoot() string { return alpacadev.StateDir(serverName) }

// devicesSubdir is the directory beside hurd.json holding one device per file.
const devicesSubdir = "devices.d"

// stateDevicesSubdir holds per-device state files.
const stateDevicesSubdir = "devices"

// loadDevicesDir loads device files and state overlays in filename order.
// A missing directory returns no entries.
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
	return deviceSpecFromAdmin(path, stateDir, admin)
}

// deviceSpecFromAdmin applies the same state overlay to disk and editor input.
func deviceSpecFromAdmin(path, stateDir string, admin map[string]json.RawMessage) (DeviceSpec, error) {
	instance := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	var state map[string]json.RawMessage
	var err error
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

// readJSONObject reads a JSON object with line and block comments.
func readJSONObject(path string) (map[string]json.RawMessage, error) {
	return devicemain.ReadDeviceFile(path)
}

// overlay merges state and admin settings, returning pinned driver keys.
// Admin values win. Enable belongs exclusively to the device configuration.
func overlay(admin, state map[string]json.RawMessage) (merged map[string]json.RawMessage, pinned map[string]bool) {
	merged = make(map[string]json.RawMessage, len(admin)+len(state))
	for k, v := range state {
		if strings.EqualFold(k, "enable") {
			continue
		}
		merged[k] = v
	}
	pinned = map[string]bool{}
	for k, v := range admin {
		merged[k] = v
		if !isCommonKey(k) {
			pinned[k] = true
		}
	}
	return merged, pinned
}

// deviceEnableText changes only top-level enable values, retaining JSONC comments.
func deviceEnableText(text string, on bool) (string, error) {
	clean, err := editorJSON(text)
	if err != nil {
		return "", err
	}
	if !json.Valid(clean) {
		return "", fmt.Errorf("invalid device JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(clean))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return "", fmt.Errorf("device configuration must be an object")
	}
	type span struct{ start, end int }
	var spans []span
	keys := 0
	for dec.More() {
		keys++
		key, err := dec.Token()
		if err != nil {
			return "", err
		}
		start := int(dec.InputOffset())
		for start < len(clean) && (clean[start] == ':' || bytes.ContainsRune([]byte(" \t\r\n"), rune(clean[start]))) {
			start++
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return "", err
		}
		if strings.EqualFold(key.(string), "enable") {
			spans = append(spans, span{start, int(dec.InputOffset())})
		}
	}
	value := "false"
	if on {
		value = "true"
	}
	for i := len(spans) - 1; i >= 0; i-- {
		r := spans[i]
		text = text[:r.start] + value + text[r.end:]
	}
	if len(spans) == 0 {
		start := bytes.IndexByte(clean, '{') + 1
		comma := ""
		if keys > 0 {
			comma = ","
		}
		text = text[:start] + "\n  \"enable\": " + value + comma + text[start:]
	}
	return text, nil
}

func writeDeviceEnable(path string, on bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text, err := deviceEnableText(string(data), on)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, []byte(text))
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

// stateDevicesDir returns the directory for per-device state files.
func stateDevicesDir() string {
	return filepath.Join(stateDirRoot(), stateDevicesSubdir)
}

// portScanBase starts automatic port allocation. Each server gets a portScanSpan window.
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

// waitBound returns bound ports in server order, or the first startup error.
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

// persistBoundPorts updates device state with the bound ports.
// Inline entries and unchanged ports are skipped.
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
