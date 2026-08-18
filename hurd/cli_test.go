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
	if errs := checkConfig(&out, cfg); errs != 0 || !strings.Contains(out.String(), "focuser/0 on port 11250") {
		t.Errorf("uncommented seed should check ok on port 11250:\n%s", out.String())
	}
	out.Reset()
	if errs := checkConfig(&out, cfg); errs != 0 {
		t.Fatalf("checkConfig(example) = %d error(s):\n%s", errs, out.String())
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
// error (checkConfig is the systemd ExecStartPre gate, so these must fail fast).
func TestCheckConfigFindsProblems(t *testing.T) {
	cfg := &Config{Devices: []DeviceSpec{
		parseSpec(t, `{"driver":"sim-focuser","port":11200}`),                  // ok
		parseSpec(t, `{"driver":"sim-focuser","enable":false}`),                // skipped (no port needed)
		parseSpec(t, `{"driver":"sim-focuser"}`),                               // missing port
		parseSpec(t, `{"driver":"nope","port":11201}`),                         // unknown driver
		parseSpec(t, `{"driver":"asieaf","port":11202,"serail":"x"}`),          // driver-key typo
		parseSpec(t, `{"driver":"sim-focuser","port":11203,"lx200Port":4040}`), // lx200Port on a non-mount
		// Sharing a port is legal (this is focuser/1 there), but pinning a number an
		// earlier entry already took is not.
		parseSpec(t, `{"driver":"sim-focuser","port":11200}`),
		parseSpec(t, `{"driver":"sim-focuser","port":11200,"device":0}`),
		parseSpec(t, `{"driver":"sim-focuser","port":11200,"device":-1}`),
		// Two INDI mounts falling back to the same explicit name collide on the hub.
		parseSpec(t, `{"driver":"sim-telescope","port":11204,"name":"M","indi":true}`),
		parseSpec(t, `{"driver":"sim-telescope","port":11205,"name":"M","indi":true}`),
	}}
	var out bytes.Buffer
	errs := checkConfig(&out, cfg)
	const want = 7 // missing port, unknown, typo, lx200Port, pinned dup, negative, INDI name
	if errs != want {
		t.Fatalf("checkConfig = %d errors, want %d:\n%s", errs, want, out.String())
	}
	// The second entry on port 11200 is a legal device 1, not an error.
	if !strings.Contains(out.String(), "focuser/1 on port 11200") {
		t.Errorf("entries sharing a port should number 0,1:\n%s", out.String())
	}
	for _, needle := range []string{"disabled", `"port" is required`, "is already taken by another",
		"is negative", "unknown driver", "serail", "lx200Port", "already taken"} {
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
