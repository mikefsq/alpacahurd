package hurd

import (
	"fmt"
	"os"
	"strings"
)

// launchArgv resolves the command line for one device entry running as a
// separate binary: the entry named instance in cfg, its driver resolved to an
// installed binary, launched in register discovery mode with its device file.
// This is what `alpacahurd -launch <instance>` runs, and what every platform
// supervisor's record points at, so the record never names a driver binary.
func launchArgv(cfg *Config, cfgPath, instance string) (exe string, args []string, err error) {
	for _, spec := range cfg.Devices {
		if spec.Instance != instance {
			continue
		}
		res := resolveDriver(spec)
		switch res.kind {
		case installedBinary:
			return res.exe, res.args, nil
		case compiledIn:
			return "", nil, fmt.Errorf("device %q: driver %q is compiled into alpacahurd and runs inside it; there is no separate binary to launch", instance, spec.Driver)
		}
		return "", nil, fmt.Errorf("device %q: driver %q is not compiled in and no binary was found", instance, spec.Driver)
	}
	return "", nil, fmt.Errorf("no device %q in %s (the instance is a devices.d file's stem)", instance, devicesDirFor(cfgPath))
}

// launch runs one device as a separate process for a supervisor: it resolves
// the entry and, on Unix, replaces this process with the driver binary so the
// supervisor's main PID is the driver's; on Windows it stays as the service
// host and runs the driver as its child (see launch_windows.go).
func launch(cfgPath string, cfg *Config, instance string) error {
	exe, args, err := launchArgv(cfg, cfgPath, instance)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "alpacahurd: launching %s: %s %s\n", instance, exe, strings.Join(args, " "))
	return execDriver(exe, args)
}
