package hurd

import (
	"context"
	"errors"
	alpacadev "github.com/mikefsq/goalpaca/server"
	"strings"
	"testing"
	"time"
)

type pageStateSupervisor struct {
	noSupervisor
	calls int
	fail  bool
}

func (s *pageStateSupervisor) Name() string { return "systemd" }
func (s *pageStateSupervisor) Status(context.Context, string) (InstanceStatus, error) {
	s.calls++
	if s.fail {
		return InstanceStatus{}, errors.New("service unavailable")
	}
	return InstanceStatus{Installed: true, Running: true, State: "active (running)"}, nil
}
func TestSeparateDriverAndSystemdState(t *testing.T) {
	o, _, _ := editorFixture(t)
	sup := &pageStateSupervisor{}
	o.sup = sup
	o.rows[0].res = resolution{kind: installedBinary}
	for _, enabled := range []bool{false, true} {
		o.rows[0].spec.Enable = &enabled
		for _, heartbeat := range []bool{false, true} {
			o.rows[0].reg = nil
			if heartbeat {
				o.rows[0].reg = &alpacadev.Registration{Seen: time.Now(), Local: true}
			}
			page := logGet(o, "/setup").Body.String()
			if !strings.Contains(page, "<th>Driver State</th><th>Systemd State</th>") || !strings.Contains(page, "<td>active (running)</td>") {
				t.Fatal(page)
			}
			expected := "Disabled · Not verified"
			if enabled {
				expected = "Enabled · Not verified"
				if heartbeat {
					expected = "Enabled · Not verified"
				}
			}
			if !strings.Contains(page, expected) {
				t.Fatalf("missing %s", expected)
			}
		}
	}
	if sup.calls != 4 {
		t.Fatalf("status calls: %d", sup.calls)
	}
	sup.fail = true
	page := logGet(o, "/setup").Body.String()
	if !strings.Contains(page, "<td>status: service unavailable</td>") || !strings.Contains(page, "Enabled · Not verified") {
		t.Fatal(page)
	}
	o.rows[0].inProcess = true
	o.rows[0].res.kind = compiledIn
	page = logGet(o, "/setup").Body.String()
	if !strings.Contains(page, "<td>Not applicable</td>") || sup.calls != 5 {
		t.Fatal("queried systemd for built-in driver")
	}
}
