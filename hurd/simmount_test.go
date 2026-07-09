package hurd

import (
	"testing"

	"github.com/mikefsq/lx200"
)

// TestSimMountExposesLiveMount drives the simulated mount through the lx200.Mount
// surface the INDI server and LX200 bridge use — proving the sim feeds those
// front-ends, not just Alpaca.
func TestSimMountExposesLiveMount(t *testing.T) {
	sm := newSimMount("Sim")
	m, err := sm.LiveMount()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := m.SetTargetRA(5.5); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetTargetDec(22.0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SyncToTarget(); err != nil {
		t.Fatal(err)
	}
	ra, _ := m.RA()
	dec, _ := m.Dec()
	if !approxEq(ra, 5.5) || !approxEq(dec, 22.0) {
		t.Errorf("after sync: RA=%v Dec=%v want ~5.5/22.0", ra, dec)
	}

	if err := m.(lx200.Guider).PulseGuide(lx200.North, 100); err != nil {
		t.Errorf("pulse guide: %v", err)
	}
	unlock := m.(lx200.OpLocker).OpLock()
	unlock()
	if _, ok := m.(lx200.PierSider); !ok {
		t.Error("sim mount should report pier side")
	}
}

// TestSimMountReportsOptics confirms the sim reports a configured optical train
// (mm config → metres on the ASCOM getters), so TELESCOPE_INFO works against it.
func TestSimMountReportsOptics(t *testing.T) {
	sm := newSimMount("Sim")
	sm.UseOptics(newOpticsHolder(130, 0, 1000, 0, 0))
	if sm.ApertureDiameter() != 0.13 || sm.FocalLength() != 1.0 {
		t.Errorf("optics: aperture=%v focal=%v want 0.13/1.0 (metres)", sm.ApertureDiameter(), sm.FocalLength())
	}
}

func approxEq(a, b float64) bool { d := a - b; return d < 1e-3 && d > -1e-3 }
