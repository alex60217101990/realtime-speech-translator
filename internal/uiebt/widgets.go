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
	vector.DrawFilledCircle(dst, cx, cy, r, c, true)
}

func drawCircleBorder(dst *ebiten.Image, cx, cy, r, width float32, c color.NRGBA) {
	vector.StrokeCircle(dst, cx, cy, r, width, c, true)
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
