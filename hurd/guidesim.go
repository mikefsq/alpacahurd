package hurd

import (
	"encoding/binary"
	"math"
	"sync"
	"time"

	"github.com/mikefsq/goalpaca/sim"
	"github.com/mikefsq/goindi/ccd"
	"github.com/mikefsq/lx200"
)

// simSky couples the sim mount and sim camera into a closed guiding loop: a star
// drifts (simulating tracking error), the mount's guide pulses move it back, and the
// camera renders it at its current position. Package-level because the sim mount and
// sim camera are separate fleet devices that must share one "sky".
var simSky = newGuideSim()

type guideSim struct {
	mu             sync.Mutex
	x, y           float64 // star offset from frame centre, pixels
	lastT          time.Time
	driftX, driftY float64 // pixels/sec of uncorrected tracking error
	pixPerMs       float64 // star movement per ms of guide pulse
}

func newGuideSim() *guideSim {
	return &guideSim{lastT: time.Now(), driftX: 0.20, driftY: 0.05, pixPerMs: 0.006}
}

// advance integrates the drift since the last call; the caller holds mu.
func (g *guideSim) advance() {
	now := time.Now()
	dt := now.Sub(g.lastT).Seconds()
	g.x += g.driftX * dt
	g.y += g.driftY * dt
	g.lastT = now
}

// position returns the star's current offset from the frame centre (pixels).
func (g *guideSim) position() (float64, float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.advance()
	return g.x, g.y
}

// pulse moves the star a calibrated amount in the guide direction (pixPerMs * ms).
func (g *guideSim) pulse(d lx200.Direction, ms int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.advance()
	step := g.pixPerMs * float64(ms)
	switch d {
	case lx200.North:
		g.y -= step
	case lx200.South:
		g.y += step
	case lx200.East:
		g.x -= step
	case lx200.West:
		g.x += step
	}
}

// starCamSource is a ccd.Camera frame source that renders a Gaussian star (coupled to
// simSky) in place of the sim camera's gradient, reusing sim.Camera for exposure
// timing and geometry.
type starCamSource struct {
	c *sim.Camera
}

func (s *starCamSource) PixelSizeUm() (float64, float64)  { return s.c.PixelSizeX(), s.c.PixelSizeY() }
func (s *starCamSource) Size() (int, int)                 { return s.c.CameraXSize(), s.c.CameraYSize() }
func (s *starCamSource) BitsPerPixel() int                { return 16 }
func (s *starCamSource) StartExposure(secs float64) error { return s.c.StartExposure(secs, true) }
func (s *starCamSource) ImageReady() bool                 { return s.c.ImageReady() }
func (s *starCamSource) AbortExposure() error             { return s.c.AbortExposure() }

func (s *starCamSource) Frame() (int, int, []byte, error) {
	w, h := s.c.CameraXSize(), s.c.CameraYSize()
	ox, oy := simSky.position()
	return w, h, renderStar(w, h, float64(w)/2+ox, float64(h)/2+oy), nil
}

// renderStar paints a 16-bit mono frame: a faint background plus a bright Gaussian
// star centred at (sx, sy).
func renderStar(w, h int, sx, sy float64) []byte {
	const (
		bg    = 1200
		peak  = 50000.0
		sigma = 2.2
	)
	buf := make([]byte, w*h*2)
	for i := 0; i < w*h; i++ {
		binary.LittleEndian.PutUint16(buf[i*2:], bg)
	}
	cx, cy := int(sx+0.5), int(sy+0.5)
	r := int(math.Ceil(4*sigma)) + 1
	for yy := cy - r; yy <= cy+r; yy++ {
		if yy < 0 || yy >= h {
			continue
		}
		for xx := cx - r; xx <= cx+r; xx++ {
			if xx < 0 || xx >= w {
				continue
			}
			dx, dy := float64(xx)-sx, float64(yy)-sy
			val := bg + int(peak*math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma)))
			if val > 65535 {
				val = 65535
			}
			binary.LittleEndian.PutUint16(buf[(yy*w+xx)*2:], uint16(val))
		}
	}
	return buf
}

var _ ccd.Camera = (*starCamSource)(nil)
