package hurd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAddDoesNotReuseEditedDisabledFile(t *testing.T) {
	o, path := addFixture(t, true)
	data, _ := os.ReadFile(path)
	writeFile(t, path, strings.Replace(string(data), `"gain":23`, `"gain":40`, 1))
	if d := options(t, o); d.Prototype != "" {
		t.Fatalf("edited configuration %s offered as a prototype", d.Prototype)
	}
}

func TestPatchDeviceTextSetsSeveralKeysInOnePass(t *testing.T) {
	source := "{\n // help\n \"a\":1, \"A\":2,\n \"nested\":{\"b\":1}\n}"
	got, err := patchDeviceText(source, map[string]json.RawMessage{"a": json.RawMessage("5"), "b": json.RawMessage(`"x"`)})
	if err != nil {
		t.Fatal(err)
	}
	clean, _ := editorJSON(got)
	var m map[string]any
	if err := json.Unmarshal(clean, &m); err != nil {
		t.Fatal(got)
	}
	if m["a"] != 5.0 || m["A"] != 5.0 || m["b"] != "x" || !strings.Contains(got, `"nested":{"b":1}`) || !strings.Contains(got, "// help") {
		t.Fatal(got)
	}
}

func TestAddSkipsUnreadableDeviceFileWithWarning(t *testing.T) {
	o, _ := addFixture(t, true)
	writeFile(t, filepath.Join(devicesDirFor(o.cfgPath), "broken.json"), "{")
	d := options(t, o)
	if len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "broken.json") {
		t.Fatalf("%q", d.Warnings)
	}
}

func TestAddDraftsEvictOldestWhenFull(t *testing.T) {
	o, _ := addFixture(t, true)
	o.addDrafts = map[string]*addDraft{}
	for i := 0; i < maxAddDrafts; i++ {
		o.addDrafts[string(rune('a'+i))] = &addDraft{expires: time.Now().Add(time.Duration(i+1) * time.Minute)}
	}
	d := options(t, o)
	if _, ok := o.addDrafts[d.Token]; !ok || len(o.addDrafts) != maxAddDrafts {
		t.Fatalf("%d drafts", len(o.addDrafts))
	}
	if _, ok := o.addDrafts["a"]; ok {
		t.Fatal("oldest draft kept")
	}
}

func TestAddFallsBackForDriverWithoutDiscovery(t *testing.T) {
	o, path := addFixture(t, true)
	spec, _ := loadDeviceFile(path, stateDevicesDir())
	script, _ := os.ReadFile(spec.Exec)
	old := string(script)
	cut := old[strings.Index(old, " -schema:json)"):strings.Index(old, " -config:")]
	if err := os.WriteFile(spec.Exec, []byte(strings.Replace(old, cut, "", 1)), 0755); err != nil {
		t.Fatal(err)
	}
	d := options(t, o)
	if d.Discovery.Supported || d.Discovery.Error == "" || d.Discovery.Devices == nil || d.Discovery.Identity == nil || len(d.Warnings) == 0 {
		t.Fatalf("%+v", d)
	}
}
