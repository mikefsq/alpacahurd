package hurd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/mikefsq/goalpaca/devicemain"
	"github.com/mikefsq/goalpaca/registry"
)

// printDrivers lists every driver compiled into this binary.
func printDrivers(w io.Writer) {
	tw := tabwriter.NewWriter(w, 2, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "DRIVER\tTYPE\tDESCRIPTION")
	for _, d := range registry.All() {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", d.Name, d.Type, d.Description)
	}
	tw.Flush()
}

// printExample writes a starter config assembled from every compiled-in
// hardware driver's ConfigExample (each entry disabled, on a sequential port;
// flip "enable" and fill in your identifiers), or a single driver's entry when
// name is given. install.sh uses the full form to seed /etc/alpacahurd/hurd.json.
func printExample(w io.Writer, name string) error {
	if name != "" {
		d, ok := registry.Lookup(name)
		if !ok {
			return fmt.Errorf("unknown driver %q (alpacahurd -drivers lists the compiled-in set)", name)
		}
		entry, err := exampleEntry(d, examplePortBase, false)
		if err != nil {
			return err
		}
		fmt.Fprintln(w, entry)
		return nil
	}
	// Server blocks only. Devices live in devices.d beside this file, one per
	// file; writeExampleDevicesDir seeds that directory.
	out := "{\n" +
		"  \"discovery\": \"direct\",\n" +
		"  \"devices\": []\n" +
		"}"
	fmt.Fprintln(w, out)
	return nil
}

// writeExampleDevicesDir seeds dir with one disabled device file per compiled-in
// hardware driver, <driver>.json, each holding the driver's ConfigExample plus a
// sequential port. Existing files are left alone, so a re-run adds files for
// newly compiled-in drivers without touching an admin's edits. It reports what
// it wrote. Sims are skipped, as with -example; ask for one by name.
func writeExampleDevicesDir(w io.Writer, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	port := examplePortBase
	for _, d := range registry.All() {
		if strings.HasPrefix(d.Name, "sim-") {
			continue
		}
		path := filepath.Join(dir, d.Name+".json")
		thisPort := port
		port++
		if _, err := os.Stat(path); err == nil {
			fmt.Fprintf(w, "keep   %s\n", path)
			continue
		}
		// The seed documents every key at its default, all commented, so it
		// changes nothing until a line is uncommented; driver and enable:false
		// are the only live keys.
		var b strings.Builder
		if err := devicemain.WriteCommentedDeviceFile(&b, d, thisPort); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(w, "wrote  %s\n", path)
	}
	return nil
}

// examplePortBase is where assembled example configs start numbering Alpaca ports.
const examplePortBase = 11200

// exampleEntry splices a "port" (and optionally "enable": false) into a
// driver's ConfigExample, preserving the author's key order.
func exampleEntry(d registry.Driver, port int, disabled bool) (string, error) {
	s := strings.TrimSpace(d.ConfigExample)
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return "", fmt.Errorf("driver %s: ConfigExample is not a JSON object: %q", d.Name, d.ConfigExample)
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	extra := fmt.Sprintf(`"port": %d`, port)
	if disabled {
		extra += `, "enable": false`
	}
	entry := "{ " + extra + " }"
	if inner != "" {
		entry = "{ " + inner + ", " + extra + " }"
	}
	if !json.Valid([]byte(entry)) {
		return "", fmt.Errorf("driver %s: ConfigExample is not valid JSON: %q", d.Name, d.ConfigExample)
	}
	return entry, nil
}

// checkConfig validates cfg by constructing every enabled device (no hardware
// is touched; construction only binds identities). It prints one line per
// device and returns two counts. fatal counts what stops the server itself,
// such as an unusable "listen". errs counts per-device problems, which serve
// skips while serving the rest, so -check's exit gates on fatal alone. A
// supervisor runs -check as a pre-start step for the readable report; one bad
// device file must not keep the herd down.
func checkConfig(w io.Writer, cfg *Config) (fatal, errs int) {
	fail := func(spec DeviceSpec, format string, args ...any) {
		fmt.Fprintf(w, "error  %-22s %s\n", spec.Driver, fmt.Sprintf(format, args...))
		errs++
	}

	// The one whole-server check: serve resolves "listen" before anything else
	// and cannot start when it does not resolve.
	if _, _, err := resolveListen(cfg.Listen); err != nil {
		fmt.Fprintf(w, "fatal  %-22s %v\n", "listen", err)
		fatal++
	}

	// Device numbers are per port, assigned as serve assigns them, so -check
	// reports the addresses the hurd will serve.
	nums := map[int]*deviceNumbers{}
	enabled := 0
	for _, spec := range cfg.Devices {
		if !spec.enabled() {
			fmt.Fprintf(w, "skip   %-22s disabled\n", spec.Driver)
			continue
		}
		enabled++
		if spec.Port == 0 && spec.Instance == "" {
			fail(spec, `"port" is required`)
		}

		switch res := resolveDriver(spec); res.kind {
		case compiledIn:
		case installedBinary:
			fmt.Fprintf(w, "ok     %-22s separate binary %s %s\n", spec.Driver, res.exe, strings.Join(res.args, " "))
			continue
		default:
			if spec.Instance != "" {
				// An unresolvable devices.d fragment is a warning: serve skips it
				// and keeps starting, so -check must not gate startup on it.
				fmt.Fprintf(w, "warn   %-22s %s: driver is not compiled in and no binary was found; the entry will be skipped\n", spec.Driver, spec.Source)
				continue
			}
		}
		// A multi-device entry (a driver with a MultiKey) checks one device per
		// block; a flat entry is itself.
		subs, serr := subSpecs(spec)
		if serr != nil {
			fail(spec, "%v", serr)
			continue
		}
		for _, sub := range subs {
			if !sub.enabled() {
				fmt.Fprintf(w, "skip   %-22s device %d disabled\n", sub.Driver, *sub.Block)
				continue
			}
			drv, dev, err := buildDevice(sub)
			if err != nil {
				fail(sub, "%v", err)
				continue
			}
			if nums[sub.Port] == nil {
				nums[sub.Port] = &deviceNumbers{}
			}
			num, err := nums[sub.Port].assign(sub, drv.Type)
			if err != nil {
				fail(sub, "%v", err)
				continue
			}

			if sub.Port == 0 {
				fmt.Fprintf(w, "ok     %-22s %s/%d on a scanned port (from %d; recorded in the state file at first start)  %q\n", sub.Driver, drv.Type, num, portScanBase, dev.Name())
			} else {
				fmt.Fprintf(w, "ok     %-22s %s/%d on port %d  %q\n", sub.Driver, drv.Type, num, sub.Port, dev.Name())
			}
		}
	}

	if enabled == 0 {
		fmt.Fprintf(w, "warn   no enabled devices (the server will start and idle)\n")
	}
	fmt.Fprintf(w, "%d device(s) enabled, %d error(s)\n", enabled, errs)
	if errs > 0 {
		fmt.Fprintf(w, "an entry with an error is skipped at start; the rest of the herd serves\n")
	}
	return fatal, errs
}
