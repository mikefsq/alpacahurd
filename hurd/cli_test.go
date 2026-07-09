package hurd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExampleConfigIsUsable is the guard on every compiled-in driver's
// ConfigExample: the assembled -example output (what install.sh seeds
// /etc/alpacahurd/hurd.json with) must load, and every entry must be disabled
// and pass checkConfig with zero errors.
func TestExampleConfigIsUsable(t *testing.T) {
	var buf bytes.Buffer
	if err := printExample(&buf, ""); err != nil {
		t.Fatalf("printExample: %v", err)
	}
	path := filepath.Join(t.TempDir(), "hurd.json")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("assembled example does not load: %v", err)
	}
	if len(cfg.Devices) == 0 {
		t.Fatal("assembled example has no devices")
	}
	for _, d := range cfg.Devices {
		if d.enabled() {
			t.Errorf("example entry %q is enabled; the seed config must start all-disabled", d.Driver)
		}
		if d.Port == 0 {
			t.Errorf("example entry %q has no port", d.Driver)
		}
		if strings.HasPrefix(d.Driver, "sim-") {
			t.Errorf("example includes %q; sims are requested by name, not seeded", d.Driver)
		}
	}
	var out bytes.Buffer
	if errs := checkConfig(&out, cfg); errs != 0 {
		t.Fatalf("checkConfig(example) = %d error(s):\n%s", errs, out.String())
	}
}

// TestSingleDriverExample: the per-driver form prints that driver's entry.
func TestSingleDriverExample(t *testing.T) {
	var buf bytes.Buffer
	if err := printExample(&buf, "asicam"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"driver": "asicam"`) || !strings.Contains(buf.String(), `"port"`) {
		t.Fatalf("unexpected single-driver example: %s", buf.String())
	}
	if err := printExample(&buf, "not-a-driver"); err == nil {
		t.Fatal("unknown driver name should error")
	}
}

// TestCheckConfigFindsProblems: each class of config mistake is reported as an
// error (checkConfig is the systemd ExecStartPre gate, so these must fail fast).
func TestCheckConfigFindsProblems(t *testing.T) {
	cfg := &Config{Devices: []DeviceSpec{
		parseSpec(t, `{"driver":"sim-focuser","port":11200}`),                  // ok
		parseSpec(t, `{"driver":"sim-focuser","enable":false}`),                // skipped (no port needed)
		parseSpec(t, `{"driver":"sim-focuser"}`),                               // missing port
		parseSpec(t, `{"driver":"sim-focuser","port":11200}`),                  // duplicate port
		parseSpec(t, `{"driver":"nope","port":11201}`),                         // unknown driver
		parseSpec(t, `{"driver":"asieaf","port":11202,"serail":"x"}`),          // driver-key typo
		parseSpec(t, `{"driver":"sim-focuser","port":11203,"lx200Port":4040}`), // lx200Port on a non-mount
		// Two INDI mounts falling back to the same explicit name collide on the hub.
		parseSpec(t, `{"driver":"sim-telescope","port":11204,"name":"M","indi":true}`),
		parseSpec(t, `{"driver":"sim-telescope","port":11205,"name":"M","indi":true}`),
	}}
	var out bytes.Buffer
	errs := checkConfig(&out, cfg)
	const want = 6 // missing port, dup port, unknown, typo, lx200Port, INDI name
	if errs != want {
		t.Fatalf("checkConfig = %d errors, want %d:\n%s", errs, want, out.String())
	}
	for _, needle := range []string{"disabled", `"port" is required`, "already used", "unknown driver",
		"serail", "lx200Port", "already taken"} {
		if !strings.Contains(out.String(), needle) {
			t.Errorf("checkConfig output missing %q:\n%s", needle, out.String())
		}
	}
}

// TestCheckConfigWarnsIndiIncapable: "indi": true on a device that can't join
// the hub is a warning, not an error (the herd still runs; the flag is ignored).
func TestCheckConfigWarnsIndiIncapable(t *testing.T) {
	cfg := &Config{Devices: []DeviceSpec{
		parseSpec(t, `{"driver":"sim-focuser","port":11200,"indi":true}`),
	}}
	var out bytes.Buffer
	if errs := checkConfig(&out, cfg); errs != 0 {
		t.Fatalf("INDI-incapable device should warn, not error:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "not INDI-capable") {
		t.Errorf("expected an INDI-capability warning:\n%s", out.String())
	}
}
