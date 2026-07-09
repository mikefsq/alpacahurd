package hurd

import (
	alpacadev "github.com/mikefsq/goalpaca/server"
	"github.com/mikefsq/goalpaca/sim"
	"github.com/mikefsq/goindi/ccd"
)

// simCamera is the Alpaca camera simulator (goalpaca/sim) with a LiveCamera seam, so
// the same simulated camera is exposed over Alpaca and drives the fleet's INDI CCD
// device — a no-hardware guide camera over INDI.
type simCamera struct {
	*sim.Camera
	src ccd.Camera
}

func newSimCamera(name string) *simCamera {
	c := sim.NewCamera()
	if name != "" {
		c.DevName = name
	}
	// Render a drifting star coupled to the sim mount (closed guide loop), not the
	// sim's gradient.
	return &simCamera{Camera: c, src: &starCamSource{c: c}}
}

// ImageFrame overrides the embedded sim camera's gradient on the Alpaca path with the
// same drifting star the INDI front-end shows (coupled to simSky), placed at its
// full-frame position and cropped to the current ROI.
func (s *simCamera) ImageFrame() (alpacadev.ImageFrame, error) {
	f, err := s.Camera.ImageFrame() // honors ROI; gives geometry, element types, readiness
	if err != nil {
		return f, err
	}
	ox, oy := simSky.position()
	cx := float64(s.Camera.CameraXSize())/2 + ox - float64(s.Camera.StartX())
	cy := float64(s.Camera.CameraYSize())/2 + oy - float64(s.Camera.StartY())
	f.Pixels = renderStar(f.Width, f.Height, cx, cy)
	return f, nil
}

// LiveCamera exposes the simulator as a ccd.Camera for the INDI CCD device.
func (s *simCamera) LiveCamera() (ccd.Camera, error) { return s.src, nil }

var _ liveCamera = (*simCamera)(nil)
