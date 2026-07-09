package hurd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

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
// hardware driver's ConfigExample (each entry disabled, on a sequential port —
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

	var entries []string
	port := examplePortBase
	for _, d := range registry.All() {
		if strings.HasPrefix(d.Name, "sim-") {
			continue // sims are listed by -drivers; ask for one by name
		}
		entry, err := exampleEntry(d, port, true)
		if err != nil {
			return err
		}
		entries = append(entries, "    "+entry)
		port++
	}
	out := "{\n" +
		"  \"discovery\": \"direct\",\n" +
		"  \"indi\":  { \"enable\": false, \"port\": 7624 },\n" +
		"  \"lx200\": { \"enable\": false, \"basePort\": 4030 },\n" +
		"  \"devices\": [\n" +
		strings.Join(entries, ",\n") + "\n" +
		"  ]\n" +
		"}"
	if !json.Valid([]byte(out)) {
		return fmt.Errorf("assembled example config is not valid JSON (a driver's ConfigExample is malformed)")
	}
	fmt.Fprintln(w, out)
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
// is touched — construction only binds identities). It prints one line per
// device and returns the number of errors; systemd runs this as ExecStartPre so
// a bad config fails fast with a readable journal message.
func checkConfig(w io.Writer, cfg *Config) int {
	errs := 0
	fail := func(spec DeviceSpec, format string, args ...any) {
		fmt.Fprintf(w, "error  %-22s %s\n", spec.Driver, fmt.Sprintf(format, args...))
		errs++
	}

	ports := map[int]string{}    // port -> driver, for duplicate detection
	indiNames := map[string]bool{} // INDI ids must be unique on the hub
	enabled := 0
	for _, spec := range cfg.Devices {
		if !spec.enabled() {
			fmt.Fprintf(w, "skip   %-22s disabled\n", spec.Driver)
			continue
		}
		enabled++
		if spec.Port == 0 {
			fail(spec, `"port" is required`)
		} else if prev, dup := ports[spec.Port]; dup {
			fail(spec, "port %d already used by %s", spec.Port, prev)
		} else {
			ports[spec.Port] = spec.Driver
		}

		drv, dev, err := buildDevice(spec)
		if err != nil {
			fail(spec, "%v", err)
			continue
		}

		if spec.LX200Port != 0 && !isLiveMounter(dev) {
			fail(spec, `sets "lx200Port" but is not a mount`)
		}
		if spec.indiEnabled() {
			if !isLiveMounter(dev) && !isLiveCamera(dev) {
				fmt.Fprintf(w, "warn   %-22s \"indi\": true but the device is not INDI-capable (ignored)\n", spec.Driver)
			} else if name := indiName(spec); indiNames[name] {
				fail(spec, "INDI name %q is already taken (INDI ids must be unique; set \"name\")", name)
			} else {
				indiNames[name] = true
			}
		}

		fmt.Fprintf(w, "ok     %-22s %s/0 on port %d  %q\n", spec.Driver, drv.Type, spec.Port, dev.Name())
	}

	if enabled == 0 {
		fmt.Fprintf(w, "warn   no enabled devices (the server will start and idle)\n")
	}
	fmt.Fprintf(w, "%d device(s) enabled, %d error(s)\n", enabled, errs)
	return errs
}
