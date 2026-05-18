// Package uiebt is the Ebiten-based native UI for the realtime
// speech translator. It is a parallel front-end to the Fyne shell
// at cmd/translator and replaces the Wails track originally
// proposed in docs/UI_REWRITE_PLAN.md.
//
// Design tokens — colors / radii / spacing — live in theme.go so
// every widget reads from one place.
package uiebt

import "image/color"

// Color tokens. These match the dark glassmorphic mock in
// docs/ui-mock-2026-05.png. Each color names what it is for, not
// what it looks like, so theme variants are a single struct swap.
type Theme struct {
	BgTop      color.NRGBA // gradient top
	BgBottom   color.NRGBA // gradient bottom
	Card       color.NRGBA // card fill
	CardBorder color.NRGBA // card 1px border
	CardActive color.NRGBA // selected / hovered card outline glow

	TextPrimary   color.NRGBA
	TextSecondary color.NRGBA
	TextMuted     color.NRGBA
	TextAccent    color.NRGBA // links / username highlight

	AccentBlue   color.NRGBA // mic ring inner
	AccentCyan   color.NRGBA // sphere outer rim
	AccentMagent color.NRGBA // sphere inner fold
	AccentGreen  color.NRGBA // status dot
	AccentRed    color.NRGBA // end-call / error
	AccentAmber  color.NRGBA // warning

	// Sphere palette — separate so the shader uniforms can be
	// derived without dragging the rest of the theme.
	SphereInner color.NRGBA
	SphereOuter color.NRGBA
	SphereGlow  color.NRGBA
}

// DefaultTheme returns the dark-cosmic palette from the reference
// mock.
func DefaultTheme() Theme {
	return Theme{
		BgTop:      color.NRGBA{R: 0x0E, G: 0x10, B: 0x22, A: 0xFF},
		BgBottom:   color.NRGBA{R: 0x05, G: 0x06, B: 0x14, A: 0xFF},
		Card:       color.NRGBA{R: 0x18, G: 0x1B, B: 0x2E, A: 0xC8},
		CardBorder: color.NRGBA{R: 0x2A, G: 0x2E, B: 0x48, A: 0xFF},
		CardActive: color.NRGBA{R: 0x4C, G: 0x76, B: 0xFF, A: 0xFF},

		TextPrimary:   color.NRGBA{R: 0xEA, G: 0xEC, B: 0xF4, A: 0xFF},
		TextSecondary: color.NRGBA{R: 0xB0, G: 0xB6, B: 0xC8, A: 0xFF},
		TextMuted:     color.NRGBA{R: 0x6E, G: 0x76, B: 0x90, A: 0xFF},
		TextAccent:    color.NRGBA{R: 0x6F, G: 0xA0, B: 0xFF, A: 0xFF},

		AccentBlue:   color.NRGBA{R: 0x4C, G: 0x76, B: 0xFF, A: 0xFF},
		AccentCyan:   color.NRGBA{R: 0x5F, G: 0xC8, B: 0xFF, A: 0xFF},
		AccentMagent: color.NRGBA{R: 0xC8, G: 0x5F, B: 0xFF, A: 0xFF},
		AccentGreen:  color.NRGBA{R: 0x5F, G: 0xE0, B: 0x9A, A: 0xFF},
		AccentRed:    color.NRGBA{R: 0xEF, G: 0x65, B: 0x6A, A: 0xFF},
		AccentAmber:  color.NRGBA{R: 0xFF, G: 0xC4, B: 0x66, A: 0xFF},

		SphereInner: color.NRGBA{R: 0x36, G: 0x67, B: 0xFF, A: 0xFF},
		SphereOuter: color.NRGBA{R: 0x9F, G: 0x44, B: 0xFF, A: 0xFF},
		SphereGlow:  color.NRGBA{R: 0x6C, G: 0x88, B: 0xFF, A: 0xFF},
	}
}

// Spacing tokens. Use named values; never write a raw 12 in a
// widget.
const (
	SpaceXS = 4
	SpaceS  = 8
	SpaceM  = 12
	SpaceL  = 16
	SpaceXL = 24
	SpaceXX = 32
)

// Radii.
const (
	RadiusCard   = 20
	RadiusPill   = 999 // anything large makes the rect a pill
	RadiusButton = 14
)
