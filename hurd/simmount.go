package hurd

import (
	"sync"

	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goalpaca/sim"
	"github.com/mikefsq/lx200"
)

// simMount is the Alpaca telescope simulator (goalpaca/sim) with a LiveMount seam, so
// the same simulated mount is exposed over Alpaca and drives the fleet's INDI and
// LX200 front-ends — a no-hardware testbed for all three protocols.
type simMount struct {
	*sim.Telescope
	lx     *simMountAdapter
	optics alpacadev.OpticsStore
}

func newSimMount(name string) *simMount {
	st := sim.NewTelescope()
	if name != "" {
		st.DevName = name
	}
	return &simMount{Telescope: st, lx: &simMountAdapter{t: st}}
}

// LiveMount exposes the simulator as a lx200.Mount for the INDI server and LX200
// bridge — the liveMounter seam the fleet wires those front-ends onto.
func (s *simMount) LiveMount() (lx200.Mount, error) { return s.lx, nil }

// UseOptics + the optics getters let the sim report a configured optical train over
// both Alpaca (ApertureDiameter/FocalLength) and INDI (TELESCOPE_INFO).
func (s *simMount) UseOptics(o alpacadev.OpticsStore) { s.optics = o }

func (s *simMount) ApertureDiameter() float64 {
	if s.optics != nil {
		a, _, _, _, _ := s.optics.Optics()
		return a
	}
	return s.Telescope.ApertureDiameter()
}

func (s *simMount) ApertureArea() float64 {
	if s.optics != nil {
		_, a, _, _, _ := s.optics.Optics()
		return a
	}
	return s.Telescope.ApertureArea()
}

func (s *simMount) FocalLength() float64 {
	if s.optics != nil {
		_, _, f, _, _ := s.optics.Optics()
		return f
	}
	return s.Telescope.FocalLength()
}

// simMountAdapter presents the sim.Telescope as a lx200.Mount (+ Guider, OpLocker,
// PierSider). All front-ends drive the one underlying simulated telescope.
type simMountAdapter struct {
	t    *sim.Telescope
	opMu sync.Mutex
}

func (a *simMountAdapter) RA() (float64, error)  { return a.t.RightAscension(), nil }
func (a *simMountAdapter) Dec() (float64, error) { return a.t.Declination(), nil }
func (a *simMountAdapter) SetTargetRA(h float64) (bool, error) {
	return true, a.t.SetTargetRightAscension(h)
}
func (a *simMountAdapter) SetTargetDec(d float64) (bool, error) {
	return true, a.t.SetTargetDeclination(d)
}
func (a *simMountAdapter) SlewToTarget() error           { return a.t.SlewToTargetAsync() }
func (a *simMountAdapter) SyncToTarget() (string, error) { return "Matched", a.t.SyncToTarget() }
func (a *simMountAdapter) Halt() error                   { return a.t.AbortSlew() }
func (a *simMountAdapter) Slewing() (bool, error)        { return a.t.Slewing(), nil }
func (a *simMountAdapter) Tracking() (bool, error)       { return a.t.Tracking(), nil }
func (a *simMountAdapter) SetTracking(on bool) error     { return a.t.SetTracking(on) }

// PulseGuide (lx200.Guider) issues a guide pulse and nudges the simulated star
// (simSky), closing the guide loop so corrections show in the sim camera's frames.
func (a *simMountAdapter) PulseGuide(d lx200.Direction, ms int) error {
	simSky.pulse(d, ms)
	return a.t.PulseGuide(guideDir(d), ms)
}

// OpLock (lx200.OpLocker) serializes the INDI/LX200 front-ends' multi-step gotos.
func (a *simMountAdapter) OpLock() func() { a.opMu.Lock(); return a.opMu.Unlock }

// PierSide (lx200.PierSider) reports the pier side, substituting East when the sim
// returns Unknown (it has no real pier).
func (a *simMountAdapter) PierSide() (lx200.PierSide, error) {
	s := lx200.PierSide(a.t.SideOfPier())
	if s == lx200.PierUnknown {
		s = lx200.PierEast
	}
	return s, nil
}

func guideDir(d lx200.Direction) alpacadev.GuideDirection {
	switch d {
	case lx200.South:
		return alpacadev.GuideSouth
	case lx200.East:
		return alpacadev.GuideEast
	case lx200.West:
		return alpacadev.GuideWest
	default:
		return alpacadev.GuideNorth
	}
}

var (
	_ lx200.Mount        = (*simMountAdapter)(nil)
	_ lx200.Guider       = (*simMountAdapter)(nil)
	_ lx200.OpLocker     = (*simMountAdapter)(nil)
	_ lx200.PierSider    = (*simMountAdapter)(nil)
	_ liveMounter        = (*simMount)(nil)
	_ opticsConfigurable = (*simMount)(nil)
)
