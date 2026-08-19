package hurd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mikefsq/goalpaca/registry"
	alpacadev "github.com/mikefsq/goalpaca/server"
)

// fe-widget is a test driver with a FrontEnd, delegating construction to the
// sim focuser; the hook records what the host handed it.
var feLast struct {
	ctx   context.Context
	hosts []string
	entry json.RawMessage
	dev   func() alpacadev.Device
	calls int
}

func init() {
	sim, ok := registry.Lookup("sim-focuser")
	if !ok {
		panic("sim-focuser is not registered")
	}
	registry.Register(registry.Driver{
		Name:          "fe-widget",
		Type:          sim.Type,
		Description:   "front-end test driver",
		ConfigExample: `{ "driver": "fe-widget" }`,
		Config:        sim.Config,
		New:           sim.New,
		FrontEnd: func(ctx context.Context, dev func() alpacadev.Device, entry json.RawMessage, hosts []string) error {
			feLast.ctx, feLast.hosts, feLast.entry, feLast.dev = ctx, hosts, entry, dev
			feLast.calls++
			return nil
		},
	})
}

// TestFrontEndLifecycle: enabling a device wires its driver front-end with
// the device's own context, the entry, and the hosts the Alpaca servers bind;
// disabling cancels that context before the device is unregistered; a second
// enable wires a fresh front-end.
func TestFrontEndLifecycle(t *testing.T) {
	t.Setenv("ALPACA_STATE_DIR", t.TempDir())
	root := t.TempDir()
	cfgPath := filepath.Join(root, "hurd.json")
	writeFile(t, cfgPath, `{"discovery":"off"}`)
	writeFile(t, filepath.Join(root, "devices.d", "fe.json"),
		`{"driver":"fe-widget","port":11738,"lx200Port":14038,"enable":false}`)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	orch := &orchestrator{cfgPath: cfgPath, cfg: cfg, sup: noSupervisor{}, servers: map[int]*alpacadev.Server{}, startedAt: time.Now(),
		ctx: ctx, byPort: map[int]*alpacadev.Server{}, nums: map[int]*deviceNumbers{}, listenAddrs: []string{"127.0.0.1"}}
	orch.rows = []orchRow{{spec: cfg.Devices[0], res: resolveDriver(cfg.Devices[0]), port: 11738}}
	page := alpacadev.New(alpacadev.Config{Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: serverName, SetupPages: orch.setupPages(), SetupHome: orch})
	ts := httptest.NewServer(http.HandlerFunc(page.ServeHTTP))
	defer ts.Close()
	post := func(action string) string {
		resp, err := http.PostForm(ts.URL+"/setup", url.Values{"instance": {"fe"}, "action": {action}})
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	if body := post("enable"); !strings.Contains(body, "fe enabled") {
		t.Fatalf("enable:\n%s", body)
	}
	if feLast.calls != 1 {
		t.Fatalf("FrontEnd calls = %d, want 1", feLast.calls)
	}
	if feLast.ctx.Err() != nil {
		t.Fatal("front-end context cancelled while the device is enabled")
	}
	if len(feLast.hosts) != 1 || feLast.hosts[0] != "127.0.0.1" {
		t.Fatalf("hosts = %v, want the orchestrator's listen addresses", feLast.hosts)
	}
	if !strings.Contains(string(feLast.entry), `"lx200Port"`) {
		t.Fatalf("entry missing the front-end key: %s", feLast.entry)
	}
	if feLast.dev() == nil {
		t.Fatal("dev() returned nil for a registered device")
	}
	firstCtx := feLast.ctx

	if body := post("disable"); !strings.Contains(body, "fe disabled") {
		t.Fatalf("disable:\n%s", body)
	}
	if firstCtx.Err() == nil {
		t.Fatal("disable did not cancel the front-end context")
	}
	if feLast.dev() != nil {
		t.Fatal("dev() should be nil after the device is unregistered")
	}

	if body := post("enable"); !strings.Contains(body, "fe enabled") {
		t.Fatalf("re-enable:\n%s", body)
	}
	if feLast.calls != 2 {
		t.Fatalf("FrontEnd calls after re-enable = %d, want 2", feLast.calls)
	}
	if feLast.ctx.Err() != nil {
		t.Fatal("re-enabled front-end context should be live")
	}
}
