package hurd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeviceEnablePreservesComments(t *testing.T) {
	for _, source := range []string{
		"{\n // enable: false\n \"driver\":\"sim-camera\",\"enable\":false /* keep */\n}",
		`{"driver":"sim-camera","nested":{"enable":false},"enable":false,"Enable":false}`,
		`{"driver":"sim-camera"}`, `{ /* keep */ }`,
	} {
		for _, on := range []bool{true, false} {
			got, err := deviceEnableText(source, on)
			if err != nil {
				t.Fatal(err)
			}
			clean, _ := editorJSON(got)
			if !strings.Contains(string(clean), `"enable":`) {
				t.Fatal(got)
			}
			if strings.Contains(source, "/* keep */") && !strings.Contains(got, "/* keep */") {
				t.Fatal("lost comment")
			}
			if strings.Contains(source, `"nested":{"enable":false}`) && !strings.Contains(got, `"nested":{"enable":false}`) {
				t.Fatal("changed nested flag")
			}
			path := filepath.Join(t.TempDir(), "cam.json")
			writeFile(t, path, got)
			spec, err := loadDeviceFile(path, "")
			if err != nil || spec.enabled() != on {
				t.Fatalf("%s: %v", got, err)
			}
		}
	}
}

func TestDeviceEnableIgnoresLegacyState(t *testing.T) {
	o, path, _ := editorFixture(t)
	writeFile(t, filepath.Join(stateDevicesDir(), "cam.json"), `{"Enable":true,"enable":true,"port":12345}`)
	spec, err := loadDeviceFile(path, stateDevicesDir())
	if err != nil || spec.enabled() {
		t.Fatalf("legacy enable won: %+v %v", spec, err)
	}
	// Invalid enabled configuration is rejected before any saved change.
	bad := `{"driver":"sim-camera","enable":false,"pixelCountX":"bad"}`
	writeFile(t, path, bad)
	if _, err := o.setEnabled(context.Background(), "cam", true); err == nil {
		t.Fatal("invalid enable accepted")
	}
	got, _ := os.ReadFile(path)
	if string(got) != bad {
		t.Fatal("failed validation changed configuration")
	}
}

type enableOrderSupervisor struct {
	noSupervisor
	t     *testing.T
	path  string
	want  bool
	calls []string
}

func (s *enableOrderSupervisor) record(action string) error {
	spec, err := loadDeviceFile(s.path, stateDevicesDir())
	if err != nil || spec.enabled() != s.want {
		s.t.Fatalf("%s called before saved flag: %v", action, err)
	}
	s.calls = append(s.calls, action)
	return nil
}
func (s *enableOrderSupervisor) Install(context.Context, string) error { return s.record("install") }
func (s *enableOrderSupervisor) Enable(context.Context, string) error  { return s.record("enable") }
func (s *enableOrderSupervisor) Start(context.Context, string) error   { return s.record("start") }
func (s *enableOrderSupervisor) Stop(context.Context, string) error    { return s.record("stop") }
func (s *enableOrderSupervisor) Disable(context.Context, string) error { return s.record("disable") }

func TestDeviceEnableSupervisorFollowsConfig(t *testing.T) {
	o, path, _ := editorFixture(t)
	exe := filepath.Join(t.TempDir(), "test-driver")
	writeFile(t, exe, "#!/bin/sh\n[ \"$2\" != '' ]\n")
	if err := os.Chmod(exe, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, `{"driver":"external-test","exec":"`+exe+`","enable":false}`)
	spec, err := loadDeviceFile(path, stateDevicesDir())
	if err != nil {
		t.Fatal(err)
	}
	o.rows[0].spec = spec
	sup := &enableOrderSupervisor{t: t, path: path, want: true}
	o.sup = sup
	if _, err := o.setEnabled(context.Background(), "cam", true); err != nil {
		t.Fatal(err)
	}
	if strings.Join(sup.calls, ",") != "install,enable,start" {
		t.Fatal(sup.calls)
	}
	sup.want = false
	if _, err := o.setEnabled(context.Background(), "cam", false); err != nil {
		t.Fatal(err)
	}
	if strings.Join(sup.calls, ",") != "install,enable,start,stop,disable" {
		t.Fatal(sup.calls)
	}
	if o.rows[0].spec.enabled() {
		t.Fatal("disabled row still enabled")
	}
}
