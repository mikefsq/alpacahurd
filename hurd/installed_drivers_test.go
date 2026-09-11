package hurd

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func installTestDriver(t *testing.T, o *orchestrator, body string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell schema fixture")
	}
	exe := filepath.Join(t.TempDir(), "installed-test")
	script := "#!/bin/sh\n[ \"$1\" = -schema ] && [ \"$2\" = commented ] || exit 90\ncat <<'TEMPLATE'\n" + body + "\nTEMPLATE\n"
	if err := os.WriteFile(exe, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(filepath.Dir(o.cfgPath), "drivers.conf")
	writeFile(t, manifest, "# Installed binaries\n\n"+exe+"\n"+exe+"\n")
	return exe, manifest
}
func addPost(o *orchestrator, driver, instance string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/setup/add", strings.NewReader(url.Values{"driver": {driver}, "instance": {instance}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	o.ServeHTTP(w, r)
	return w
}
func TestInstalledDriverCatalogueAndCreation(t *testing.T) {
	o, _, _ := editorFixture(t)
	if strings.Contains(logGet(o, "/setup/add").Body.String(), `value="installed-test"`) {
		t.Fatal("driver appeared before installation")
	}
	exe, manifest := installTestDriver(t, o, "{\n // preserve the driver's help\n \"driver\":\"installed-test\",\n \"enable\":false\n}")
	page := logGet(o, "/setup/add").Body.String()
	if !strings.Contains(page, `value="installed-test"`) || !strings.Contains(page, `value="sim-camera"`) {
		t.Fatal("installed and built-in choices not merged")
	}
	// No prior instance or runtime state is needed to create a second device.
	w := addPost(o, "installed-test", "new-mount")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("creation: %d %s", w.Code, w.Body.String())
	}
	path := filepath.Join(filepath.Dir(o.cfgPath), "devices.d", "new-mount.json")
	spec, err := loadDeviceFile(path, stateDevicesDir())
	if err != nil {
		t.Fatal(err)
	}
	if spec.enabled() || spec.Exec != exe || spec.Driver != "installed-test" {
		t.Fatalf("created %+v", spec)
	}
	if resolveDriver(spec).exe != exe {
		t.Fatal("instance does not resolve to installed binary")
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "preserve the driver's help") {
		t.Fatal("schema comments lost")
	}
	if _, err := os.Stat(filepath.Join(stateDevicesDir(), "new-mount.json")); !os.IsNotExist(err) {
		t.Fatal("creation generated runtime state")
	}
	os.Remove(manifest)
	if strings.Contains(logGet(o, "/setup/add").Body.String(), `value="installed-test"`) {
		t.Fatal("uninstalled driver remains in catalogue")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("uninstall removed instance config")
	}
}
func TestInstalledDriverRejectsUnsafeTemplate(t *testing.T) {
	for _, body := range []string{`{`, `{"driver":"","enable":false}`, `{"driver":"installed-test"}`, `{"driver":"installed-test","enable":true}`, `{"driver":"installed-test","enable":false,"exec":"/other"}`} {
		t.Run(body, func(t *testing.T) {
			o, _, _ := editorFixture(t)
			installTestDriver(t, o, body)
			w := addPost(o, "installed-test", "new-mount")
			if w.Code == http.StatusSeeOther {
				t.Fatal("invalid or enabled template accepted")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(o.cfgPath), "devices.d", "new-mount.json")); !os.IsNotExist(err) {
				t.Fatal("invalid template wrote configuration")
			}
		})
	}
}
func TestAddDoesNotReuseExistingState(t *testing.T) {
	o, _, _ := editorFixture(t)
	path := filepath.Join(stateDevicesDir(), "old-camera.json")
	writeFile(t, path, `{"enable":true,"port":12345}`)
	w := addPost(o, "sim-camera", "old-camera")
	if !strings.Contains(w.Body.String(), "already has saved state") {
		t.Fatal(w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(o.cfgPath), "devices.d", "old-camera.json")); !os.IsNotExist(err) {
		t.Fatal("created unexpectedly enabled instance")
	}
	b, _ := os.ReadFile(path)
	if string(b) != `{"enable":true,"port":12345}` {
		t.Fatal("existing state changed")
	}
}
func TestInstalledDriverStaleAndMalformedRecords(t *testing.T) {
	o, _, _ := editorFixture(t)
	exe, manifest := installTestDriver(t, o, `{"driver":"installed-test","enable":false}`)
	os.Remove(exe)
	drivers, err := installedDrivers(o.cfgPath)
	if err == nil || len(drivers) != 0 {
		t.Fatal("missing executable offered")
	}
	writeFile(t, manifest, `invalid`)
	drivers, err = installedDrivers(o.cfgPath)
	if err == nil || len(drivers) != 0 {
		t.Fatal("invalid registry offered")
	}
}

func TestInstalledDriverBinaryAlias(t *testing.T) {
	o, _, _ := editorFixture(t)
	installTestDriver(t, o, `{"driver":"canonical-name","enable":false}`)
	w := addPost(o, "installed-test", "alias-mount")
	if w.Code != http.StatusSeeOther {
		t.Fatal(w.Body.String())
	}
	spec, err := loadDeviceFile(filepath.Join(filepath.Dir(o.cfgPath), "devices.d", "alias-mount.json"), stateDevicesDir())
	if err != nil || spec.Driver != "canonical-name" {
		t.Fatalf("%+v %v", spec, err)
	}
}
