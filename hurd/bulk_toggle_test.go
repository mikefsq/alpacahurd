package hurd

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type bulkSupervisor struct {
	noSupervisor
	calls []string
	fail  string
}

func (s *bulkSupervisor) record(action, inst string) error {
	s.calls = append(s.calls, action+":"+inst)
	if inst == s.fail && (action == "start" || action == "stop") {
		return fmt.Errorf("service failure")
	}
	return nil
}
func (s *bulkSupervisor) Install(_ context.Context, inst string) error {
	return s.record("install", inst)
}
func (s *bulkSupervisor) Enable(_ context.Context, inst string) error {
	return s.record("enable", inst)
}
func (s *bulkSupervisor) Start(_ context.Context, inst string) error { return s.record("start", inst) }
func (s *bulkSupervisor) Stop(_ context.Context, inst string) error  { return s.record("stop", inst) }
func (s *bulkSupervisor) Disable(_ context.Context, inst string) error {
	return s.record("disable", inst)
}

func TestBulkToggleContinuesDeduplicatesAndSyncs(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprint(on), func(t *testing.T) {
			o, path, _ := editorFixture(t)
			exe := filepath.Join(t.TempDir(), "test-driver")
			if err := os.WriteFile(exe, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
				t.Fatal(err)
			}
			o.rows = nil
			o.cfg.Devices = nil
			for _, inst := range []string{"bad", "good"} {
				file := filepath.Join(filepath.Dir(path), inst+".json")
				writeFile(t, file, fmt.Sprintf(`{"driver":"external-test","exec":%q,"enable":%t}`, exe, !on))
				spec, err := loadDeviceFile(file, stateDevicesDir())
				if err != nil {
					t.Fatal(err)
				}
				row := orchRow{spec: spec, res: resolveDriver(spec)}
				o.rows = append(o.rows, row, row)
				o.cfg.Devices = append(o.cfg.Devices, spec)
			}
			inline := o.rows[0]
			inline.spec.Instance = "(inline)"
			unmanaged := inline
			unmanaged.spec.Instance, unmanaged.spec.Source = "unmanaged", ""
			o.rows = append(o.rows, inline, unmanaged)
			sup := &bulkSupervisor{fail: "bad"}
			o.sup = sup
			message, err := o.setAllEnabled(context.Background(), on)
			if err == nil || !strings.Contains(err.Error(), "bad") {
				t.Fatalf("missing failure: %v", err)
			}
			if !strings.Contains(message, "1 device configurations") || !strings.Contains(message, "2 inline or unmanaged entries skipped") {
				t.Fatal(message)
			}
			want := "stop:bad,disable:bad,stop:good,disable:good"
			if on {
				want = "install:bad,enable:bad,start:bad,stop:bad,disable:bad,install:good,enable:good,start:good"
			}
			if got := strings.Join(sup.calls, ","); got != want {
				t.Fatalf("calls %s, want %s", got, want)
			}
			for _, spec := range o.cfg.Devices {
				wantOn := on && spec.Instance == "good"
				disk, err := loadDeviceFile(spec.Source, stateDevicesDir())
				if err != nil || disk.enabled() != wantOn || spec.enabled() != wantOn {
					t.Fatalf("%s state out of sync: disk=%v memory=%v err=%v", spec.Instance, disk.enabled(), spec.enabled(), err)
				}
			}
			for _, row := range o.rows {
				if row.spec.Instance == "bad" && row.spec.enabled() {
					t.Fatal("failed device row still enabled")
				}
			}
		})
	}
}

func TestBulkActionRedirectAndEmpty(t *testing.T) {
	o, path, _ := editorFixture(t)
	writeFile(t, path, `{"driver":"sim-camera","enable":true}`)
	w := addRequest(o, "/setup", url.Values{"action": {"disable-all"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	location := w.Header().Get("Location")
	page := logGet(o, location)
	if !strings.Contains(page.Body.String(), "1 device configurations disabled") {
		t.Fatal(page.Body.String())
	}
	if strings.Contains(logGet(o, location).Body.String(), "1 device configurations disabled") {
		t.Fatal("flash repeated after refresh")
	}
	for _, label := range []string{"Enable all", "Disable all", "Restart alpacahurd"} {
		if !strings.Contains(page.Body.String(), label) {
			t.Fatal("missing " + label)
		}
	}
	o.rows = nil
	message, err := o.setAllEnabled(context.Background(), true)
	if err != nil || message != "0 device configurations enabled" {
		t.Fatal(message, err)
	}
}
