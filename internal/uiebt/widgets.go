package uiebt

import (
	"image"
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// drawRoundRect paints a filled rounded rectangle directly inside r
// with the given colour. Convenience wrapper around the Path
// helpers so widgets do not repeat the FillPath boilerplate.
func drawRoundRect(dst *ebiten.Image, r image.Rectangle, radius float32, c color.NRGBA) {
	path := roundedRectPath(
		float32(r.Min.X), float32(r.Min.Y),
		float32(r.Dx()), float32(r.Dy()),
		clampRadius(radius, r),
	)
	var cs ebiten.ColorScale
	cs.ScaleWithColor(c)
	vector.FillPath(dst, path,
		&vector.FillOptions{},
		&vector.DrawPathOptions{AntiAlias: true, ColorScale: cs},
	)
}

// drawRoundRectBorder paints only the outline.
func drawRoundRectBorder(dst *ebiten.Image, r image.Rectangle, radius, width float32, c color.NRGBA) {
	path := roundedRectPath(
		float32(r.Min.X), float32(r.Min.Y),
		float32(r.Dx()), float32(r.Dy()),
		clampRadius(radius, r),
	)
	var cs ebiten.ColorScale
	cs.ScaleWithColor(c)
	vector.StrokePath(dst, path,
		&vector.StrokeOptions{Width: width},
		&vector.DrawPathOptions{AntiAlias: true, ColorScale: cs},
	)
}

// drawFilledCircle is the same trick for circles — used by the
// mic / language switch / end-call buttons.
func drawFilledCircle(dst *ebiten.Image, cx, cy, r float32, c color.NRGBA) {
	vector.FillCircle(dst, cx, cy, r, c, true)
}

func drawCircleBorder(dst *ebiten.Image, cx, cy, r, width float32, c color.NRGBA) {
	vector.StrokeCircle(dst, cx, cy, r, width, c, true)
}

// drawCheckmark paints a two-stroke check inside a square anchored
// at (cx, cy) with the given side length. Used instead of the
// U+2713 glyph (missing from the embedded goregular font).
func drawCheckmark(dst *ebiten.Image, cx, cy, side, stroke float32, c color.NRGBA) {
	half := side / 2
	p := &vector.Path{}
	p.MoveTo(cx-half*0.7, cy)
	p.LineTo(cx-half*0.1, cy+half*0.55)
	p.LineTo(cx+half*0.8, cy-half*0.55)
	var cs ebiten.ColorScale
	cs.ScaleWithColor(c)
	vector.StrokePath(dst, p,
		&vector.StrokeOptions{Width: stroke, LineCap: vector.LineCapRound, LineJoin: vector.LineJoinRound},
		&vector.DrawPathOptions{AntiAlias: true, ColorScale: cs},
	)
}

// drawClose paints an "×" via two crossed strokes for the same
// reason — U+2715 is not in goregular.
func drawClose(dst *ebiten.Image, cx, cy, side, stroke float32, c color.NRGBA) {
	half := side / 2
	var cs ebiten.ColorScale
	cs.ScaleWithColor(c)
	p1 := &vector.Path{}
	p1.MoveTo(cx-half, cy-half)
	p1.LineTo(cx+half, cy+half)
	p2 := &vector.Path{}
	p2.MoveTo(cx-half, cy+half)
	p2.LineTo(cx+half, cy-half)
	opts := &vector.StrokeOptions{Width: stroke, LineCap: vector.LineCapRound}
	dp := &vector.DrawPathOptions{AntiAlias: true, ColorScale: cs}
	vector.StrokePath(dst, p1, opts, dp)
	vector.StrokePath(dst, p2, opts, dp)
}

// drawMicGlyph paints a stylised microphone (rounded capsule body
// + curved base + stem) centred on (cx, cy) sized to fit inside a
// circle of radius bound. Used over the bottom-bar mic button.
func drawMicGlyph(dst *ebiten.Image, cx, cy, bound float32, c color.NRGBA) {
	bodyW := bound * 0.55
	bodyH := bound * 1.0
	bodyTop := cy - bodyH/2 - bound*0.05
	bodyRect := image.Rect(
		int(cx-bodyW/2), int(bodyTop),
		int(cx+bodyW/2), int(bodyTop+bodyH),
	)
	drawRoundRect(dst, bodyRect, bodyW/2, c)

	// Stem dropping below the body.
	stemTop := bodyTop + bodyH + bound*0.05
	stemBot := cy + bound*0.85
	stemW := bound * 0.08
	stemRect := image.Rect(
		int(cx-stemW/2), int(stemTop),
		int(cx+stemW/2), int(stemBot),
	)
	drawRoundRect(dst, stemRect, stemW/2, c)

	// Foot — short horizontal line under the stem.
	footY := stemBot + bound*0.04
	footW := bound * 0.55
	footH := bound * 0.10
	footRect := image.Rect(
		int(cx-footW/2), int(footY),
		int(cx+footW/2), int(footY+footH),
	)
	drawRoundRect(dst, footRect, footH/2, c)

	// Cradle — a thin arc under the body. Approximated as a
	// rounded rect with a hole would be expensive; we render two
	// strokes instead.
	cradleR := bodyW*0.8 + bound*0.1
	stroke := bound * 0.08
	cradle := &vector.Path{}
	cradle.MoveTo(cx-cradleR, cy+bound*0.10)
	cradle.QuadTo(cx, cy+bound*0.70, cx+cradleR, cy+bound*0.10)
	var cs ebiten.ColorScale
	cs.ScaleWithColor(c)
	vector.StrokePath(dst, cradle,
		&vector.StrokeOptions{Width: stroke, LineCap: vector.LineCapRound},
		&vector.DrawPathOptions{AntiAlias: true, ColorScale: cs},
	)
}

func clampRadius(r float32, rect image.Rectangle) float32 {
	w := float32(rect.Dx())
	h := float32(rect.Dy())
	if r > w/2 {
		r = w / 2
	}
	if r > h/2 {
		r = h / 2
	}
	if r < 0 {
		r = 0
	}
	return r
}
