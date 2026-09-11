package hurd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteDeviceConfiguration(t *testing.T) {
	o, path, _ := editorFixture(t)
	state := filepath.Join(stateDevicesDir(), "cam.json")
	writeFile(t, state, `{"port":12345}`)
	list := filepath.Join(filepath.Dir(o.cfgPath), "drivers.conf")
	writeFile(t, list, "/usr/local/bin/driver\n")
	if err := o.deleteDevice(context.Background(), "cam"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("configuration still exists")
	}
	if len(o.rows) != 0 || len(o.cfg.Devices) != 0 {
		t.Fatal("instance still listed")
	}
	for _, p := range []string{state, list} {
		if _, err := os.Stat(p); err != nil {
			t.Fatal(err)
		}
	}
}
func TestDeleteRejectsEnabledAndUnknown(t *testing.T) {
	o, path, _ := editorFixture(t)
	on := true
	o.rows[0].spec.Enable = &on
	if err := o.deleteDevice(context.Background(), "cam"); err == nil {
		t.Fatal("deleted enabled instance")
	}
	if err := o.deleteDevice(context.Background(), "../cam"); err == nil {
		t.Fatal("unknown accepted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
