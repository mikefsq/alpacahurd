package hurd

import (
	"encoding/binary"
	"testing"

	"github.com/mikefsq/lx200"
)

func TestRenderStarHasPeak(t *testing.T) {
	w, h := 64, 48
	buf := renderStar(w, h, 32, 24)
	center := binary.LittleEndian.Uint16(buf[(24*w+32)*2:])
	corner := binary.LittleEndian.Uint16(buf[0:])
	if center < 30000 {
		t.Errorf("star center too faint: %d", center)
	}
	if corner > 5000 {
		t.Errorf("background too bright: %d", corner)
	}
}

// TestGuidePulseMovesStar: the mount's pulses move the simulated star (so PHD2's
// corrections are visible), and opposite directions reverse it — the closed loop.
func TestGuidePulseMovesStar(t *testing.T) {
	g := newGuideSim()
	g.driftX, g.driftY = 0, 0 // isolate the pulse from drift
	x0, _ := g.position()
	g.pulse(lx200.West, 1000)
	x1, _ := g.position()
	if x1 <= x0 {
		t.Errorf("West pulse should move the star +x: %v -> %v", x0, x1)
	}
	g.pulse(lx200.East, 1000)
	x2, _ := g.position()
	if x2 >= x1 {
		t.Errorf("East pulse should move it back -x: %v -> %v", x1, x2)
	}
}
