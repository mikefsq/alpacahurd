package hurd

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed readiness.js
var readinessJS string

type readinessResult struct {
	CanStart bool   `json:"canStart"`
	Hardware string `json:"hardware"`
	State    string `json:"state"`
	Kind     string `json:"kind"`
	Detail   string `json:"detail"`
	Checked  string `json:"checked"`
}

func readinessLabel(enabled bool, label, kind, detail string) readinessResult {
	prefix := "Disabled"
	if enabled {
		prefix = "Enabled"
	}
	return readinessResult{Hardware: "Unknown", State: prefix + " · " + label, Kind: kind, Detail: detail, Checked: time.Now().Format(time.RFC3339)}
}

func (o *orchestrator) handleReadiness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	inst := r.URL.Query().Get("instance")
	o.mu.RLock()
	var rows []orchRow
	for _, row := range o.rows {
		if row.spec.Instance == inst && inst != "" {
			rows = append(rows, row)
		}
	}
	sup := o.sup
	o.mu.RUnlock()
	if len(rows) == 0 {
		http.Error(w, "Unknown device", http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	row := rows[0]
	spec := row.spec
	result := readinessLabel(spec.enabled(), "Not verified", "neutral", "Device operation has not been checked.")
	data, err := os.ReadFile(spec.Source)
	if err == nil {
		var warning string
		spec, warning, err = validateDeviceEdit(ctx, row.spec, string(data))
		if warning != "" {
			err = fmt.Errorf("%s", warning)
		}
	}
	if err != nil && ctx.Err() != nil {
		result = readinessLabel(row.spec.enabled(), "Not verified", "neutral", "Configuration check timed out. Refresh status to try again.")
	} else if err != nil {
		result = readinessLabel(row.spec.enabled(), "Needs configuration", "warn", err.Error())
	} else if !spec.enabled() {
		result = readinessLabel(false, "Configured", "neutral", "Configuration check passed. Hardware has not been tested while disabled.")
	} else if row.skipped != "" {
		result = readinessLabel(true, "Error", "error", row.skipped)
	} else {
		if row.res.kind == installedBinary && (row.reg == nil || row.reg.Local) {
			if _, none := sup.(noSupervisor); !none {
				status, err := sup.Status(ctx, inst)
				if err == nil && (status.LastError != "" || strings.HasPrefix(status.State, "failed")) {
					result = readinessLabel(true, "Error", "error", "Service failed: "+status.State+" "+status.LastError)
				}
			}
		}
		if result.Kind != "error" {
			port := row.port
			if port == 0 {
				port = spec.Port
			}
			host := "127.0.0.1"
			if registeredState(row.reg) != "" {
				port = row.reg.AlpacaPort
				if !row.reg.Local {
					host = row.reg.Addr.String()
				}
			}
			if port > 0 {
				result = probeReadiness(ctx, "http://"+net.JoinHostPort(host, fmt.Sprint(port)))
			}
		}
	}
	result.CanStart = err == nil && spec.enabled()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(result)
}

// Only GET requests: never connect a device or change its settings during a check.
func probeReadiness(ctx context.Context, base string) readinessResult {
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string, value any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("device returned HTTP %d", resp.StatusCode)
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(value)
	}
	var devices struct {
		Value []struct {
			DeviceType   string
			DeviceNumber int
		}
		ErrorNumber  int
		ErrorMessage string
	}
	if err := get("/management/v1/configureddevices", &devices); err != nil {
		return readinessLabel(true, "Not verified", "neutral", "Could not check the device: "+err.Error())
	}
	if devices.ErrorNumber != 0 {
		return readinessLabel(true, "Error", "error", devices.ErrorMessage)
	}
	if len(devices.Value) == 0 {
		return readinessLabel(true, "Not verified", "neutral", "The daemon reports no devices.")
	}
	for _, device := range devices.Value {
		typ := strings.ToLower(device.DeviceType)
		if strings.ContainsAny(typ, "/?.") || typ == "" || device.DeviceNumber < 0 {
			return readinessLabel(true, "Not verified", "neutral", "The daemon returned invalid device information.")
		}
		var connected struct {
			Value        *bool
			ErrorNumber  int
			ErrorMessage string
		}
		if err := get(fmt.Sprintf("/api/v1/%s/%d/connected", typ, device.DeviceNumber), &connected); err != nil {
			return readinessLabel(true, "Not verified", "neutral", err.Error())
		}
		if connected.ErrorNumber != 0 {
			return readinessLabel(true, "Error", "error", connected.ErrorMessage)
		}
		if connected.Value == nil {
			return readinessLabel(true, "Not verified", "neutral", "The driver did not report its connection state.")
		}
		if !*connected.Value {
			result := readinessLabel(true, "Disconnected", "warn", "The driver reports a device disconnected. Open setup to check the connection.")
			result.Hardware = "Disconnected"
			return result
		}
	}
	result := readinessLabel(true, "Working", "ok", "The daemon responds and all its Alpaca devices report connected. This does not test every hardware function.")
	result.Hardware = "Connected"
	return result
}
