package ui

import (
	"image/color"
	"math"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
)

// VoiceViz renders a microphone-level pulsing circle. The outer ring
// breathes with peak amplitude; the inner disc colours change based on
// VAD activity so the user can see at a glance whether the segmenter is
// firing. SetLevel is safe to call from any goroutine — Refresh is
// dispatched onto Fyne's UI thread internally.
//
// Animation choice: a critically-damped exponential smoothing of the
// raw level (alpha ≈ 0.35) feels closer to a "voice meter" than the
// raw 200 ms peak samples would; the eye perceives the smoothed signal
// as motion, the raw one as a stepping bar.
type VoiceViz struct {
	widget.BaseWidget

	mu       sync.Mutex
	level    float32 // 0..1
	smoothed float32
	active   bool // VAD currently inside an utterance
	clip     bool // peak hit the limit recently
}

// NewVoiceViz constructs a default-sized viz. The widget has a
// natural minimum size of 140×140; the parent layout can grow it.
func NewVoiceViz() *VoiceViz {
	v := &VoiceViz{}
	v.ExtendBaseWidget(v)
	return v
}

// SetLevel records a fresh amplitude reading. `level` is expected in
// the unit range [0, 1] (clamp here so callers don't have to).
// `vadActive` tells the viz the segmenter is currently inside an
// utterance, which we use to pick a "speaking" colour. Refresh is
// driven by the caller (a 30 Hz ticker in main): we do not Refresh
// inside SetLevel to avoid Fyne queueing dozens of redraws per second
// — the caller batches via fyne.Do().
func (v *VoiceViz) SetLevel(level float32, vadActive bool) {
	if level < 0 {
		level = 0
	}
	if level > 1 {
		level = 1
	}
	v.mu.Lock()
	v.level = level
	// Exponential smoothing — alpha controls "stickiness": higher =
	// snappier, lower = lazier. 0.35 keeps motion visible but not
	// jittery on noisy peak samples.
	v.smoothed = v.smoothed + 0.35*(level-v.smoothed)
	v.active = vadActive
	v.clip = level > 0.95
	v.mu.Unlock()
}

// snapshot returns a stable copy of the rendering state under the
// widget mutex, so the renderer doesn't race with SetLevel.
func (v *VoiceViz) snapshot() (level float32, active, clip bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.smoothed, v.active, v.clip
}

// CreateRenderer plumbs the widget into Fyne's render loop.
func (v *VoiceViz) CreateRenderer() fyne.WidgetRenderer {
	outer := canvas.NewCircle(color.NRGBA{R: 80, G: 110, B: 160, A: 80})
	inner := canvas.NewCircle(color.NRGBA{R: 120, G: 140, B: 180, A: 200})
	core := canvas.NewCircle(color.NRGBA{R: 200, G: 220, B: 255, A: 255})
	return &vizRenderer{
		v:     v,
		outer: outer,
		inner: inner,
		core:  core,
	}
}

type vizRenderer struct {
	v        *VoiceViz
	lastSize fyne.Size
	outer    *canvas.Circle
	inner    *canvas.Circle
	core     *canvas.Circle
}

func (r *vizRenderer) Destroy() {}

func (r *vizRenderer) Layout(size fyne.Size) {
	r.lastSize = size
	level, active, clip := r.v.snapshot()
	side := float32(math.Min(float64(size.Width), float64(size.Height)))

	// Outer ring scales 0.55..1.0 with level — it's the "breath".
	outerR := side * (0.55 + 0.45*level)
	// Inner is half of outer with a 0.4..0.85 amplitude band.
	innerR := side * (0.30 + 0.30*level)
	// Core stays nearly constant so there's always *something* visible
	// when the mic is silent, otherwise an idle viz looks broken.
	coreR := side * 0.20

	centerX := size.Width / 2
	centerY := size.Height / 2

	place := func(c *canvas.Circle, radius float32) {
		c.Resize(fyne.NewSize(radius, radius))
		c.Move(fyne.NewPos(centerX-radius/2, centerY-radius/2))
	}
	place(r.outer, outerR)
	place(r.inner, innerR)
	place(r.core, coreR)

	// Colour scheme communicates state:
	//   idle    → blue-grey palette
	//   active  → green-leaning (matches the rest of the app's "go" cue)
	//   clip    → red, signals the mic is overdriving
	switch {
	case clip:
		r.outer.FillColor = color.NRGBA{R: 220, G: 60, B: 60, A: 70}
		r.inner.FillColor = color.NRGBA{R: 220, G: 80, B: 80, A: 200}
		r.core.FillColor = color.NRGBA{R: 255, G: 200, B: 200, A: 255}
	case active:
		r.outer.FillColor = color.NRGBA{R: 60, G: 180, B: 110, A: 70}
		r.inner.FillColor = color.NRGBA{R: 80, G: 200, B: 130, A: 200}
		r.core.FillColor = color.NRGBA{R: 200, G: 255, B: 220, A: 255}
	default:
		r.outer.FillColor = color.NRGBA{R: 80, G: 110, B: 160, A: 70}
		r.inner.FillColor = color.NRGBA{R: 110, G: 140, B: 180, A: 200}
		r.core.FillColor = color.NRGBA{R: 200, G: 220, B: 255, A: 255}
	}
	r.outer.Refresh()
	r.inner.Refresh()
	r.core.Refresh()
}

func (r *vizRenderer) MinSize() fyne.Size {
	return fyne.NewSize(140, 140)
}

func (r *vizRenderer) Objects() []fyne.CanvasObject {
	// Order matters — outer first so it renders behind.
	return []fyne.CanvasObject{r.outer, r.inner, r.core}
}

func (r *vizRenderer) Refresh() {
	// Fyne calls Refresh when SetLevel-driven state changes; re-run
	// Layout with the last-known size so colours + circle radii pick
	// up the new snapshot. Before the first Layout we have no size,
	// so MinSize is a safe fallback.
	size := r.lastSize
	if size.IsZero() {
		size = r.MinSize()
	}
	r.Layout(size)
}
