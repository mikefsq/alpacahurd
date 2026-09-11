package hurd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mikefsq/goalpaca/devicemain"
	"github.com/mikefsq/goalpaca/registry"
)

// Read the installation list on demand, without launching any binaries.
type installedDriver struct {
	Executable string
}

func installedDrivers(cfgPath string) (map[string]installedDriver, error) {
	path := filepath.Join(filepath.Dir(cfgPath), "drivers.conf")
	data, err := os.ReadFile(path)
	drivers := map[string]installedDriver{}
	if os.IsNotExist(err) {
		return drivers, nil
	}
	if err != nil {
		return drivers, err
	}
	var problems []error
	for line, text := range strings.Split(string(data), "\n") {
		executable := strings.TrimSpace(text)
		if executable == "" || strings.HasPrefix(executable, "#") {
			continue
		}
		if !filepath.IsAbs(executable) || !isExecutable(executable) {
			problems = append(problems, fmt.Errorf("%s:%d: installed executable %q is unavailable", path, line+1, executable))
			continue
		}
		name := filepath.Base(executable)
		if prior, ok := drivers[name]; ok && prior.Executable != executable {
			problems = append(problems, fmt.Errorf("%s:%d: duplicate installed driver %q", path, line+1, name))
			continue
		}
		drivers[name] = installedDriver{Executable: executable}
	}
	return drivers, errors.Join(problems...)
}

// newDeviceTemplate obtains schema only. No device construction or launch occurs.
func newDeviceTemplate(ctx context.Context, cfgPath, driver string) (string, error) {
	if drv, ok := registry.Lookup(driver); ok {
		var b strings.Builder
		err := devicemain.WriteCommentedDeviceFile(&b, drv, examplePortBase)
		return b.String(), err
	}
	installed, err := installedDrivers(cfgPath)
	d, ok := installed[driver]
	if !ok {
		if err != nil {
			return "", fmt.Errorf("driver %q is unavailable: %w", driver, err)
		}
		return "", fmt.Errorf("driver %q is not compiled in or registered as installed", driver)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.Executable, "-schema", "commented")
	cmd.WaitDelay = time.Second
	var out, diagnostics checkOutput
	cmd.Stdout, cmd.Stderr = &out, &diagnostics
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s could not provide a configuration template: %w: %s", driver, err, strings.TrimSpace(diagnostics.String()))
	}
	text := out.String()
	clean, err := editorJSON(text)
	if err != nil {
		return "", err
	}
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(clean, &entry); err != nil {
		return "", fmt.Errorf("%s returned an invalid configuration template: %w", driver, err)
	}
	var name string
	var enabled *bool
	if err := json.Unmarshal(entry["driver"], &name); err != nil || strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("%s template must name a driver", driver)
	}
	if err := json.Unmarshal(entry["enable"], &enabled); err != nil || enabled == nil || *enabled {
		return "", fmt.Errorf("%s template must explicitly set enable to false", driver)
	}
	if _, has := entry["exec"]; has {
		return "", fmt.Errorf("%s template must not set an executable override", driver)
	}
	// Retain the driver's comments and bind this instance to the installed path,
	// including binaries whose executable name differs from their driver key.
	start := strings.IndexByte(string(clean), '{')
	if start < 0 {
		return "", fmt.Errorf("%s template must be a JSON object", driver)
	}
	path, _ := json.Marshal(d.Executable)
	return text[:start+1] + "\n  \"exec\": " + string(path) + "," + text[start+1:], nil
}
