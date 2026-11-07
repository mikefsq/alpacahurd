package hurd

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func addFixture(t *testing.T, supported bool) (*orchestrator, string) {
	t.Helper()
	o, path, _ := editorFixture(t)
	exe, _ := installTestDriver(t, o, `{"driver":"installed-test","enable":false}`)
	scan := `{"driver":"installed-test","supported":true,"identity":["serial","index"],"devices":[{"label":"Camera A","values":{"serial":"ABC","index":0}},{"label":"Camera B","values":{"serial":"DEF","index":1}}]}`
	if !supported {
		scan = `{"driver":"installed-test","supported":false,"identity":["addr"],"devices":[]}`
	}
	script := `#!/bin/sh
case "$1:$2" in
 -schema:commented) echo '{"driver":"installed-test","gain":23,"enable":false}';;
 -schema:json) echo '{"driver":"installed-test","fields":[{"name":"serial","label":"Serial","type":"text","default":""},{"name":"index","label":"Index","type":"number","default":"0"},{"name":"addr","label":"Address","type":"text","default":"","help":"host:port"},{"name":"gain","label":"Gain","type":"number","default":"10"}]}';;
 -discover:) echo '` + scan + `';;
 -config:*) exit 0;;
 *) exit 99;;
esac
`
	if err := os.WriteFile(exe, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	template := "{\n // keep this help\n \"driver\":\"installed-test\",\n \"exec\":\"" + exe + "\",\n // \"serial\": \"\",\n \"gain\":23,\n \"enable\":false\n}"
	writeFile(t, path, template)
	spec, err := loadDeviceFile(path, stateDevicesDir())
	if err != nil {
		t.Fatal(err)
	}
	o.rows[0].spec = spec
	o.rows[0].res = resolveDriver(spec)
	o.cfg.Devices = []DeviceSpec{spec}
	return o, path
}
func addRequest(o *orchestrator, path string, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	o.ServeHTTP(w, r)
	return w
}
func options(t *testing.T, o *orchestrator) *addDraft {
	t.Helper()
	w := addRequest(o, "/setup/add/options", url.Values{"driver": {"installed-test"}})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var d addDraft
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	return &d
}
func TestAddReusesPrototypeAndRejectsDuplicateDisabledDevice(t *testing.T) {
	o, path := addFixture(t, true)
	d := options(t, o)
	if d.Prototype != "cam" || len(d.Discovery.Devices) != 2 {
		t.Fatalf("%+v", d)
	}
	stale := options(t, o)
	inst, on, err := o.saveAddDevice(context.Background(), url.Values{"token": {d.Token}, "device": {"0"}, "name": {"Main camera"}})
	if err != nil || on || inst != "cam" {
		t.Fatalf("%s %v %v", inst, on, err)
	}
	data, _ := os.ReadFile(path)
	for _, want := range []string{"keep this help", `"serial": "ABC"`, `"gain":23`, `"name": "Main camera"`} {
		if !strings.Contains(string(data), want) {
			t.Fatal(string(data))
		}
	}
	spec, err := loadDeviceFile(path, stateDevicesDir())
	if err != nil || spec.enabled() || spec.Port < portScanBase {
		t.Fatalf("%+v %v", spec, err)
	}
	if _, _, err := o.saveAddDevice(context.Background(), url.Values{"token": {stale.Token}, "device": {"0"}}); err == nil || !strings.Contains(err.Error(), "already configured") {
		t.Fatalf("duplicate accepted: %v", err)
	}
	if _, _, err := o.saveAddDevice(context.Background(), url.Values{"token": {stale.Token}, "device": {"1"}}); err == nil {
		t.Fatal("stale prototype overwritten")
	}
	fresh := options(t, o)
	if fresh.Prototype != "" || fresh.Discovery.Devices[0].Configured != "cam" {
		t.Fatalf("%+v", fresh)
	}
	other, _, err := o.saveAddDevice(context.Background(), url.Values{"token": {fresh.Token}, "device": {"1"}})
	if err != nil || other == "cam" || !strings.HasPrefix(other, "installed-test-") {
		t.Fatalf("%s %v", other, err)
	}
	files, _ := filepath.Glob(filepath.Join(devicesDirFor(o.cfgPath), "*.json"))
	if len(files) != 2 {
		t.Fatal(files)
	}
	second, _ := loadDeviceFile(filepath.Join(devicesDirFor(o.cfgPath), other+".json"), stateDevicesDir())
	if second.Port == spec.Port {
		t.Fatal("port reused")
	}
}
func TestAddManualConnectionAndConcurrentFileEdit(t *testing.T) {
	o, path := addFixture(t, false)
	d := options(t, o)
	if _, _, err := o.saveAddDevice(context.Background(), url.Values{"token": {d.Token}}); err == nil {
		t.Fatal("missing address accepted")
	}
	original, _ := os.ReadFile(path)
	writeFile(t, path, string(original)+"\n// another editor\n")
	form := url.Values{"token": {d.Token}, "field.addr": {"mount.local:3492"}}
	if _, _, err := o.saveAddDevice(context.Background(), form); err == nil {
		t.Fatal("concurrent edit overwritten")
	}
	d = options(t, o)
	form.Set("token", d.Token)
	if _, _, err := o.saveAddDevice(context.Background(), form); err != nil {
		t.Fatal(err)
	}
	d = options(t, o)
	form.Set("token", d.Token)
	if _, _, err := o.saveAddDevice(context.Background(), form); err == nil || !strings.Contains(err.Error(), "already configured") {
		t.Fatalf("%v", err)
	}
}
func TestAddDoesNotOverwriteRuntimeIdentity(t *testing.T) {
	o, path := addFixture(t, true)
	writeFile(t, filepath.Join(stateDevicesDir(), "cam.json"), `{"serial":"ABC"}`)
	d := options(t, o)
	if d.Prototype != "" || d.Discovery.Devices[0].Configured != "cam" {
		t.Fatalf("%+v", d)
	}
	before, _ := os.ReadFile(path)
	_, _, err := o.saveAddDevice(context.Background(), url.Values{"token": {d.Token}, "device": {"0"}})
	if err == nil {
		t.Fatal("duplicate runtime identity")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("prototype changed")
	}
}
func TestAddRejectsUnknownFieldsAndUnselectedHardware(t *testing.T) {
	o, _ := addFixture(t, true)
	d := options(t, o)
	for _, form := range []url.Values{{"token": {d.Token}}, {"token": {d.Token}, "device": {"99"}}, {"token": {d.Token}, "device": {"0"}, "field.exec": {"/bin/false"}}} {
		if _, _, err := o.saveAddDevice(context.Background(), form); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
}
func TestAddPortSkipsConfiguredAndListeningPorts(t *testing.T) {
	listener, err := net.Listen("tcp", ":11300")
	if err != nil {
		t.Skip(err)
	}
	defer listener.Close()
	port, err := availableAddPort(map[int]bool{11301: true, 11302: true})
	if err != nil || port == 11300 || port == 11301 || port == 11302 {
		t.Fatalf("%d %v", port, err)
	}
}
func TestAddEnableUsesSupervisor(t *testing.T) {
	o, path := addFixture(t, true)
	d := options(t, o)
	sup := &enableOrderSupervisor{t: t, path: path, want: true}
	o.sup = sup
	w := addRequest(o, "/setup/add", url.Values{"token": {d.Token}, "device": {"0"}, "enable": {"true"}})
	if w.Code != 200 || strings.Join(sup.calls, ",") != "install,enable,start" {
		t.Fatalf("%s %v", w.Body.String(), sup.calls)
	}
}

func TestAddDetectsDisabledMultiCameraAssignments(t *testing.T) {
	o, path := addFixture(t, true)
	data, _ := os.ReadFile(path)
	text, err := patchDeviceText(string(data), map[string]json.RawMessage{"cameras": json.RawMessage(`[{"serial":"ABC","enable":false},{"serial":"DEF"}]`)})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, text)
	d := options(t, o)
	if d.Prototype != "" {
		t.Fatal("multi-camera configuration treated as a prototype")
	}
	for _, found := range d.Discovery.Devices {
		if found.Configured != "cam" {
			t.Fatalf("%+v", found)
		}
	}
}
func TestAddPrototypePatchKeepsCommentsAndExistingKeys(t *testing.T) {
	source := "{\n// serial comment\n\"Serial\":\"\",\"nested\":{\"serial\":\"keep\"},\"gain\":5,\"enable\":false\n}"
	text, err := patchDeviceText(source, map[string]json.RawMessage{"serial": json.RawMessage(`"ABC"`), "port": json.RawMessage(`11300`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "// serial comment") || !strings.Contains(text, `"nested":{"serial":"keep"}`) || !strings.Contains(text, `"Serial":"ABC"`) || strings.Count(text, `"serial"`) != 1 {
		t.Fatal(text)
	}
	clean, err := editorJSON(text)
	if err != nil || !json.Valid(clean) {
		t.Fatalf("%s %v", text, err)
	}
}
func TestAddDiscoveryDoesNotTrustArbitraryExecutable(t *testing.T) {
	o, _, _ := editorFixture(t)
	w := addRequest(o, "/setup/add/options", url.Values{"driver": {"/bin/sh"}})
	if w.Code == 200 {
		t.Fatal("arbitrary executable accepted")
	}
}

func TestAddPinsAstrocamPrototypeWithoutOpeningOtherSlot(t *testing.T) {
	o, path := addFixture(t, true)
	spec, _ := loadDeviceFile(path, stateDevicesDir())
	exe := spec.Exec
	script, _ := os.ReadFile(exe)
	script = []byte(strings.ReplaceAll(string(script), `"driver":"installed-test"`, `"driver":"astrocam"`))
	if err := os.WriteFile(exe, script, 0755); err != nil {
		t.Fatal(err)
	}
	shipped := `{
 "driver":"astrocam",
 "cameras":[
  { // first camera help
   // "serial":"",
   "name":"", "fpsPercent":60
  },
  { // keep second slot help
   "name":""
  }
 ],
 "enable":false
}`
	// The driver ships the array template; the seeded file is that template bound to exe.
	writeFile(t, exe+".tmpl", shipped)
	script = []byte(strings.Replace(string(script), "echo '{\"driver\":\"astrocam\",\"gain\":23,\"enable\":false}'", "cat "+strconv.Quote(exe+".tmpl"), 1))
	if err := os.WriteFile(exe, script, 0755); err != nil {
		t.Fatal(err)
	}
	template, err := newDeviceTemplate(context.Background(), o.cfgPath, "installed-test")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, template)
	spec, _ = loadDeviceFile(path, stateDevicesDir())
	o.rows[0].spec = spec
	o.cfg.Devices = []DeviceSpec{spec}
	d := options(t, o)
	if d.Prototype != "cam" {
		t.Fatal("shipped array not reused")
	}
	if _, _, err := o.saveAddDevice(context.Background(), url.Values{"token": {d.Token}, "device": {"0"}, "name": {"Main camera"}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	clean, _ := editorJSON(string(data))
	var config struct {
		Serial  string
		Enable  bool
		Cameras []struct {
			Serial     string
			Name       string
			FPSPercent int
			Enable     bool
		}
	}
	if err := json.Unmarshal(clean, &config); err != nil {
		t.Fatal(err)
	}
	if config.Serial != "" || config.Enable || len(config.Cameras) != 2 || config.Cameras[0].Serial != "ABC" || !config.Cameras[0].Enable || config.Cameras[0].Name != "Main camera" || config.Cameras[0].FPSPercent != 60 || config.Cameras[1].Enable {
		t.Fatalf("%s", data)
	}
	if !strings.Contains(string(data), "first camera help") || !strings.Contains(string(data), "keep second slot help") {
		t.Fatal("comments lost")
	}
	d = options(t, o)
	if d.Discovery.Devices[0].Configured != "cam" || d.Prototype != "" {
		t.Fatal("pinned camera offered twice")
	}
}
