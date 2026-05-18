package uiebt

import (
	"image"
	"image/color"
	"math"
	"sync"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// mathSin is a single-call indirection so the idle helpers stay
// import-free at the call site.
func mathSin(x float64) float64 { return math.Sin(x) }

// Window size matching the reference mock. Layout is responsive
// from here on; this only sets the initial window dimension.
const (
	WindowWidth  = 1280
	WindowHeight = 800
)

// SessionStatus enumerates the values the header pill displays.
// Exported so the cmd layer can drive it via SetStatus without
// going through a string contract.
type SessionStatus int

const (
	StatusRunning SessionStatus = iota
	StatusWaiting
	StatusStopped
	StatusError
)

// Backwards-compatible private aliases used by the internal code
// in this package.
const (
	statusRunning = StatusRunning
	statusWaiting = StatusWaiting
	statusStopped = StatusStopped
	statusError   = StatusError
)

type sessionStatus = SessionStatus

// App is the top-level Ebiten Game. State is kept under a single
// mutex because the bindings push new transcript / translation
// lines and audio buckets from background goroutines.
type App struct {
	theme  Theme
	fonts  Fonts
	bg     *backgroundCache
	sphere *Sphere

	mu          sync.RWMutex
	status      sessionStatus
	transcript  []Card
	translation []Card
	sourceLang  string // "Русский"
	targetLang  string // "English"
	sourceCode  string // "ru"
	targetCode  string // "en"
	hint        string // bottom-bar microcopy
}

// backgroundCache holds the per-size gradient image so we only
// repaint it on resize, not every frame.
type backgroundCache struct {
	w, h  int
	image *ebiten.Image
}

// NewApp constructs a default-theme App. Call Run to enter the
// Ebiten loop.
func NewApp() *App {
	return &App{
		theme:      DefaultTheme(),
		fonts:      MustLoadFonts(),
		sphere:     NewSphere(),
		status:     statusWaiting,
		sourceLang: "Русский",
		targetLang: "English",
		sourceCode: "ru",
		targetCode: "en",
		hint:       "Нажмите ⌘ + K для быстрого старта",
	}
}

// SetAudio forwards the latest audio snapshot to the sphere
// shader. Safe for concurrent calls.
func (a *App) SetAudio(s SphereAudio) {
	a.sphere.SetAudio(s)
}

// SetStatus updates the header pill from the binding goroutine.
func (a *App) SetStatus(s SessionStatus) {
	a.mu.Lock()
	a.status = s
	a.mu.Unlock()
}

// AppendTranscript pushes one finalised STT line to the left pane.
func (a *App) AppendTranscript(text, ts string) {
	a.mu.Lock()
	a.transcript = append(a.transcript, Card{Text: text, Timestamp: ts, Active: true})
	// dim the previous active card.
	if n := len(a.transcript); n >= 2 {
		a.transcript[n-2].Active = false
	}
	a.mu.Unlock()
}

// AppendTranslation pushes one MT line to the right pane.
func (a *App) AppendTranslation(text, ts string) {
	a.mu.Lock()
	a.translation = append(a.translation, Card{Text: text, Timestamp: ts, Active: true})
	if n := len(a.translation); n >= 2 {
		a.translation[n-2].Active = false
	}
	a.mu.Unlock()
}

// SetLanguages updates the pane headers + flag colours.
func (a *App) SetLanguages(srcCode, srcLabel, tgtCode, tgtLabel string) {
	a.mu.Lock()
	a.sourceCode = srcCode
	a.sourceLang = srcLabel
	a.targetCode = tgtCode
	a.targetLang = tgtLabel
	a.mu.Unlock()
}

// Update advances logic. We tick the sphere clock and feed it a
// slow idle wave when no real audio is bound — this is what makes
// the sphere visibly breathe on the splash / waiting-for-models
// screen. Real audio buckets from cmd/translator-ui will overwrite
// the idle values whenever they arrive.
func (a *App) Update() error {
	const dt = 1.0 / 60.0
	a.sphere.Tick(dt)
	if !a.sphereHasAudio() {
		t := a.sphereTime()
		a.sphere.setAudioInternal(idleAudio(t))
	}
	return nil
}

// idleAudio synthesises a calm breathing wave so the sphere is
// always visibly alive — useful before the audio bindings land
// and during long pauses between user utterances.
func idleAudio(t float64) SphereAudio {
	// Three sine waves at different frequencies keep the
	// modulation from looking metronomic.
	bass := 0.18 + 0.12*sin01(t*0.6)
	mid := 0.10 + 0.10*sin01(t*0.45+1.2)
	treble := 0.08 + 0.07*sin01(t*1.1+2.7)
	rms := 0.12 + 0.08*sin01(t*0.35+0.9)
	return SphereAudio{
		Bass:   float32(bass),
		Mid:    float32(mid),
		Treble: float32(treble),
		Rms:    float32(rms),
	}
}

// sin01 returns sin(x) remapped to [0, 1].
func sin01(x float64) float64 {
	return 0.5 + 0.5*sinFast(x)
}

// sinFast uses math.Sin; the wrapper exists so a future SIMD path
// can swap it without touching the call sites.
func sinFast(x float64) float64 {
	return mathSin(x)
}

// sphereHasAudio returns true once SetAudio has been called from
// outside with any non-zero value. Until then the Update loop
// drives the sphere off the idle wave.
func (a *App) sphereHasAudio() bool {
	a.sphere.mu.RLock()
	defer a.sphere.mu.RUnlock()
	return a.sphere.externalDriven
}

// sphereTime exposes the sphere clock so the idle wave runs on the
// same reference as the shader's Time uniform.
func (a *App) sphereTime() float64 {
	a.sphere.mu.RLock()
	defer a.sphere.mu.RUnlock()
	return a.sphere.time
}

// Draw paints one frame.
func (a *App) Draw(screen *ebiten.Image) {
	bounds := screen.Bounds()
	a.ensureBackground(bounds.Dx(), bounds.Dy())
	screen.DrawImage(a.bg.image, nil)

	a.mu.RLock()
	transcript := append([]Card(nil), a.transcript...)
	translation := append([]Card(nil), a.translation...)
	srcCode, srcLabel := a.sourceCode, a.sourceLang
	tgtCode, tgtLabel := a.targetCode, a.targetLang
	a.mu.RUnlock()

	r := LayoutFor(bounds.Dx(), bounds.Dy())
	a.drawHeader(screen, r.Header)
	a.drawPane(screen, r.Left, PaneHeader{Flag: srcCode, Label: srcLabel}, transcript)
	a.drawPane(screen, r.Right, PaneHeader{Flag: tgtCode, Label: tgtLabel}, translation)
	a.drawCenter(screen, r.Center)
	a.drawBottom(screen, r.Bottom)
	a.drawStatus(screen, r.Status)
}

// Layout reports the logical-pixel dimensions Ebiten should render
// into. We pin to the host's outer dims so resize is honoured.
func (a *App) Layout(outerWidth, outerHeight int) (int, int) {
	if outerWidth < 800 {
		outerWidth = 800
	}
	if outerHeight < 600 {
		outerHeight = 600
	}
	return outerWidth, outerHeight
}

// Run sets up the window and enters the Ebiten event loop.
func Run() error {
	ebiten.SetWindowTitle("Realtime Speech Translator")
	ebiten.SetWindowSize(WindowWidth, WindowHeight)
	ebiten.SetWindowResizable(true)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetTPS(60)
	return ebiten.RunGame(NewApp())
}

// ensureBackground rebuilds the cached gradient image when the
// window dimensions change. One-time cost per resize.
func (a *App) ensureBackground(w, h int) {
	if a.bg != nil && a.bg.w == w && a.bg.h == h {
		return
	}
	a.bg = &backgroundCache{w: w, h: h, image: ebiten.NewImage(w, h)}

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
		vector.DrawFilledRect(a.bg.image, 0, float32(y), float32(w), 1, c, false)
	}
}

// roundedRectPath builds an outlined rounded-rectangle path with
// corner radius r, starting at the top-left corner.
func roundedRectPath(x, y, w, h, r float32) *vector.Path {
	p := &vector.Path{}
	p.MoveTo(x+r, y)
	p.LineTo(x+w-r, y)
	p.ArcTo(x+w, y, x+w, y+r, r)
	p.LineTo(x+w, y+h-r)
	p.ArcTo(x+w, y+h, x+w-r, y+h, r)
	p.LineTo(x+r, y+h)
	p.ArcTo(x, y+h, x, y+h-r, r)
	p.LineTo(x, y+r)
	p.ArcTo(x, y, x+r, y, r)
	p.Close()
	return p
}

// drawCenter paints the audio-reactive sphere into the centre
// pane via the Kage shader. The shader is the visual anchor of
// the Main tab; everything else is intentionally calmer so the
// motion reads.
func (a *App) drawCenter(dst *ebiten.Image, r image.Rectangle) {
	a.sphere.Draw(dst, r)
}

// drawBottom paints the bottom control strip — large mic button in
// the center, mic / speaker indicators on the left, text / settings
// on the right. Wired to actions in a follow-up.
func (a *App) drawBottom(dst *ebiten.Image, r image.Rectangle) {
	cy := float32(r.Min.Y + r.Dy()/2)

	// Centre stack: small "audio levels" + big mic + small "translate"
	micR := float32(34)
	cx := float32(r.Min.X + r.Dx()/2)
	// Big mic button.
	drawFilledCircle(dst, cx, cy, micR*1.35,
		color.NRGBA{R: 0x4C, G: 0x76, B: 0xFF, A: 0x22})
	drawFilledCircle(dst, cx, cy, micR, a.theme.AccentBlue)
	drawCircleBorder(dst, cx, cy, micR, 2, a.theme.AccentCyan)
	// Side mini buttons.
	sideR := float32(22)
	drawFilledCircle(dst, cx-90, cy, sideR, a.theme.Card)
	drawCircleBorder(dst, cx-90, cy, sideR, 1, a.theme.CardBorder)
	drawFilledCircle(dst, cx+90, cy, sideR, a.theme.Card)
	drawCircleBorder(dst, cx+90, cy, sideR, 1, a.theme.CardBorder)

	// Hint text under the mic.
	hw, _ := measureText(a.hint, a.fonts.Caption)
	drawText(dst, a.hint, a.fonts.Caption,
		int(cx)-int(hw)/2, r.Max.Y-SpaceL, a.theme.TextMuted)

	// Left side: mic / speaker icons (placeholder dots + labels).
	leftX := r.Min.X + SpaceXL*2
	drawFilledCircle(dst, float32(leftX), cy, sideR*0.7, a.theme.Card)
	drawCircleBorder(dst, float32(leftX), cy, sideR*0.7, 1, a.theme.CardBorder)
	drawText(dst, "Микрофон", a.fonts.Caption, leftX-30, int(cy)+38, a.theme.TextMuted)

	drawFilledCircle(dst, float32(leftX)+70, cy, sideR*0.7, a.theme.Card)
	drawCircleBorder(dst, float32(leftX)+70, cy, sideR*0.7, 1, a.theme.CardBorder)
	drawText(dst, "Динамик", a.fonts.Caption, leftX+40, int(cy)+38, a.theme.TextMuted)

	// Right side: text / settings icons.
	rightX := r.Max.X - SpaceXL*2
	drawFilledCircle(dst, float32(rightX)-70, cy, sideR*0.7, a.theme.Card)
	drawCircleBorder(dst, float32(rightX)-70, cy, sideR*0.7, 1, a.theme.CardBorder)
	drawText(dst, "Текст", a.fonts.Caption, rightX-90, int(cy)+38, a.theme.TextMuted)

	drawFilledCircle(dst, float32(rightX), cy, sideR*0.7, a.theme.Card)
	drawCircleBorder(dst, float32(rightX), cy, sideR*0.7, 1, a.theme.CardBorder)
	drawText(dst, "Настройки", a.fonts.Caption, rightX-26, int(cy)+38, a.theme.TextMuted)
}

// drawStatus paints the bottom-most strip (mock shows quality
// dropdown on the left, end-call pill in the middle, timer + signal
// on the right). Static for now.
func (a *App) drawStatus(dst *ebiten.Image, r image.Rectangle) {
	// Subtle separator line at the top edge of the strip.
	vector.DrawFilledRect(dst,
		float32(r.Min.X+SpaceL), float32(r.Min.Y),
		float32(r.Dx()-SpaceL*2), 1,
		color.NRGBA{R: 0x20, G: 0x24, B: 0x3A, A: 0xFF}, false)

	cy := float32(r.Min.Y + r.Dy()/2)
	drawText(dst, "Качество перевода: Высокое",
		a.fonts.Caption,
		r.Min.X+SpaceL*2, int(cy)+5,
		a.theme.TextMuted)
	drawText(dst, "00:00:00",
		a.fonts.Caption,
		r.Max.X-SpaceL*2-60, int(cy)+5,
		a.theme.TextMuted)
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
