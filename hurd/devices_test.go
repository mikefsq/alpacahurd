package hurd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mikefsq/goalpaca/devicemain"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// loadDevicesDir reads one entry per file in name order, with the filename stem
// as the instance, and tolerates a missing directory.
func TestLoadDevicesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "devices.d")
	writeFile(t, filepath.Join(dir, "b-guide.json"), `{"driver":"astrocam","port":11201,"serial":"bbbb"}`)
	writeFile(t, filepath.Join(dir, "a-main.json"), `{"driver":"astrocam","port":11201,"serial":"aaaa","fixdefects":true}`)
	writeFile(t, filepath.Join(dir, "notes.txt"), "ignored")

	specs, err := loadDevicesDir(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 || specs[0].Instance != "a-main" || specs[1].Instance != "b-guide" {
		t.Fatalf("got %d specs, instances %q %q", len(specs), inst(specs, 0), inst(specs, 1))
	}
	if specs[0].Driver != "astrocam" || specs[0].Port != 11201 {
		t.Errorf("common keys not decoded: %+v", specs[0].deviceCommon)
	}
	if !specs[0].Pinned["serial"] || !specs[0].Pinned["fixdefects"] || specs[0].Pinned["driver"] || specs[0].Pinned["port"] {
		t.Errorf("pinned = %v, want serial and fixdefects only", specs[0].Pinned)
	}
	if !strings.HasSuffix(specs[0].Source, "a-main.json") {
		t.Errorf("source = %q", specs[0].Source)
	}

	// Missing dir: no entries, no error.
	if specs, err := loadDevicesDir(filepath.Join(t.TempDir(), "nope"), ""); err != nil || len(specs) != 0 {
		t.Errorf("missing dir: %v, %v", specs, err)
	}
	// A malformed file is an error naming the file.
	writeFile(t, filepath.Join(dir, "c-bad.json"), `{not json`)
	if _, err := loadDevicesDir(dir, ""); err == nil || !strings.Contains(err.Error(), "c-bad.json") {
		t.Errorf("malformed file: err = %v", err)
	}
}

func inst(s []DeviceSpec, i int) string {
	if i < len(s) {
		return s[i].Instance
	}
	return "<none>"
}

// The state file fills keys the admin file left unset and never overrides one
// it named. Pinned keys are the admin's driver-owned keys.
func TestOverlayPrecedence(t *testing.T) {
	root := t.TempDir()
	admin := filepath.Join(root, "etc", "devices.d")
	state := filepath.Join(root, "var", "devices")
	writeFile(t, filepath.Join(admin, "cam.json"), `{"driver":"astrocam","port":11201,"serial":"aaaa","fixdefects":true}`)
	writeFile(t, filepath.Join(state, "cam.json"), `{"fixdefects":false,"fpsPercent":60,"serial":"STALE"}`)

	specs, err := loadDevicesDir(admin, state)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 {
		t.Fatalf("got %d specs", len(specs))
	}
	raw := string(specs[0].Raw)
	// admin only: driver, port, serial keep the admin value.
	if !strings.Contains(raw, `"serial":"aaaa"`) {
		t.Errorf("admin serial overridden by state: %s", raw)
	}
	// both: admin wins.
	if !strings.Contains(raw, `"fixdefects":true`) {
		t.Errorf("admin fixdefects overridden by state: %s", raw)
	}
	// state only: taken.
	if !strings.Contains(raw, `"fpsPercent":60`) {
		t.Errorf("state-only key not merged: %s", raw)
	}
	if !specs[0].Pinned["serial"] || !specs[0].Pinned["fixdefects"] || specs[0].Pinned["fpsPercent"] {
		t.Errorf("pinned = %v", specs[0].Pinned)
	}
	// No state file at all is fine.
	writeFile(t, filepath.Join(admin, "cam2.json"), `{"driver":"astrocam","port":11202}`)
	if specs, err := loadDevicesDir(admin, state); err != nil || len(specs) != 2 {
		t.Errorf("without a state file: %d specs, %v", len(specs), err)
	}
}

// LoadConfig merges the inline devices array with devices.d beside the file,
// inline entries first.
func TestLoadConfigMergesInlineAndDevicesDir(t *testing.T) {
	t.Setenv("ALPACA_STATE_DIR", t.TempDir())
	root := t.TempDir()
	cfg := filepath.Join(root, "hurd.json")
	writeFile(t, cfg, `{"discovery":"off","devices":[{"driver":"sim-focuser","port":11300,"name":"Inline"}]}`)
	writeFile(t, filepath.Join(root, "devices.d", "cam.json"), `{"driver":"sim-camera","port":11301,"name":"FromDir"}`)

	c, err := LoadConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Devices) != 2 {
		t.Fatalf("got %d devices, want 2", len(c.Devices))
	}
	if c.Devices[0].Name != "Inline" || c.Devices[0].Instance != "" || c.Devices[0].Source != cfg {
		t.Errorf("inline entry: %+v", c.Devices[0])
	}
	if c.Devices[1].Name != "FromDir" || c.Devices[1].Instance != "cam" {
		t.Errorf("devices.d entry: %+v", c.Devices[1])
	}
	// The inline entry's driver keys are all pinned; it has no state overlay.
	if c.Devices[0].Pinned == nil {
		t.Error("inline entry should have pinned keys computed")
	}
}

// A pinned key from the admin file renders locked with the file named, and a
// submit that names it is dropped; a state-only key is editable and persists to
// the state file.
func TestDevicesDirPinnedKeysOnSetupPage(t *testing.T) {
	stateRoot := t.TempDir()
	t.Setenv("ALPACA_STATE_DIR", stateRoot)
	root := t.TempDir()
	cfg := filepath.Join(root, "hurd.json")
	writeFile(t, cfg, `{"discovery":"off"}`)
	adminFile := filepath.Join(root, "devices.d", "main.json")
	writeFile(t, adminFile, `{"driver":"astrocam","port":11201,"serial":"aaaa","fixdefects":true}`)

	c, err := LoadConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Devices) != 1 {
		t.Fatalf("got %d devices", len(c.Devices))
	}
	s := newHurdServer(t, c.Devices[0])
	r, _ := http.Get(s + "/setup/v1/camera/0/setup")
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	body := string(b)
	if !strings.Contains(body, "set in "+adminFile) {
		t.Errorf("locked note should name the admin file:\n%s", body)
	}
	if strings.Count(body, "set in "+adminFile) != 2 {
		t.Errorf("serial and fixdefects should both be pinned")
	}
	// Submit fpsPercent (editable) alongside serial (pinned): serial is dropped.
	resp, err := http.PostForm(s+"/setup/v1/camera/0/setup", map[string][]string{"fpsPercent": {"55"}, "serial": {"zzzz"}, "fixdefects": {"false"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "Settings applied and saved") {
		t.Fatalf("apply:\n%s", b)
	}
	stateFile := filepath.Join(stateRoot, "devices", "main.json")
	sb, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("state file not written at %s: %v", stateFile, err)
	}
	// The generated form persists typed JSON, so the value is a number, and it
	// decodes under the driver's typed Config at the next start.
	if !strings.Contains(string(sb), `"fpsPercent": 55`) {
		t.Errorf("state file should hold a typed fpsPercent:\n%s", sb)
	}
	if strings.Contains(string(sb), "zzzz") || strings.Contains(string(sb), `"serial"`) {
		t.Errorf("pinned serial leaked into state:\n%s", sb)
	}
	// Reload: the state value overlays, the pinned value stands.
	c2, err := LoadConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(c2.Devices[0].Raw)
	if !strings.Contains(raw, `"fpsPercent":55`) || !strings.Contains(raw, `"serial":"aaaa"`) {
		t.Errorf("reload overlay wrong: %s", raw)
	}
	// And the driver's strict decode accepts the overlaid entry.
	if _, _, err := buildDevice(c2.Devices[0]); err != nil {
		t.Errorf("driver rejected the overlaid config: %v", err)
	}
}

// newHurdServer registers one loaded DeviceSpec the way serve does, with
// persistence on, and returns the live base URL. Unlike serveSpec it keeps the
// spec's Instance, Pinned, and Source, so it exercises the devices.d path.
func newHurdServer(t *testing.T, spec DeviceSpec) string {
	t.Helper()
	srv := alpacadev.New(alpacadev.Config{
		Discovery:  alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff},
		ServerName: serverName, Manufacturer: "t",
		Settings: alpacadev.NewFileStore(),
	})
	if _, _, err := registerDevice(srv, spec, &deviceNumbers{}); err != nil {
		t.Fatalf("registerDevice: %v", err)
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	t.Cleanup(ts.Close)
	return ts.URL
}

// A devices.d fragment naming a driver that is not compiled in is a warning
// from -check, not an error: serve skips it and keeps starting. The same
// unknown driver in the inline array stays an error, since that file is the
// admin's own and -check exists to catch it.
func TestUnresolvableFragmentIsWarning(t *testing.T) {
	t.Setenv("ALPACA_STATE_DIR", t.TempDir())
	root := t.TempDir()
	cfg := filepath.Join(root, "hurd.json")
	writeFile(t, cfg, `{"discovery":"off","devices":[{"driver":"nope-inline","port":11400}]}`)
	writeFile(t, filepath.Join(root, "devices.d", "orphan.json"), `{"driver":"nope-removed","port":11401}`)
	writeFile(t, filepath.Join(root, "devices.d", "ok.json"), `{"driver":"sim-focuser","port":11402}`)
	c, err := LoadConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	fatal, errs := checkConfig(&out, c)
	if fatal != 0 || errs != 1 {
		t.Errorf("fatal, errors = %d, %d, want 0, 1 (the inline unknown driver):\n%s", fatal, errs, out.String())
	}
	if !strings.Contains(out.String(), "warn") || !strings.Contains(out.String(), "orphan.json") {
		t.Errorf("fragment should warn and name the file:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "focuser/0 on port 11402") {
		t.Errorf("the good fragment should still check ok:\n%s", out.String())
	}
}

// A devices.d entry with no port scans from portScanBase; the bound port is
// written to its state file once, read back over the entry at the next load,
// and never overwritten by a later scan.
func TestScannedPortPersists(t *testing.T) {
	stateRoot := t.TempDir()
	t.Setenv("ALPACA_STATE_DIR", stateRoot)
	root := t.TempDir()
	cfg := filepath.Join(root, "hurd.json")
	writeFile(t, cfg, `{"discovery":"off"}`)
	writeFile(t, filepath.Join(root, "devices.d", "scanme.json"), `{"driver":"sim-focuser"}`)

	c, err := LoadConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	spec := c.Devices[0]
	if spec.Port != 0 {
		t.Fatalf("entry should have no port, has %d", spec.Port)
	}
	// -check accepts it.
	var out strings.Builder
	if fatal, errs := checkConfig(&out, c); fatal+errs != 0 || !strings.Contains(out.String(), "scanned port") {
		t.Errorf("-check on a scanning entry:\n%s", out.String())
	}

	// Run a scanning server the way serve does and persist its port.
	srv := alpacadev.New(alpacadev.Config{AlpacaPort: 0, PortScanBase: portScanBase, PortScanLimit: 20,
		Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: serverName})
	if _, _, err := registerDevice(srv, spec, &deviceNumbers{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- srv.Run(ctx) }()
	ports, err := waitBound(ctx, []*alpacadev.Server{srv}, errc)
	if err != nil {
		t.Fatal(err)
	}
	if ports[0] < portScanBase || ports[0] >= portScanBase+20 {
		t.Fatalf("bound %d, want a scanned port from %d", ports[0], portScanBase)
	}
	persistBoundPorts([]boundEntry{{spec: spec, port: srv.Port()}})
	stateFile := filepath.Join(stateRoot, "devices", "scanme.json")
	sb, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	if !strings.Contains(string(sb), `"port": `+strconv.Itoa(ports[0])) {
		t.Errorf("state should record port %d:\n%s", ports[0], sb)
	}

	// The next load sees the port through the overlay, so the entry no longer
	// scans and the driver decodes it.
	c2, err := LoadConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Devices[0].Port != ports[0] {
		t.Errorf("reloaded port = %d, want %d", c2.Devices[0].Port, ports[0])
	}
	if c2.Devices[0].Pinned["port"] {
		t.Error("a state-supplied port must not be pinned; the page and the orchestrator may change it")
	}

	// Persisting the same bound port again leaves the file untouched.
	before, _ := os.ReadFile(stateFile)
	persistBoundPorts([]boundEntry{{spec: spec, port: ports[0]}})
	after, _ := os.ReadFile(stateFile)
	if string(after) != string(before) {
		t.Errorf("unchanged port rewrote the file:\nbefore %s\nafter  %s", before, after)
	}
	// State describes what is: when the device binds a different port (the admin
	// pinned one), the stale recorded port is replaced, not left to mislead.
	persistBoundPorts([]boundEntry{{spec: spec, port: 11999}})
	sb2, _ := os.ReadFile(stateFile)
	if !strings.Contains(string(sb2), `"port": 11999`) {
		t.Errorf("stale port should be replaced by the bound one:\n%s", sb2)
	}
}

// resolveDriver: compiled in wins; a binary is found by exec, beside the
// orchestrator, or on PATH; else unresolved. An inline entry with an unknown
// driver stays fatal to -check; a devices.d one warns.
func TestResolveDriver(t *testing.T) {
	// Compiled in.
	if r := resolveDriver(parseSpec(t, `{"driver":"sim-focuser","port":1}`)); r.kind != compiledIn {
		t.Errorf("sim-focuser: %v", r.kind)
	}
	// Installed binary by exec: any executable file.
	bin := filepath.Join(t.TempDir(), "mywidget")
	os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	spec := parseSpec(t, `{"driver":"mywidget","exec":"`+bin+`","port":1}`)
	spec.Instance, spec.Source = "w1", "/etc/x/devices.d/w1.json"
	r := resolveDriver(spec)
	if r.kind != installedBinary || r.exe != bin {
		t.Fatalf("exec: %+v", r)
	}
	if strings.Join(r.args, " ") != "-discovery register -config /etc/x/devices.d/w1.json" {
		t.Errorf("args = %v", r.args)
	}
	// By name on PATH.
	t.Setenv("PATH", filepath.Dir(bin))
	if r := resolveDriver(parseSpec(t, `{"driver":"mywidget","port":1}`)); r.kind != installedBinary || r.exe != bin {
		t.Errorf("PATH: %+v", r)
	}
	// Unresolved.
	t.Setenv("PATH", t.TempDir())
	if r := resolveDriver(parseSpec(t, `{"driver":"nothing-here","port":1}`)); r.kind != unresolved {
		t.Errorf("unresolved: %v", r.kind)
	}
	// exec pointing at a non-executable is unresolved, not silently PATH-searched.
	txt := filepath.Join(t.TempDir(), "notexec")
	os.WriteFile(txt, []byte("x"), 0o644)
	if r := resolveDriver(parseSpec(t, `{"driver":"mywidget","exec":"`+txt+`","port":1}`)); r.kind != unresolved {
		t.Errorf("non-executable exec: %v", r.kind)
	}

	// -check reports each kind.
	c := &Config{Devices: []DeviceSpec{
		parseSpec(t, `{"driver":"sim-focuser","port":11500}`),
	}}
	binSpec := parseSpec(t, `{"driver":"mywidget","exec":"`+bin+`","port":11501}`)
	binSpec.Instance, binSpec.Source = "w1", "w1.json"
	unres := parseSpec(t, `{"driver":"gone","port":11502}`)
	unres.Instance, unres.Source = "gone", "gone.json"
	c.Devices = append(c.Devices, binSpec, unres)
	var out strings.Builder
	if fatal, errs := checkConfig(&out, c); fatal+errs != 0 {
		t.Errorf("fatal, errors = %d, %d:\n%s", fatal, errs, out.String())
	}
	o := out.String()
	if !strings.Contains(o, "focuser/0 on port 11500") || !strings.Contains(o, "separate binary "+bin) || !strings.Contains(o, "warn") {
		t.Errorf("-check output:\n%s", o)
	}
}

// noSupervisor reports every instance as in-process and running, and refuses
// every action with ErrNoSupervisor, so the orchestrator page has one contract
// whatever the platform.
func TestNoSupervisor(t *testing.T) {
	var s Supervisor = noSupervisor{}
	ctx := context.Background()
	st, err := s.Status(ctx, "x")
	if err != nil || !st.Running || !st.Installed || st.State != "in-process" {
		t.Errorf("status = %+v, %v", st, err)
	}
	for name, fn := range map[string]func() error{
		"install":   func() error { return s.Install(ctx, "x") },
		"uninstall": func() error { return s.Uninstall(ctx, "x") },
		"start":     func() error { return s.Start(ctx, "x") },
		"stop":      func() error { return s.Stop(ctx, "x") },
		"restart":   func() error { return s.Restart(ctx, "x") },
		"enable":    func() error { return s.Enable(ctx, "x") },
		"disable":   func() error { return s.Disable(ctx, "x") },
	} {
		if err := fn(); !errors.Is(err, ErrNoSupervisor) {
			t.Errorf("%s: err = %v, want ErrNoSupervisor", name, err)
		}
	}
	var sb strings.Builder
	if err := s.Logs(ctx, "x", 10, &sb); err != nil || sb.Len() == 0 {
		t.Errorf("logs: %v %q", err, sb.String())
	}
	if platformSupervisor("/tmp/hurd.json").Name() == "" {
		t.Error("platformSupervisor has no name")
	}
}

// The orchestrator page renders every configured device, in process or not,
// runs the config check on demand, refuses supervisor actions politely under
// the no-op supervisor, and adds a device by writing a commented file.
func TestOrchestratorPage(t *testing.T) {
	t.Setenv("ALPACA_STATE_DIR", t.TempDir())
	root := t.TempDir()
	cfgPath := filepath.Join(root, "hurd.json")
	writeFile(t, cfgPath, `{"discovery":"off"}`)
	writeFile(t, filepath.Join(root, "devices.d", "foc.json"), `{"driver":"sim-focuser","port":11600,"name":"Foc"}`)
	bin := filepath.Join(t.TempDir(), "widget")
	os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	writeFile(t, filepath.Join(root, "devices.d", "wid.json"), `{"driver":"widget","exec":"`+bin+`","port":11601}`)
	writeFile(t, filepath.Join(root, "devices.d", "gone.json"), `{"driver":"gone-driver","port":11602}`)
	writeFile(t, filepath.Join(root, "devices.d", "off.json"), `{"driver":"sim-camera","port":11603,"enable":false}`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	orch := &orchestrator{cfgPath: cfgPath, cfg: cfg, sup: noSupervisor{}, servers: map[int]*alpacadev.Server{}, startedAt: time.Now()}
	srv := alpacadev.New(alpacadev.Config{Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: serverName, SetupPages: orch.setupPages(), SetupHome: orch})
	for _, spec := range cfg.Devices {
		res := resolveDriver(spec)
		if !spec.enabled() {
			// As serve does: a disabled file is listed, nothing is built.
			orch.rows = append(orch.rows, orchRow{spec: spec, res: res, port: spec.Port})
			continue
		}
		switch res.kind {
		case compiledIn:
			dev, num, err := registerDevice(srv, spec, &deviceNumbers{})
			if err != nil {
				t.Fatal(err)
			}
			orch.rows = append(orch.rows, orchRow{spec: spec, res: res, num: num, inProcess: true, deviceName: dev.Name(), devType: deviceTypeOf(spec, dev), port: 11600})
		case installedBinary:
			orch.rows = append(orch.rows, orchRow{spec: spec, res: res, port: spec.Port, skipped: "separate binary; no supervisor on this host"})
		default:
			orch.rows = append(orch.rows, orchRow{spec: spec, res: res, skipped: "driver not compiled in and no binary found"})
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	t.Cleanup(ts.Close)

	get := func(p string) string {
		r, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d\n%s", p, r.StatusCode, b)
		}
		return string(b)
	}
	// /setup is the page; the server page has nothing to show on a server
	// with no devices, so /setup/server lands on the page too.
	if pg := get("/setup/server"); !strings.Contains(pg, "<h1>alpacahurd</h1>") || strings.Contains(pg, "No devices are configured") {
		t.Errorf("/setup/server should land on the orchestrator page:\n%s", pg)
	}
	body := get("/setup")
	for _, want := range []string{"foc", "in process", "11600", "Foc", `/setup/v1/focuser/0/setup`,
		"wid", "separate binary", bin, "-discovery register",
		"gone", "driver not compiled in", "in-process", "sim-focuser</option>",
		"off", "11603", "disabled"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// The check endpoint shows -check output.
	if chk := get("/setup/check"); !strings.Contains(chk, "focuser/0 on port 11600") || !strings.Contains(chk, "separate binary") || !strings.Contains(chk, "warn") {
		t.Errorf("check output:\n%s", chk)
	}
	// An action under the no-op supervisor is refused with a clear banner.
	resp, _ := http.PostForm(ts.URL+"/setup", url.Values{"instance": {"wid"}, "action": {"restart"}})
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "no separate process to control") {
		t.Errorf("no-op action banner:\n%s", b)
	}
	// Add writes a commented device file; a second add of the same name refuses.
	resp, _ = http.PostForm(ts.URL+"/setup/add", url.Values{"driver": {"sim-camera"}, "instance": {"guide-cam"}})
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	added := filepath.Join(root, "devices.d", "guide-cam.json")
	if !strings.Contains(string(b), "wrote "+added) {
		t.Fatalf("add:\n%s", b)
	}
	m, err := devicemain.ReadDeviceFile(added)
	if err != nil || string(m["driver"]) != `"sim-camera"` || m["port"] != nil {
		t.Errorf("added file: %v %v", m, err)
	}
	resp, _ = http.PostForm(ts.URL+"/setup/add", url.Values{"driver": {"sim-camera"}, "instance": {"guide-cam"}})
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "exists already") {
		t.Errorf("duplicate add should refuse:\n%s", b)
	}
	resp, _ = http.PostForm(ts.URL+"/setup/add", url.Values{"driver": {"sim-camera"}, "instance": {"../evil"}})
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "plain filename stem") {
		t.Errorf("path-traversal instance should refuse:\n%s", b)
	}
}

// Several devices.d entries with no port scan at once, each in its own window
// above portScanBase, so none collides with another and each ends up on a
// distinct port that persists.
func TestManyScannedPortsDoNotCollide(t *testing.T) {
	stateRoot := t.TempDir()
	t.Setenv("ALPACA_STATE_DIR", stateRoot)
	root := t.TempDir()
	cfg := filepath.Join(root, "hurd.json")
	writeFile(t, cfg, `{"discovery":"off"}`)
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		writeFile(t, filepath.Join(root, "devices.d", n+".json"), `{"driver":"sim-focuser"}`)
	}
	c, err := LoadConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var servers []*alpacadev.Server
	errc := make(chan error, len(c.Devices))
	for i, spec := range c.Devices {
		srv := alpacadev.New(alpacadev.Config{AlpacaPort: 0, PortScanBase: portScanBase + i*portScanSpan, PortScanLimit: portScanSpan,
			Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: serverName})
		if _, _, err := registerDevice(srv, spec, &deviceNumbers{}); err != nil {
			t.Fatal(err)
		}
		servers = append(servers, srv)
		go func() { errc <- srv.Run(ctx) }()
	}
	ports, err := waitBound(ctx, servers, errc)
	if err != nil {
		t.Fatalf("waitBound: %v", err)
	}
	seen := map[int]bool{}
	var bound []boundEntry
	for i, p := range ports {
		if seen[p] {
			t.Fatalf("port %d bound twice: %v", p, ports)
		}
		seen[p] = true
		bound = append(bound, boundEntry{spec: c.Devices[i], port: p})
	}
	persistBoundPorts(bound)
	c2, _ := LoadConfig(cfg)
	for i, spec := range c2.Devices {
		if spec.Port != ports[i] {
			t.Errorf("%s reloaded port %d, want %d", spec.Instance, spec.Port, ports[i])
		}
	}
	// A device's own state file holds port beside its settings; the settings
	// load must not choke on it (goalpaca skips undeclared keys).
	sb, _ := os.ReadFile(filepath.Join(stateRoot, "devices", "a.json"))
	if !strings.Contains(string(sb), `"port"`) {
		t.Errorf("state should record port: %s", sb)
	}
}

// Pinning port in the admin file after a scan: the device binds the pinned
// port and the state file is brought to match, so state never names a port the
// device is not on.
func TestPinnedPortReplacesStaleState(t *testing.T) {
	stateRoot := t.TempDir()
	t.Setenv("ALPACA_STATE_DIR", stateRoot)
	root := t.TempDir()
	cfg := filepath.Join(root, "hurd.json")
	writeFile(t, cfg, `{"discovery":"off"}`)
	admin := filepath.Join(root, "devices.d", "cam.json")
	stateFile := filepath.Join(stateRoot, "devices", "cam.json")
	// A scan already happened: state says 11305.
	writeFile(t, admin, `{"driver":"sim-focuser"}`)
	writeFile(t, stateFile, `{"port": 11305, "extra": "kept"}`)
	c, _ := LoadConfig(cfg)
	if c.Devices[0].Port != 11305 {
		t.Fatalf("state port not overlaid: %d", c.Devices[0].Port)
	}
	// Now the admin pins 11800.
	writeFile(t, admin, `{"driver":"sim-focuser","port":11800}`)
	c, _ = LoadConfig(cfg)
	if c.Devices[0].Port != 11800 || !c.Devices[0].Pinned["port"] == true {
		// port is a common key, so it is never in Pinned; the admin value wins by overlay order.
	}
	if c.Devices[0].Port != 11800 {
		t.Fatalf("admin port should win: %d", c.Devices[0].Port)
	}
	persistBoundPorts([]boundEntry{{spec: c.Devices[0], port: 11800}})
	sb, _ := os.ReadFile(stateFile)
	if !strings.Contains(string(sb), `"port": 11800`) || strings.Contains(string(sb), "11305") {
		t.Errorf("state should now say 11800:\n%s", sb)
	}
	if !strings.Contains(string(sb), `"extra": "kept"`) {
		t.Errorf("other state keys must survive:\n%s", sb)
	}
}

// A device server mounts /setup/hurd as a redirect to the orchestrator page
// on the setup port, using the host the browser addressed; with the page off
// it says so.
func TestDeviceServerRedirectsToOrchestratorPage(t *testing.T) {
	orch := &orchestrator{sup: noSupervisor{}, servers: map[int]*alpacadev.Server{}}
	srv := alpacadev.New(alpacadev.Config{Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: serverName, SetupPages: orch.redirectPages()})
	if err := srv.Register(alpacadev.FocuserType, 0, mustDev(t, `{"driver":"sim-focuser"}`)); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	t.Cleanup(ts.Close)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// Page off: a clear 404.
	r, _ := client.Get(ts.URL + "/setup/hurd")
	r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Errorf("page off: %d", r.StatusCode)
	}
	// Page on 32227: redirect to the same host on that port.
	orch.setPagePort(32227)
	req, _ := http.NewRequest("GET", ts.URL+"/setup/hurd", nil)
	req.Host = "rig.local:11300"
	r, _ = client.Do(req)
	r.Body.Close()
	if r.StatusCode != http.StatusFound || r.Header.Get("Location") != "http://rig.local:32227/setup" {
		t.Errorf("redirect: %d -> %q", r.StatusCode, r.Header.Get("Location"))
	}
	// setupPort resolution.
	for _, c := range []struct{ in, want int }{{0, 32227}, {-1, 0}, {8080, 8080}} {
		if got := (&Config{SetupPort: c.in}).setupPort(); got != c.want {
			t.Errorf("setupPort(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func mustDev(t *testing.T, entry string) alpacadev.Device {
	t.Helper()
	_, dev, err := buildDevice(parseSpec(t, entry))
	if err != nil {
		t.Fatal(err)
	}
	return dev
}
