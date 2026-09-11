package hurd

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mikefsq/goalpaca/devicemain"
)

//go:embed editor.js
var editorScript string

func (o *orchestrator) editableSpec(instance string) (DeviceSpec, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for _, row := range o.rows {
		if row.spec.Instance == instance && row.spec.Source != "" {
			return row.spec, nil
		}
	}
	return DeviceSpec{}, fmt.Errorf("no device file for %q", instance)
}

// validateDeviceEdit checks the proposed file and effective state overlay without
// replacing the original or opening hardware. Disabled entries may be saved as
// drafts when driver validation fails, but are not reported ready to run.
func validateDeviceEdit(ctx context.Context, original DeviceSpec, text string) (DeviceSpec, string, error) {
	clean, err := editorJSON(text)
	if err != nil {
		return DeviceSpec{}, "", err
	}
	var admin map[string]json.RawMessage
	if err := json.Unmarshal(clean, &admin); err != nil {
		return DeviceSpec{}, "", fmt.Errorf("not valid JSON: %w", err)
	}
	if admin == nil {
		return DeviceSpec{}, "", fmt.Errorf("configuration must be a JSON object")
	}
	var declared DeviceSpec
	if err := json.Unmarshal(clean, &declared); err != nil {
		return DeviceSpec{}, "", err
	}
	if strings.TrimSpace(declared.Driver) == "" {
		return DeviceSpec{}, "", fmt.Errorf("the file must name a driver")
	}
	if err := checkCommonFields(declared); err != nil {
		return DeviceSpec{}, "", err
	}
	spec, err := deviceSpecFromAdmin(original.Source, stateDevicesDir(), admin)
	if err != nil {
		return DeviceSpec{}, "", err
	}
	if err := checkCommonFields(spec); err != nil {
		return DeviceSpec{}, "", err
	}
	res := resolveDriver(spec)
	switch res.kind {
	case compiledIn:
		subs, err := subSpecs(spec)
		if err != nil {
			return DeviceSpec{}, "", err
		}
		nums := &deviceNumbers{}
		for _, sub := range subs {
			drv, _, err := buildDevice(sub)
			if err != nil {
				return driverCheckResult(spec, err)
			}
			if _, err := nums.assign(sub, drv.Type); err != nil {
				return DeviceSpec{}, "", err
			}
		}
	case installedBinary:
		// Never execute a new command supplied in an unchecked browser draft.
		trusted := resolveDriver(original)
		if trusted.kind != installedBinary || trusted.exe != res.exe || spec.Driver != original.Driver || spec.Exec != original.Exec {
			return DeviceSpec{}, "", fmt.Errorf("check unavailable: changing the standalone driver or executable requires configuring it outside this editor first")
		}
		if err := checkStandalone(ctx, trusted.exe, spec.Raw); err != nil {
			if ctx.Err() != nil {
				return DeviceSpec{}, "", ctx.Err()
			}
			return driverCheckResult(spec, err)
		}
	default:
		return DeviceSpec{}, "", fmt.Errorf("driver %q is not compiled in and no executable was found", spec.Driver)
	}
	return spec, "", nil
}

// A disabled draft cannot start hardware. Preserve its diagnostics without
// presenting a failed driver check as successful readiness validation.
func driverCheckResult(spec DeviceSpec, err error) (DeviceSpec, string, error) {
	if spec.enabled() {
		return DeviceSpec{}, "", err
	}
	return spec, "Disabled draft can be saved, but is not ready to run.\n" + err.Error(), nil
}

// Reject unterminated block comments before using the shared JSONC stripper.
// This keeps browser and server validation in agreement even after valid JSON.
func editorJSON(text string) ([]byte, error) {
	quoted := false
	for i := 0; i < len(text); i++ {
		if quoted {
			if text[i] == '\\' {
				i++
			} else if text[i] == '"' {
				quoted = false
			}
			continue
		}
		if text[i] == '"' {
			quoted = true
		} else if strings.HasPrefix(text[i:], "//") {
			for i < len(text) && text[i] != '\n' {
				i++
			}
		} else if strings.HasPrefix(text[i:], "/*") {
			end := strings.Index(text[i+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("not valid JSON: unclosed block comment")
			}
			i += end + 3
		}
	}
	return devicemain.StripComments([]byte(text)), nil
}

func checkCommonFields(spec DeviceSpec) error {
	if spec.Port < 0 || spec.Port > 65535 {
		return fmt.Errorf("port must be between 0 (automatic) and 65535")
	}
	if spec.Device != nil && *spec.Device < 0 {
		return fmt.Errorf("device number must not be negative")
	}
	return nil
}

func checkStandalone(ctx context.Context, executable string, raw json.RawMessage) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "alpacahurd-check-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "device.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, executable, "-config", path, "-check")
	cmd.WaitDelay = time.Second
	var output checkOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("configuration check interrupted or timed out: %w", ctx.Err())
		}
		return fmt.Errorf("configuration check failed: %v\n%s", err, strings.TrimSpace(output.String()))
	}
	return nil
}

// Keep diagnostic output bounded even if a checker is unexpectedly noisy.
type checkOutput struct{ data bytes.Buffer }

func (b *checkOutput) String() string { return b.data.String() }

func (b *checkOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 16384 - b.data.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.data.Write(p)
	}
	return n, nil
}

func (o *orchestrator) handleEditCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]any{"valid": false, "message": "Use POST to check a draft."})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var warning string
	err := r.ParseForm()
	if err == nil {
		var original DeviceSpec
		original, err = o.editableSpec(r.PostForm.Get("instance"))
		if err == nil {
			_, warning, err = validateDeviceEdit(r.Context(), original, r.PostForm.Get("text"))
		}
	}
	if err != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{"valid": false, "message": err.Error()})
		return
	}
	if warning != "" {
		_ = json.NewEncoder(w).Encode(map[string]any{"valid": true, "ready": false, "message": warning})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"valid": true, "ready": true, "message": "Configuration valid. Ready to save; hardware has not been opened."})
}
