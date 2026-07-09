package hurd

import (
	"testing"

	tenmicrondrv "github.com/mikefsq/goalpaca-devices/tenmicron"
)

// TestOpticsSharedAcrossFrontEnds proves the profile flow: the hurd injects ONE
// holder into the Alpaca telescope; a client sets optics over the Alpaca setoptics
// Action; and both the Alpaca readout (metres) and the INDI side (mm, via the same
// holder that feeds TELESCOPE_INFO) report the new values — no divergence.
func TestOpticsSharedAcrossFrontEnds(t *testing.T) {
	h := newOpticsHolder(200, 0, 1600, 0, 0) // mm in; guider defaults to the main scope
	tel := tenmicrondrv.NewTelescope("x")
	tel.UseOptics(h) // the same holder the INDI device would get via WithOptics

	// Client selects a profile → pushes optics over Alpaca (non-standard Action, mm).
	if _, err := tel.Action("setoptics", `{"focal_length":1000,"guider_focal_length":240}`); err != nil {
		t.Fatalf("setoptics: %v", err)
	}

	// Alpaca front-end reports it (metres — ASCOM's unit).
	if tel.FocalLength() != 1.0 || tel.ApertureDiameter() != 0.2 {
		t.Errorf("Alpaca: focal=%v aperture=%v want 1.0/0.2 (metres)", tel.FocalLength(), tel.ApertureDiameter())
	}

	// INDI front-end reads the SAME holder (millimetres) for TELESCOPE_INFO.
	ap, fl, gap, gfl := h.OpticsMM()
	if ap != 200 || fl != 1000 || gap != 200 || gfl != 240 {
		t.Errorf("INDI mm: aperture=%v focal=%v guiderAperture=%v guiderFocal=%v want 200/1000/200/240",
			ap, fl, gap, gfl)
	}
}
