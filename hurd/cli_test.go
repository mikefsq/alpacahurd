package hurd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikefsq/goalpaca/devicemain"
	"github.com/mikefsq/goalpaca/registry"
)

// TestExampleConfigIsUsable is the guard on every compiled-in driver's
// ConfigExample: -example (server blocks) plus -example-devices (one disabled
// file per hardware driver) is what install.sh seeds, and the pair must load
// and pass checkConfig with zero errors, every device disabled.
func TestExampleConfigIsUsable(t *testing.T) {
	t.Setenv("ALPACA_STATE_DIR", t.TempDir())
	root := t.TempDir()
	var buf bytes.Buffer
	if err := printExample(&buf, ""); err != nil {
		t.Fatalf("printExample: %v", err)
	}
	path := filepath.Join(root, "hurd.json")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := writeExampleDevicesDir(&out, filepath.Join(root, "devices.d")); err != nil {
		t.Fatalf("writeExampleDevicesDir: %v", err)
	}
	if !strings.Contains(out.String(), "wrote") {
		t.Fatalf("nothing written:\n%s", out.String())
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
		// Every key but driver and enable is commented in a seed, port included,
		// so a loaded seed has no port. -check skips a disabled entry before
		// requiring one.
		if d.Port != 0 {
			t.Errorf("example entry %q has a live port %d; the seed should comment it", d.Driver, d.Port)
		}
		if strings.HasPrefix(d.Driver, "sim-") {
			t.Errorf("example includes %q; sims are requested by name, not seeded", d.Driver)
		}
		if d.Instance != d.Driver {
			t.Errorf("example file for %q should be named after the driver, got instance %q", d.Driver, d.Instance)
		}
	}
	// A seed is usable by uncommenting: enable it and its port line, and it
	// checks ok.
	seed := filepath.Join(root, "devices.d", "sim-focuser.json")
	var sb strings.Builder
	drv, _ := registry.Lookup("sim-focuser")
	if err := devicemain.WriteCommentedDeviceFile(&sb, drv, 11250); err != nil {
		t.Fatal(err)
	}
	txt := strings.Replace(sb.String(), `"enable": false`, `"enable": true`, 1)
	txt = strings.Replace(txt, `// "port": 11250,`, `"port": 11250,`, 1)
	if err := os.WriteFile(seed, []byte(txt), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig(path)
	if err != nil {
		t.Fatalf("uncommented seed does not load: %v", err)
	}
	out.Reset()
	if fatal, errs := checkConfig(&out, cfg); fatal+errs != 0 || !strings.Contains(out.String(), "focuser/0 on port 11250") {
		t.Errorf("uncommented seed should check ok on port 11250:\n%s", out.String())
	}
	out.Reset()
	if fatal, errs := checkConfig(&out, cfg); fatal+errs != 0 {
		t.Fatalf("checkConfig(example) = %d fatal, %d error(s):\n%s", fatal, errs, out.String())
	}
	// A second run keeps every existing file.
	out.Reset()
	if err := writeExampleDevicesDir(&out, filepath.Join(root, "devices.d")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "wrote") || !strings.Contains(out.String(), "keep") {
		t.Errorf("re-run should keep, not overwrite:\n%s", out.String())
	}
}

// TestSingleDriverExample: the per-driver form prints that driver's entry.
func TestSingleDriverExample(t *testing.T) {
	var buf bytes.Buffer
	if err := printExample(&buf, "astrocam"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"driver": "astrocam"`) || !strings.Contains(buf.String(), `"port"`) {
		t.Fatalf("unexpected single-driver example: %s", buf.String())
	}
	if err := printExample(&buf, "not-a-driver"); err == nil {
		t.Fatal("unknown driver name should error")
	}
}

// TestCheckConfigFindsProblems: each class of config mistake is reported as an
// error, and none is fatal: serve skips an entry with an error and serves the
// rest, so a supervisor's pre-start -check must not gate startup on them.
func TestCheckConfigFindsProblems(t *testing.T) {
	cfg := &Config{Devices: []DeviceSpec{
		parseSpec(t, `{"driver":"sim-focuser","port":11200}`),                  // ok
		parseSpec(t, `{"driver":"sim-focuser","enable":false}`),                // skipped (no port needed)
		parseSpec(t, `{"driver":"sim-focuser"}`),                               // missing port
		parseSpec(t, `{"driver":"nope","port":11201}`),                         // unknown driver
		parseSpec(t, `{"driver":"asieaf","port":11202,"serail":"x"}`),          // driver-key typo
		// Sharing a port is legal (this is focuser/1 there), but pinning a number an
		// earlier entry already took is not.
		parseSpec(t, `{"driver":"sim-focuser","port":11200}`),
		parseSpec(t, `{"driver":"sim-focuser","port":11200,"device":0}`),
		parseSpec(t, `{"driver":"sim-focuser","port":11200,"device":-1}`),
	}}
	var out bytes.Buffer
	fatal, errs := checkConfig(&out, cfg)
	const want = 5 // missing port, unknown, typo, pinned dup, negative
	if errs != want {
		t.Fatalf("checkConfig = %d errors, want %d:\n%s", errs, want, out.String())
	}
	if fatal != 0 {
		t.Fatalf("device errors must not be fatal (serve skips those entries), got %d:\n%s", fatal, out.String())
	}
	if !strings.Contains(out.String(), "skipped at start") {
		t.Errorf("the summary should say erroneous entries are skipped at start:\n%s", out.String())
	}
	// The second entry on port 11200 is a legal device 1, not an error.
	if !strings.Contains(out.String(), "focuser/1 on port 11200") {
		t.Errorf("entries sharing a port should number 0,1:\n%s", out.String())
	}
	for _, needle := range []string{"disabled", `"port" is required`, "is already taken by another",
		"is negative", "unknown driver", "serail", "already taken"} {
		if !strings.Contains(out.String(), needle) {
			t.Errorf("checkConfig output missing %q:\n%s", needle, out.String())
		}
	}
}

// TestCheckConfigFatalListen: a "listen" entry that resolves to nothing stops
// serve before any device exists, so it is the one thing -check exits non-zero
// for with a loaded config.
func TestCheckConfigFatalListen(t *testing.T) {
	cfg := &Config{
		Listen:  []string{"no-such-interface-0"},
		Devices: []DeviceSpec{parseSpec(t, `{"driver":"sim-focuser","port":11200}`)},
	}
	var out bytes.Buffer
	fatal, errs := checkConfig(&out, cfg)
	if fatal != 1 || errs != 0 {
		t.Fatalf("fatal, errors = %d, %d, want 1, 0:\n%s", fatal, errs, out.String())
	}
	if !strings.Contains(out.String(), "fatal") || !strings.Contains(out.String(), "no-such-interface-0") {
		t.Errorf("the fatal line should name the listen entry:\n%s", out.String())
	}
}

// TestCheckConfigIgnoresFrontEndKeys: the keys of the removed INDI and LX200
// front-ends ("indi", "lx200Port") stay common keys, so an old entry carrying
// them checks clean — they never reach the driver's strict decode.
func TestCheckConfigIgnoresFrontEndKeys(t *testing.T) {
	cfg := &Config{Devices: []DeviceSpec{
		parseSpec(t, `{"driver":"sim-focuser","port":11200,"indi":true,"lx200Port":4040}`),
	}}
	var out bytes.Buffer
	if fatal, errs := checkConfig(&out, cfg); fatal+errs != 0 {
		t.Fatalf("front-end keys should be ignored, not errors:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "focuser/0 on port 11200") {
		t.Errorf("the entry should check ok:\n%s", out.String())
	}
}
