package uiebt

import (
	"image/color"
	"log/slog"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
)

// Fonts holds the three text faces every widget uses. Sized once
// at boot from a single TTF (Go's own font, embedded) so the
// binary stays self-contained — no system-font lookup, no Cairo /
// FreeType runtime dep. Title / Body / Caption cover everything in
// the mock.
type Fonts struct {
	Title   text.Face // header, bold prominence
	Body    text.Face // chat bubble text
	Caption text.Face // timestamps, status line, small labels
}

// MustLoadFonts panics on failure — fonts are static assets, a
// missing face means the binary itself is corrupt.
func MustLoadFonts() Fonts {
	ttf, err := opentype.Parse(goregular.TTF)
	if err != nil {
		slog.Error("uiebt: failed to parse goregular TTF", "err", err)
		panic(err)
	}
	mk := func(sizePx float64) text.Face {
		face, ferr := opentype.NewFace(ttf, &opentype.FaceOptions{
			Size:    sizePx,
			DPI:     72,
			Hinting: 0,
		})
		if ferr != nil {
			panic(ferr)
		}
		return text.NewGoXFace(face)
	}
	return Fonts{
		Title:   mk(20),
		Body:    mk(16),
		Caption: mk(12),
	}
}

// drawText paints s at (x, y) with the given face and colour. The
// y baseline matches the text package's metric so callers can
// align against widget borders without computing ascent manually.
func drawText(dst *ebiten.Image, s string, face text.Face, x, y int, c color.NRGBA) {
	op := &text.DrawOptions{}
	op.GeoM.Translate(float64(x), float64(y))
	op.ColorScale.ScaleWithColor(c)
	text.Draw(dst, s, face, op)
}

// measureText returns the rendered width / height of s in pixels.
func measureText(s string, face text.Face) (w, h float64) {
	return text.Measure(s, face, 1.4)
}

// drawTextCentered paints s centred at (cx, cy) — useful for pill
// / button labels where computing the baseline by hand drifts. Uses
// ebiten's built-in alignment options so the glyph layer takes
// care of the math.
func drawTextCentered(dst *ebiten.Image, s string, face text.Face, cx, cy int, c color.NRGBA) {
	op := &text.DrawOptions{}
	op.GeoM.Translate(float64(cx), float64(cy))
	op.LayoutOptions.PrimaryAlign = text.AlignCenter
	op.LayoutOptions.SecondaryAlign = text.AlignCenter
	op.ColorScale.ScaleWithColor(c)
	text.Draw(dst, s, face, op)
}
