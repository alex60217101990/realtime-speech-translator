package uiebt

import (
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// Window size matching the reference mock. Layout below is a
// fixed-grid for now (header / left pane / sphere / right pane /
// bottom bar). A responsive layout will replace this once we have
// real text measurement helpers and the sphere shader is in place.
const (
	WindowWidth  = 1280
	WindowHeight = 800
)

// App is the top-level Ebiten Game. The first revision is
// deliberately minimal — it paints the dark vertical gradient
// background and the window chrome so we can iterate on widgets
// without scope creep. Subsequent commits attach panes, sphere,
// bindings to internal/app.Session, etc.
type App struct {
	theme Theme

	// frame is the offscreen image we draw into every tick. Resized
	// in Layout when the window dims change.
	frame *ebiten.Image

	bgWidth, bgHeight int
	bgImage           *ebiten.Image
}

// NewApp constructs a default-theme App. Call Run to enter the
// Ebiten loop.
func NewApp() *App {
	return &App{theme: DefaultTheme()}
}

// Update advances logic. We have nothing to tick yet — return nil
// so the loop runs at the configured TPS.
func (a *App) Update() error {
	return nil
}

// Draw paints one frame.
func (a *App) Draw(screen *ebiten.Image) {
	a.ensureBackground(screen.Bounds().Dx(), screen.Bounds().Dy())
	screen.DrawImage(a.bgImage, nil)

	// Placeholder content so the empty window is visibly the new
	// app rather than a black rectangle. Replaced in Stage 2 by
	// the header + panes.
	cardW, cardH := 480, 64
	cx := (screen.Bounds().Dx() - cardW) / 2
	cy := SpaceXX
	a.drawCard(screen, cx, cy, cardW, cardH)
	a.drawCenteredText(screen,
		cx, cy, cardW, cardH,
		"Realtime Speech Translator",
		a.theme.TextPrimary,
	)
}

// Layout reports the logical-pixel dimensions Ebiten should render
// into. We pin to the mock size; the window manager handles
// physical → logical scaling.
func (a *App) Layout(outerWidth, outerHeight int) (int, int) {
	return WindowWidth, WindowHeight
}

// Run sets up the window and enters the Ebiten event loop. Blocks
// until the user closes the window.
func Run() error {
	ebiten.SetWindowTitle("Realtime Speech Translator")
	ebiten.SetWindowSize(WindowWidth, WindowHeight)
	ebiten.SetWindowResizable(true)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetTPS(60)
	return ebiten.RunGame(NewApp())
}

// ensureBackground rebuilds the cached gradient image when the
// window dimensions change. Painting a per-pixel gradient every
// frame would be wasteful; this is a one-time cost per resize.
func (a *App) ensureBackground(w, h int) {
	if a.bgImage != nil && a.bgWidth == w && a.bgHeight == h {
		return
	}
	a.bgWidth = w
	a.bgHeight = h
	a.bgImage = ebiten.NewImage(w, h)

	top := a.theme.BgTop
	bot := a.theme.BgBottom
	for y := 0; y < h; y++ {
		t := float64(y) / float64(h-1)
		c := color.NRGBA{
			R: lerp(top.R, bot.R, t),
			G: lerp(top.G, bot.G, t),
			B: lerp(top.B, bot.B, t),
			A: 0xFF,
		}
		vector.DrawFilledRect(a.bgImage, 0, float32(y), float32(w), 1, c, false)
	}
}

// drawCard paints a rounded-rectangle card with the theme's card
// fill and border. Coordinates are top-left.
//
// The ebiten/v2/vector package (v2.9) ships rect / circle / line
// primitives but no built-in rounded-rect, so we compose one out of
// MoveTo + ArcTo on a reusable Path.
func (a *App) drawCard(dst *ebiten.Image, x, y, w, h int) {
	r := float32(RadiusCard)
	if r > float32(w)/2 {
		r = float32(w) / 2
	}
	if r > float32(h)/2 {
		r = float32(h) / 2
	}
	path := roundedRectPath(float32(x), float32(y), float32(w), float32(h), r)

	// vector paints in white and lets ColorScale tint the result —
	// we precompute one ColorScale per draw call from the NRGBA
	// theme tokens.
	var fillCS ebiten.ColorScale
	fillCS.ScaleWithColor(a.theme.Card)
	vector.FillPath(dst, path,
		&vector.FillOptions{},
		&vector.DrawPathOptions{AntiAlias: true, ColorScale: fillCS},
	)

	var strokeCS ebiten.ColorScale
	strokeCS.ScaleWithColor(a.theme.CardBorder)
	vector.StrokePath(dst, path,
		&vector.StrokeOptions{Width: 1},
		&vector.DrawPathOptions{AntiAlias: true, ColorScale: strokeCS},
	)
}

// roundedRectPath builds an outlined rounded-rectangle path with
// corner radius r, starting at the top-left corner. Used by drawCard
// and any future widget that wants a card silhouette.
func roundedRectPath(x, y, w, h, r float32) *vector.Path {
	p := &vector.Path{}
	// Top edge starts after the top-left corner.
	p.MoveTo(x+r, y)
	p.LineTo(x+w-r, y)
	p.ArcTo(x+w, y, x+w, y+r, r) // top-right corner
	p.LineTo(x+w, y+h-r)
	p.ArcTo(x+w, y+h, x+w-r, y+h, r) // bottom-right
	p.LineTo(x+r, y+h)
	p.ArcTo(x, y+h, x, y+h-r, r) // bottom-left
	p.LineTo(x, y+r)
	p.ArcTo(x, y, x+r, y, r) // top-left
	p.Close()
	return p
}

// drawCenteredText is the temporary text helper used by the
// placeholder card. Real typography lands with text/v2 in the next
// stage; this debug version draws the string with Ebiten's default
// proportional font.
func (a *App) drawCenteredText(dst *ebiten.Image, x, y, w, h int, s string, c color.NRGBA) {
	// We deliberately defer typography to the next commit; for now
	// the card carries no text — the placeholder above is enough to
	// confirm the gradient + card path runs end-to-end. A no-op
	// keeps this function callable from the boot smoke without
	// dragging the font subsystem in too early.
	_ = s
	_ = c
	_ = dst
	_ = x
	_ = y
	_ = w
	_ = h
}

func lerp(a, b uint8, t float64) uint8 {
	v := float64(a)*(1-t) + float64(b)*t
	if v < 0 {
		v = 0
	} else if v > 255 {
		v = 255
	}
	return uint8(v)
}
