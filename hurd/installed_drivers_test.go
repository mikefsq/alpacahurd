package hurd

import (
	"context"
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
func TestInstalledDriverCatalogueAndTemplate(t *testing.T) {
	o, _, _ := editorFixture(t)
	exe, manifest := installTestDriver(t, o, "{\n // preserve the driver's help\n \"driver\":\"installed-test\",\n \"enable\":false\n}")
	page := logGet(o, "/setup/add").Body.String()
	if !strings.Contains(page, `value="installed-test"`) || !strings.Contains(page, `value="sim-camera"`) {
		t.Fatal("missing drivers")
	}
	text, err := newDeviceTemplate(context.Background(), o.cfgPath, "installed-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, exe) || !strings.Contains(text, "preserve the driver's help") {
		t.Fatal(text)
	}
	os.Remove(manifest)
	if strings.Contains(logGet(o, "/setup/add").Body.String(), `value="installed-test"`) {
		t.Fatal("uninstalled driver offered")
	}
}
func TestInstalledDriverRejectsUnsafeTemplate(t *testing.T) {
	for _, body := range []string{`{`, `{"driver":"","enable":false}`, `{"driver":"installed-test"}`, `{"driver":"installed-test","enable":true}`, `{"driver":"installed-test","enable":false,"exec":"/other"}`} {
		t.Run(body, func(t *testing.T) {
			o, _, _ := editorFixture(t)
			installTestDriver(t, o, body)
			if _, err := newDeviceTemplate(context.Background(), o.cfgPath, "installed-test"); err == nil {
				t.Fatal("unsafe template accepted")
			}
		})
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
	text, err := newDeviceTemplate(context.Background(), o.cfgPath, "installed-test")
	if err != nil || !strings.Contains(text, `"driver":"canonical-name"`) {
		t.Fatalf("%s %v", text, err)
	}
}
