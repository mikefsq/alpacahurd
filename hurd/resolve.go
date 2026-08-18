package hurd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mikefsq/goalpaca/registry"
)

// A driver name resolves at runtime to one of three things, and the layout a
// deployment uses is whatever the resolution says: some drivers compiled in,
// some as installed binaries, in any mix.
//
//  1. Compiled in: the name is in the driver registry, so the orchestrator
//     constructs the device in process.
//  2. An installed binary: the entry names one with "exec", or a binary named
//     for the driver is on PATH or in the orchestrator's own directory. The
//     supervisor launches it with the entry's file and the register discovery
//     mode.
//  3. Unresolved: neither, which -check reports and serve skips.
//
// Compiled in wins when both exist, so a driver moves to a separate binary by
// removing it from hurd.conf.

// resolution says how a device entry's driver will run.
type resolution struct {
	kind resolutionKind
	// exe and args are set for a binary: the executable path and the arguments
	// the supervisor launches it with.
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

// resolveDriver decides how spec's driver runs. adminPath is the entry's
// device file, passed to a binary as -config; an inline entry has none and can
// only be compiled in.
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

// findBinary locates the executable for a driver: the entry's "exec" key when
// set, else a file named for the driver beside the orchestrator's own binary,
// else one on PATH. It returns "" when none is found or the candidate is not
// executable.
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
