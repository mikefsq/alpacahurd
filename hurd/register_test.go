package hurd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	alpacadev "github.com/mikefsq/goalpaca/server"
)

type cfgDev struct {
	DeviceName   string
	DeviceType   string
	DeviceNumber int
	UniqueID     string
}

// serveSpecs registers devices without opening hardware and returns the test server URL.
func serveSpecs(t *testing.T, entries ...string) string {
	t.Helper()
	srv := alpacadev.New(alpacadev.Config{
		Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: "t", Manufacturer: "t",
	})
	nums := &deviceNumbers{}
	for _, entry := range entries {
		if _, _, err := registerDevice(srv, parseSpec(t, entry), nums); err != nil {
			t.Fatalf("registerDevice(%s): %v", entry, err)
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	t.Cleanup(ts.Close)
	return ts.URL
}

// serveSpec serves one device without opening hardware.
func serveSpec(t *testing.T, entry string) string {
	t.Helper()
	return serveSpecs(t, entry)
}

func configured(t *testing.T, base string) []cfgDev {
	t.Helper()
	r, err := http.Get(base + "/management/v1/configureddevices")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var out struct{ Value []cfgDev }
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Value
}

func TestRegistryDriversServe(t *testing.T) {
	cases := []struct {
		entry, wantType string
	}{
		{`{"driver":"tenmicron","addr":"127.0.0.1:1"}`, "telescope"},
		{`{"driver":"asiam5","serial":"A"}`, "telescope"},
		{`{"driver":"onstep","addr":"127.0.0.1:1"}`, "telescope"},
		{`{"driver":"rst"}`, "telescope"},
		{`{"driver":"astrocam","serial":"deadbeef"}`, "camera"},
		{`{"driver":"asieaf","index":0}`, "focuser"},
		{`{"driver":"oasisfoc","index":0}`, "focuser"},
		{`{"driver":"focuscube","serial":"FT1ABCDE","maxstep":120000}`, "focuser"},
		{`{"driver":"focuslynx","nickname":"OAG focuser"}`, "focuser"},
		{`{"driver":"asiefw","index":0}`, "filterwheel"},
		{`{"driver":"oasisfw","index":0}`, "filterwheel"},
		{`{"driver":"mgpbox","index":0}`, "observingconditions"},
		{`{"driver":"unihedron","index":0}`, "observingconditions"},
		{`{"driver":"sim-telescope","name":"Sim"}`, "telescope"},
		{`{"driver":"sim-camera","name":"SimCam"}`, "camera"},
	}
	for _, c := range cases {
		devs := configured(t, serveSpec(t, c.entry))
		if len(devs) != 1 || devs[0].DeviceType != c.wantType || devs[0].DeviceNumber != 0 {
			t.Errorf("%s: got %+v, want exactly one %s/0", c.entry, devs, c.wantType)
			continue
		}
		if devs[0].UniqueID == "" {
			t.Errorf("%s: empty UniqueID", c.entry)
		}
	}
}

func TestSharedPortNumbersDevices(t *testing.T) {
	base := serveSpecs(t,
		`{"driver":"astrocam","serial":"aaaa","name":"Main"}`,
		`{"driver":"astrocam","serial":"bbbb","name":"Guide"}`,
		`{"driver":"asieaf","index":0,"name":"Focus"}`,
	)
	want := []cfgDev{
		{DeviceName: "Main", DeviceType: "camera", DeviceNumber: 0},
		{DeviceName: "Guide", DeviceType: "camera", DeviceNumber: 1},
		{DeviceName: "Focus", DeviceType: "focuser", DeviceNumber: 0},
	}
	devs := configured(t, base)
	if len(devs) != len(want) {
		t.Fatalf("got %d devices, want %d: %+v", len(devs), len(want), devs)
	}
	for i, w := range want {
		if devs[i].DeviceName != w.DeviceName || devs[i].DeviceType != w.DeviceType ||
			devs[i].DeviceNumber != w.DeviceNumber {
			t.Errorf("device %d = %+v, want %s %s/%d", i, devs[i], w.DeviceName, w.DeviceType, w.DeviceNumber)
		}
	}
	for i, name := range []string{"Main", "Guide"} {
		r, err := http.Get(fmt.Sprintf("%s/api/v1/camera/%d/name", base, i))
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Value       string
			ErrorNumber int
		}
		err = json.NewDecoder(r.Body).Decode(&out)
		r.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if out.ErrorNumber != 0 || out.Value != name {
			t.Errorf("camera/%d name = %q (err %#x), want %q", i, out.Value, out.ErrorNumber, name)
		}
	}
}

func TestSharedPortPinnedNumbers(t *testing.T) {
	devs := configured(t, serveSpecs(t,
		`{"driver":"astrocam","serial":"aaaa","name":"Main","device":3}`,
		`{"driver":"astrocam","serial":"bbbb","name":"Guide"}`,
	))
	if len(devs) != 2 || devs[0].DeviceNumber != 3 || devs[1].DeviceNumber != 0 {
		t.Fatalf("got %+v, want pinned camera/3 and camera/0", devs)
	}
}

func TestSharedPortNumberCollision(t *testing.T) {
	nums := &deviceNumbers{}
	srv := alpacadev.New(alpacadev.Config{
		Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: "t", Manufacturer: "t",
	})
	if _, _, err := registerDevice(srv, parseSpec(t, `{"driver":"astrocam","serial":"a","port":1}`), nums); err != nil {
		t.Fatalf("first camera: %v", err)
	}
	_, _, err := registerDevice(srv, parseSpec(t, `{"driver":"astrocam","serial":"b","port":1,"device":0}`), nums)
	if err == nil || !strings.Contains(err.Error(), "already taken") {
		t.Fatalf("err = %v, want an already-taken error", err)
	}
}

func TestSpecNameOverride(t *testing.T) {
	for _, entry := range []string{
		`{"driver":"astrocam","serial":"x","name":"My Cam"}`,
		`{"driver":"sim-focuser","name":"My Cam"}`,
	} {
		devs := configured(t, serveSpec(t, entry))
		if len(devs) != 1 || devs[0].DeviceName != "My Cam" {
			t.Errorf("%s: got %+v, want name \"My Cam\"", entry, devs)
		}
	}
}

func TestBuildDeviceErrors(t *testing.T) {
	cases := []struct {
		entry, wantErr string
	}{
		// Required binding fields are still enforced by the driver.
		{`{"driver":"tenmicron"}`, "addr"},
		{`{"driver":"asiam5","serial":42}`, "serial"},
		// A typo in a DRIVER-owned key is rejected by the driver's strict decode.
		{`{"driver":"asieaf","serail":"x"}`, "serail"},
		// A wrongly-typed driver key is rejected too.
		{`{"driver":"focuscube","maxstep":"lots"}`, "maxstep"},
		// Unknown driver: point at -drivers/hurd.conf.
		{`{"driver":"nope"}`, "hurd.conf"},
		{`{"driver":"asiccd"}`, "ZWO SDK"},
	}
	for _, c := range cases {
		_, _, err := buildDevice(parseSpec(t, c.entry))
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("buildDevice(%s): err %v, want mention of %q", c.entry, err, c.wantErr)
		}
	}
}

func TestMountAllowsAutomaticDiscovery(t *testing.T) {
	if _, _, err := buildDevice(parseSpec(t, `{"driver":"asiam5"}`)); err != nil {
		t.Fatalf("mount without an explicit serial should allow discovery: %v", err)
	}
}

func TestCommonKeysReachDrivers(t *testing.T) {
	entry := `{"driver":"asieaf","Name":"N","enable":true,"port":1,"indi":false,"lx200Port":0,
		"aperture":1,"apertureArea":1,"focalLength":1,"guiderAperture":1,"guiderFocalLength":1,
		"guideRate":0.5,"index":3}`
	if _, _, err := buildDevice(parseSpec(t, entry)); err != nil {
		t.Fatalf("common keys leaked into the driver decode: %v", err)
	}
}

func TestSetupFormFromRegistry(t *testing.T) {
	base := serveSpec(t, `{"driver":"astrocam","serial":"deadbeef","fixdefects":true,"name":"Main"}`)
	r, err := http.Get(base + "/setup/v1/camera/0/setup")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	body := string(b)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("setup page: %d\n%s", r.StatusCode, body)
	}
	for _, want := range []string{`name="serial"`, `name="fixdefects"`, `name="fpsPercent"`, "set in the config file"} {
		if !strings.Contains(body, want) {
			t.Errorf("generated form missing %q", want)
		}
	}
	// serial and fixdefects were named in the entry, so both are pinned; the
	// entry left fpsPercent unset, so it is editable.
	pinned := strings.Count(body, "set in the config file")
	if pinned != 2 {
		t.Errorf("expected 2 pinned fields (serial, fixdefects), found %d", pinned)
	}
	// A driver with no Config, and no form of its own, still gets the
	// conformant not-configurable page.
	base = serveSpec(t, `{"driver":"sim-focuser","name":"F"}`)
	r, _ = http.Get(base + "/setup/v1/focuser/0/setup")
	b, _ = io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(b), "no configurable settings") {
		t.Errorf("sim-focuser should be not-configurable:\n%s", b)
	}
}
