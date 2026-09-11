package hurd

import (
	"fmt"
	"os"
	"strings"
)

// launchArgv resolves an instance to a driver binary in register discovery mode.
func launchArgv(cfg *Config, cfgPath, instance string) (exe string, args []string, err error) {
	for _, spec := range cfg.Devices {
		if spec.Instance != instance {
			continue
		}
		// The parent skips a disabled entry when it decides which units to start, but nothing
		// stops systemd from starting one anyway — a restart job queued before the entry was
		// switched off, a hand-typed `systemctl start`, the template's WantedBy at boot. Refusing
		// here is what makes `enable` mean the same thing on every path into the device.
		if !spec.enabled() {
			return "", nil, fmt.Errorf("device %q is disabled in %s; enable it before launching", instance, spec.Source)
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

// launch runs an instance through the platform driver launcher.
func launch(cfgPath string, cfg *Config, instance string) error {
	exe, args, err := launchArgv(cfg, cfgPath, instance)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "alpacahurd: launching %s: %s %s\n", instance, exe, strings.Join(args, " "))
	return execDriver(exe, args)
}
