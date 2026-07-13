package hurd

import (
	"math"
	"sync"
)

// opticsHolder is the shared source of truth for one mount's optical train. The
// herd injects the same holder into the Alpaca telescope (UseOptics) and the INDI
// mount device (WithOptics), so a setoptics Action over Alpaca is reported by INDI's
// TELESCOPE_INFO. It satisfies tenmicron-alpaca's OpticsStore (metres) and
// goindi/mount's Optics (mm) structurally.
type opticsHolder struct {
	mu                     sync.Mutex
	ap, area, fl, gap, gfl float64 // metres / m²
}

// newOpticsHolder seeds from config. Lengths are config millimetres stored as metres
// (ASCOM's unit for ApertureDiameter/FocalLength; INDI's mm comes back via OpticsMM).
// Area is m² (config), defaulted from the aperture. The guide scope defaults to the
// main scope (the OAG case).
func newOpticsHolder(apertureMM, apertureAreaM2, focalLengthMM, guiderApertureMM, guiderFocalLengthMM float64) *opticsHolder {
	ap := apertureMM / 1000
	fl := focalLengthMM / 1000
	gap := guiderApertureMM / 1000
	gfl := guiderFocalLengthMM / 1000
	if gap == 0 {
		gap = ap
	}
	if gfl == 0 {
		gfl = fl
	}
	area := apertureAreaM2
	if area == 0 && ap > 0 {
		r := ap / 2
		area = math.Pi * r * r
	}
	return &opticsHolder{ap: ap, area: area, fl: fl, gap: gap, gfl: gfl}
}

// Optics / SetOptics satisfy tenmicron-alpaca's OpticsStore (metres).
func (o *opticsHolder) Optics() (float64, float64, float64, float64, float64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ap, o.area, o.fl, o.gap, o.gfl
}

func (o *opticsHolder) SetOptics(ap, area, fl, gap, gfl float64) {
	o.mu.Lock()
	o.ap, o.area, o.fl, o.gap, o.gfl = ap, area, fl, gap, gfl
	o.mu.Unlock()
}

// OpticsMM satisfies goindi/mount's Optics (millimetres).
func (o *opticsHolder) OpticsMM() (aperture, focalLength, guiderAperture, guiderFocalLength float64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ap * 1000, o.fl * 1000, o.gap * 1000, o.gfl * 1000
}
