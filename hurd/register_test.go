package hurd

import (
	"encoding/json"
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

// serveSpec registers one config entry on a fresh server (the production model:
// one device per per-port server) and returns the live base URL. Registration
// does not open hardware (that happens in srv.Run, which we don't call), so this
// exercises construction + dispatch without any devices attached.
func serveSpec(t *testing.T, entry string) string {
	t.Helper()
	srv := alpacadev.New(alpacadev.Config{
		Discovery: alpacadev.DiscoveryConfig{Mode: alpacadev.DiscoveryOff}, ServerName: "t", Manufacturer: "t",
	})
	if _, err := registerDevice(srv, parseSpec(t, entry)); err != nil {
		t.Fatalf("registerDevice(%s): %v", entry, err)
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	t.Cleanup(ts.Close)
	return ts.URL
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

// TestRegistryDriversServe: every hardware driver constructs from a config entry
// through the registry and serves as exactly device 0 of its ASCOM type on its
// own server — the per-port model, where a client (PHD2) asking for <type>/0
// must always be right.
func TestRegistryDriversServe(t *testing.T) {
	cases := []struct {
		entry, wantType string
	}{
		{`{"driver":"tenmicron","addr":"127.0.0.1:1"}`, "telescope"},
		{`{"driver":"asiam5","serial":"A"}`, "telescope"},
		{`{"driver":"onstep","addr":"127.0.0.1:1"}`, "telescope"},
		{`{"driver":"rst"}`, "telescope"},
		{`{"driver":"asicam","serial":"deadbeef"}`, "camera"},
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

// TestSpecNameOverride: the common "name" key overrides the device display name
// for every driver, hardware and sim alike.
func TestSpecNameOverride(t *testing.T) {
	for _, entry := range []string{
		`{"driver":"asicam","serial":"x","name":"My Cam"}`,
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
		{`{"driver":"asiam5"}`, "serial"},
		// A typo in a DRIVER-owned key is rejected by the driver's strict decode.
		{`{"driver":"asieaf","serail":"x"}`, "serail"},
		// A wrongly-typed driver key is rejected too.
		{`{"driver":"focuscube","maxstep":"lots"}`, "maxstep"},
		// Unknown driver: point at -drivers/hurd.conf.
		{`{"driver":"nope"}`, "hurd.conf"},
		// The cgo ZWO-SDK devices are deliberately not in the herd.
		{`{"driver":"asiccd"}`, "ZWO SDK"},
	}
	for _, c := range cases {
		_, _, err := buildDevice(parseSpec(t, c.entry))
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("buildDevice(%s): err %v, want mention of %q", c.entry, err, c.wantErr)
		}
	}
}

// TestCommonKeysReachDrivers: engine-owned keys in an entry never leak into the
// driver's strict decode (no spurious "unknown field"), whatever their case.
func TestCommonKeysReachDrivers(t *testing.T) {
	entry := `{"driver":"asieaf","Name":"N","enable":true,"port":1,"indi":false,"lx200Port":0,
		"aperture":1,"apertureArea":1,"focalLength":1,"guiderAperture":1,"guiderFocalLength":1,
		"guideRate":0.5,"index":3}`
	if _, _, err := buildDevice(parseSpec(t, entry)); err != nil {
		t.Fatalf("common keys leaked into the driver decode: %v", err)
	}
}
