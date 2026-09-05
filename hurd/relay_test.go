package hurd

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

func TestResponderLocalAndRemote(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The remote device's relay endpoint.
	var relayed atomic.Pointer[alpacadev.ReplyTarget]
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != alpacadev.DiscoveryReplyPath {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var tgt alpacadev.ReplyTarget
		_ = json.Unmarshal(b, &tgt)
		relayed.Store(&tgt)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer remote.Close()
	_, rp, _ := net.SplitHostPort(remote.Listener.Addr().String())
	remotePort, _ := strconv.Atoi(rp)

	orch := &orchestrator{rows: []orchRow{{spec: DeviceSpec{Instance: "bench-focuser", deviceCommon: deviceCommon{Driver: "x"}}}}}
	resp := newResponder([]int{11111}, orch.noteRegistration)
	// Route the TEST-NET address to the test server.
	resp.reg.Client = &http.Client{Timeout: time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, remote.Listener.Addr().String())
		},
	}}

	sock, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	go serveDiscovery(ctx, sock, resp, nil)
	target := sock.LocalAddr().(*net.UDPAddr)

	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// A local binary (heartbeat from loopback) and a remote one (Address set).
	send := func(hb alpacadev.Heartbeat) {
		b, _ := json.Marshal(hb)
		if _, err := client.WriteToUDP(b, target); err != nil {
			t.Fatal(err)
		}
	}
	send(alpacadev.Heartbeat{AlpacaPort: 11302, UniqueID: "loc", DeviceType: "Focuser", DeviceName: "Bench", Instance: "bench-focuser"})
	send(alpacadev.Heartbeat{AlpacaPort: remotePort, UniqueID: "rem", DeviceType: "Camera", DeviceName: "Roof", Address: "192.0.2.9"})

	// Wait for both registrations to land, then probe.
	deadline := time.Now().Add(2 * time.Second)
	for len(resp.reg.Live()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := len(resp.reg.Live()); n != 2 {
		t.Fatalf("registrations %d, want 2", n)
	}
	if _, err := client.WriteToUDP([]byte("alpacadiscovery1"), target); err != nil {
		t.Fatal(err)
	}

	got := map[int]bool{}
	buf := make([]byte, 256)
	for len(got) < 2 {
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := client.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("replies %v: %v", got, err)
		}
		var r struct{ AlpacaPort int }
		if err := json.Unmarshal(buf[:n], &r); err != nil {
			t.Fatalf("reply %q: %v", buf[:n], err)
		}
		got[r.AlpacaPort] = true
	}
	if !got[11111] || !got[11302] {
		t.Fatalf("direct replies for %v, want 11111 and 11302", got)
	}
	// No third direct reply: the remote device answers for itself.
	_ = client.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if n, _, err := client.ReadFromUDP(buf); err == nil {
		t.Fatalf("unexpected extra reply %q", buf[:n])
	}

	deadline = time.Now().Add(2 * time.Second)
	for relayed.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	tgt := relayed.Load()
	cport := client.LocalAddr().(*net.UDPAddr).Port
	if tgt == nil || tgt.IP != "127.0.0.1" || tgt.Port != cport {
		t.Fatalf("relay target %+v, want 127.0.0.1:%d", tgt, cport)
	}

	orch.mu.RLock()
	defer orch.mu.RUnlock()
	if orch.rows[0].reg == nil || orch.rows[0].reg.AlpacaPort != 11302 || !orch.rows[0].reg.Local {
		t.Fatalf("row registration %+v", orch.rows[0].reg)
	}
	if e := orch.extra["rem"]; e == nil || e.Local || e.AlpacaPort != remotePort {
		t.Fatalf("extra registration %+v", e)
	}
	if s := registeredState(orch.rows[0].reg); s == "" {
		t.Fatal("registered state empty for a fresh heartbeat")
	}
}

func TestRegisteredStateExpires(t *testing.T) {
	e := &alpacadev.Registration{Seen: time.Now().Add(-2 * alpacadev.DefaultRegistrationTTL), Local: true}
	if s := registeredState(e); s != "" {
		t.Fatalf("stale state %q", s)
	}
	if s := registeredState(nil); s != "" {
		t.Fatalf("nil state %q", s)
	}
}

func TestReloaderRereadsDeviceFile(t *testing.T) {
	stateRoot := t.TempDir()
	t.Setenv("ALPACA_STATE_DIR", stateRoot)
	root := t.TempDir()
	cfg := filepath.Join(root, "hurd.json")
	writeFile(t, cfg, `{"discovery":"off"}`)
	devFile := filepath.Join(root, "devices.d", "bench.json")
	writeFile(t, devFile, `{"driver":"sim-camera","port":11999,"name":"Before"}`)
	c, err := LoadConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	spec := c.Devices[0]
	_, dev, err := buildDevice(spec)
	if err != nil {
		t.Fatal(err)
	}
	if dev.Name() != "Before" {
		t.Fatalf("built %q", dev.Name())
	}
	reload := reloaderFor(spec)

	writeFile(t, devFile, `{"driver":"sim-camera","port":11999,"name":"After"}`)
	dev2, sc, err := reload(context.Background())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if dev2.Name() != "After" || dev2 == dev {
		t.Fatalf("reload built %q (same object %v)", dev2.Name(), dev2 == dev)
	}
	if sc == nil {
		t.Fatal("reload built no setup form for a driver with a Config")
	}

	writeFile(t, devFile, `{"driver":"sim-focuser","port":11999}`)
	if _, _, err := reload(context.Background()); err == nil || !strings.Contains(err.Error(), "driver") {
		t.Fatalf("driver change: %v", err)
	}
	writeFile(t, devFile, `{"driver":"sim-camera","port":11999,"enable":false}`)
	if _, _, err := reload(context.Background()); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled entry: %v", err)
	}
}

func TestEnableDisableInProcess(t *testing.T) {
	stateRoot := t.TempDir()
	t.Setenv("ALPACA_STATE_DIR", stateRoot)
	root := t.TempDir()
	cfgPath := filepath.Join(root, "hurd.json")
	writeFile(t, cfgPath, `{"discovery":"off"}`)
	writeFile(t, filepath.Join(root, "devices.d", "guide.json"), `{"driver":"sim-camera","port":11733,"name":"Guide","enable":false}`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Devices[0].enabled() {
		t.Fatal("entry should load disabled")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	orch := &orchestrator{cfgPath: cfgPath, cfg: cfg, sup: noSupervisor{}, servers: map[int]*alpacadev.Server{}, startedAt: time.Now(),
		ctx: ctx, byPort: map[int]*alpacadev.Server{}, nums: map[int]*deviceNumbers{}}
	orch.rows = []orchRow{{spec: cfg.Devices[0], res: resolveDriver(cfg.Devices[0]), port: 11733}}
	page := alpacadev.New(alpacadev.Config{Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: serverName, SetupPages: orch.setupPages(), SetupHome: orch})
	ts := httptest.NewServer(http.HandlerFunc(page.ServeHTTP))
	defer ts.Close()

	post := func(action string) string {
		resp, err := http.PostForm(ts.URL+"/setup", url.Values{"instance": {"guide"}, "action": {action}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b)
	}
	body := post("enable")
	if !strings.Contains(body, "guide enabled: serving as camera 0 on port 11733") {
		t.Fatalf("enable:\n%s", body)
	}
	// Served on its port.
	resp, err := http.Get("http://127.0.0.1:11733/management/v1/configureddevices")
	if err != nil {
		t.Fatalf("device server: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `"DeviceName":"Guide"`) {
		t.Fatalf("configureddevices: %s", b)
	}
	// The switch is in the state file and the overlay honours it over the file.
	cfg2, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg2.Devices[0].enabled() {
		t.Fatal("state enable did not override the admin file's false")
	}
	// The row is in process now, with a reload and a disable button.
	if pg := post("logs"); !strings.Contains(pg, "in process") {
		t.Fatalf("page after enable:\n%s", pg)
	}

	body = post("disable")
	if !strings.Contains(body, "guide disabled: hardware closed and the device removed from port 11733") {
		t.Fatalf("disable:\n%s", body)
	}
	resp, _ = http.Get("http://127.0.0.1:11733/management/v1/configureddevices")
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(b), `"Guide"`) {
		t.Fatalf("device still served after disable: %s", b)
	}
	cfg3, _ := LoadConfig(cfgPath)
	if cfg3.Devices[0].enabled() {
		t.Fatal("state enable false not recorded")
	}
	// Enable again lands on the same, still running server.
	if body := post("enable"); !strings.Contains(body, "on port 11733") {
		t.Fatalf("re-enable:\n%s", body)
	}

	writeFile(t, filepath.Join(root, "devices.d", "mount.json"), `{"driver":"asiam5","port":11735,"enable":false}`)
	mspec, err := loadDeviceFile(filepath.Join(root, "devices.d", "mount.json"), stateDevicesDir())
	if err != nil {
		t.Fatal(err)
	}
	orch.mu.Lock()
	orch.rows = append(orch.rows, orchRow{spec: mspec, res: resolveDriver(mspec), port: 11735})
	orch.cfg.Devices = append(orch.cfg.Devices, mspec)
	orch.mu.Unlock()
	resp, err = http.PostForm(ts.URL+"/setup", url.Values{"instance": {"mount"}, "action": {"enable"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "mount not enabled") || !strings.Contains(string(b), `value="enable"`) {
		t.Fatalf("failed enable:\n%s", b)
	}
	if again, _ := loadDeviceFile(filepath.Join(root, "devices.d", "mount.json"), stateDevicesDir()); again.enabled() {
		t.Fatal("a failed enable left the entry recorded as enabled")
	}
}

func TestEditDeviceFile(t *testing.T) {
	stateRoot := t.TempDir()
	t.Setenv("ALPACA_STATE_DIR", stateRoot)
	root := t.TempDir()
	cfgPath := filepath.Join(root, "hurd.json")
	writeFile(t, cfgPath, `{"discovery":"off"}`)
	devFile := filepath.Join(root, "devices.d", "cam.json")
	writeFile(t, devFile, "{\n\t\"driver\": \"sim-camera\",\n\t// \"port\": 11740,\n\t\"enable\": false\n}\n")
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	orch := &orchestrator{cfgPath: cfgPath, cfg: cfg, sup: noSupervisor{}, servers: map[int]*alpacadev.Server{}, startedAt: time.Now(),
		ctx: ctx, byPort: map[int]*alpacadev.Server{}, nums: map[int]*deviceNumbers{}}
	orch.rows = []orchRow{{spec: cfg.Devices[0], res: resolveDriver(cfg.Devices[0])}}
	page := alpacadev.New(alpacadev.Config{Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: serverName, SetupPages: orch.setupPages(), SetupHome: orch})
	ts := httptest.NewServer(http.HandlerFunc(page.ServeHTTP))
	defer ts.Close()

	// The row links the editor; the editor shows the file.
	resp, _ := http.Get(ts.URL + "/setup")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `/setup/edit?instance=cam`) {
		t.Fatalf("no edit link:\n%s", b)
	}
	resp, _ = http.Get(ts.URL + "/setup/edit?instance=cam")
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `// &#34;port&#34;: 11740`) || !strings.Contains(string(b), devFile) {
		t.Fatalf("editor:\n%s", b)
	}
	// Bad JSON is refused and the file untouched.
	resp, _ = http.PostForm(ts.URL+"/setup/edit", url.Values{"instance": {"cam"}, "text": {`{"driver": "sim-camera",`}})
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "not saved") {
		t.Fatalf("bad JSON:\n%s", b)
	}
	if got, _ := os.ReadFile(devFile); !strings.Contains(string(got), `// "port": 11740`) {
		t.Fatal("file changed by a refused save")
	}
	// A valid edit is written and the row re-read; enable then serves it on the new port.
	resp, _ = http.PostForm(ts.URL+"/setup/edit", url.Values{"instance": {"cam"}, "text": {"{\n\t\"driver\": \"sim-camera\", // fixed\n\t\"port\": 11741,\n\t\"name\": \"Edited\"\n}"}})
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "saved "+devFile) || !strings.Contains(string(b), "enable the device") {
		t.Fatalf("save:\n%s", b)
	}
	if got, _ := os.ReadFile(devFile); !strings.Contains(string(got), `"port": 11741`) {
		t.Fatalf("file after save: %s", got)
	}
	resp, _ = http.PostForm(ts.URL+"/setup", url.Values{"instance": {"cam"}, "action": {"enable"}})
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "cam enabled: serving as camera 0 on port 11741") {
		t.Fatalf("enable after edit:\n%s", b)
	}
	// Path traversal by instance is impossible: only known rows are editable.
	resp, _ = http.PostForm(ts.URL+"/setup/edit", url.Values{"instance": {"../hurd"}, "text": {`{"driver":"x"}`}})
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "no device file") {
		t.Fatalf("unknown instance:\n%s", b)
	}
}
