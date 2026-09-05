package hurd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mikefsq/goalpaca/registry"
)

// resolution says how a device entry's driver will run.
type resolution struct {
	kind resolutionKind
	// exe and args are the command for an installed binary.
	exe  string
	args []string
}

type resolutionKind int

const (
	unresolved resolutionKind = iota
	compiledIn
	installedBinary
)

func (k resolutionKind) String() string {
	switch k {
	case compiledIn:
		return "compiled in"
	case installedBinary:
		return "installed binary"
	}
	return "unresolved"
}

// resolveDriver prefers a compiled-in driver, then an installed binary.
// Only device-file entries can use installed binaries.
func resolveDriver(spec DeviceSpec) resolution {
	if _, ok := registry.Lookup(spec.Driver); ok {
		return resolution{kind: compiledIn}
	}
	exe := findBinary(spec)
	if exe == "" {
		return resolution{kind: unresolved}
	}
	args := []string{"-discovery", "register"}
	if spec.Source != "" && spec.Instance != "" {
		args = append(args, "-config", spec.Source)
	}
	return resolution{kind: installedBinary, exe: exe, args: args}
}

// findBinary checks exec, then the driver name beside alpacahurd and on PATH.
// It returns an empty string if no executable is found.
func findBinary(spec DeviceSpec) string {
	if e := execKey(spec); e != "" {
		if p, err := exec.LookPath(e); err == nil {
			return p
		}
		if isExecutable(e) {
			return e
		}
		return ""
	}
	name := spec.Driver
	if self, err := os.Executable(); err == nil {
		if p := filepath.Join(filepath.Dir(self), name); isExecutable(p) {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// execKey reads the optional "exec" key from the entry's raw JSON.
func execKey(spec DeviceSpec) string {
	var m struct {
		Exec string `json:"exec"`
	}
	_ = json.Unmarshal(spec.Raw, &m)
	return strings.TrimSpace(m.Exec)
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode()&0o111 != 0
}
