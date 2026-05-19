package uiebt

import (
	"context"
	"image"
	"image/color"
	"math"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
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

	// currentTab drives top-level view switching. Mutated only
	// through setTab so the change happens under a.mu.
	currentTab Tab

	// logOpen toggles the terminal-style overlay anchored to the
	// bottom of the window. Flipped by the bottom-bar Logs button.
	logOpen bool

	// logScroll is the wheel-driven offset into the log buffer
	// measured in *lines from the bottom*. 0 == newest, positive
	// scrolls back into history. Clamped in drawLogOverlay against
	// the visible-line count.
	logScroll int

	// recording drives the mic button visual + actual session
	// pause/resume. Flipped by the bottom-bar mic click. Starts at
	// true because the cmd layer auto-starts the session on boot.
	recording bool

	// micHandler is invoked (with the new desired state) every
	// time the user clicks the mic button. The cmd layer wires
	// this to Session.Pause/Resume so the UI toggle actually
	// controls capture — without it the button is purely visual.
	micHandler func(on bool)

	// logRect holds the last drawn log-overlay rectangle so the
	// wheel handler in Update can decide whether the cursor is
	// over it. Updated every Draw when logOpen is true.
	logRect image.Rectangle

	// hits is the per-frame click target list. Rebuilt every Draw,
	// consumed by the next Update. See tabs.go for the
	// register/dispatch helpers.
	hits []hitRect

	// catalog + installer state for the Models tab. Populated when
	// the cmd layer calls SetCatalog. Kept here (not in a separate
	// store) so Draw/Update have lock-free access via copy.
	catalog          []ModelEntry
	modelInstalled   map[string]bool
	modelProgress    map[string]ModelProgressSnapshot
	modelInstaller   ModelInstaller
	installerContext context.Context

	// log sink for the in-window terminal pane. Lazily populated by
	// AttachLogSink so apps that do not wire it stay log-free.
	logSink LogSink

	// settings drives the Settings tab. The committed snapshot is
	// what was last saved to disk; the draft accumulates user edits
	// until Save fires.
	settings      SettingsSnapshot
	settingsDraft SettingsSnapshot
	settingsSave  SettingsSaver
	settingsDirty bool
	settingsMsg   string

	// quit is flipped by RequestQuit (Ctrl+C / signal). The Update
	// loop checks it on every tick and returns ebiten.Termination,
	// which is how Ebiten exits cleanly.
	quit atomic.Bool
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
	text = sanitiseGlyphs(text)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.transcript = append(a.transcript, Card{Text: text, Timestamp: ts, Active: true})
	if n := len(a.transcript); n >= 2 {
		a.transcript[n-2].Active = false
	}
}

// AppendTranslation pushes one MT line to the right pane.
func (a *App) AppendTranslation(text, ts string) {
	text = sanitiseGlyphs(text)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.translation = append(a.translation, Card{Text: text, Timestamp: ts, Active: true})
	if n := len(a.translation); n >= 2 {
		a.translation[n-2].Active = false
	}
}

// sanitiseGlyphs replaces Unicode punctuation that the embedded
// goregular font does not have glyphs for. Without this MT output
// containing smart quotes / em-dashes / ellipsis renders as the
// tofu box "□" inside otherwise-fine text.
func sanitiseGlyphs(s string) string {
	if s == "" {
		return s
	}
	r := s
	r = strings.ReplaceAll(r,"‘", "'")  // ‘
	r = strings.ReplaceAll(r,"’", "'")  // ’
	r = strings.ReplaceAll(r,"‚", "'")  // ‚
	r = strings.ReplaceAll(r,"“", "\"") // “
	r = strings.ReplaceAll(r,"”", "\"") // ”
	r = strings.ReplaceAll(r,"„", "\"") // „
	r = strings.ReplaceAll(r,"–", "-")  // –
	r = strings.ReplaceAll(r,"—", "--") // —
	r = strings.ReplaceAll(r,"…", "...") // …
	r = strings.ReplaceAll(r," ", " ")  // NBSP
	return r
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
//
// Returns ebiten.Termination when RequestQuit has been called so the
// Ebiten loop exits cleanly on Ctrl+C.
func (a *App) Update() error {
	if a.quit.Load() {
		return ebiten.Termination
	}
	const dt = 1.0 / 60.0
	a.sphere.Tick(dt)
	if !a.sphereHasAudio() {
		t := a.sphereTime()
		a.sphere.setAudioInternal(idleAudio(t))
	}
	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		x, y := ebiten.CursorPosition()
		a.dispatchClick(x, y)
	}
	if _, wy := ebiten.Wheel(); wy != 0 {
		cx, cy := ebiten.CursorPosition()
		a.handleWheel(cx, cy, wy)
	}
	return nil
}

// handleWheel routes mouse-wheel events to the widget under the
// cursor. Currently only the log overlay consumes them; other
// scrollable widgets can register the same way.
func (a *App) handleWheel(cx, cy int, wy float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.logOpen {
		return
	}
	r := a.logRect
	if cx < r.Min.X || cx >= r.Max.X || cy < r.Min.Y || cy >= r.Max.Y {
		return
	}
	// Positive wheel-y = scroll up (further into history).
	step := int(wy)
	if step == 0 {
		if wy > 0 {
			step = 1
		} else {
			step = -1
		}
	}
	a.logScroll += step
	if a.logScroll < 0 {
		a.logScroll = 0
	}
}

// RequestQuit signals the Update loop to exit the Ebiten game on
// its next tick. Safe to call from any goroutine.
func (a *App) RequestQuit() {
	a.quit.Store(true)
}

// idleAudio synthesises a calm breathing wave so the sphere is
// always visibly alive — useful before the audio bindings land
// and during long pauses between user utterances.
func idleAudio(t float64) SphereAudio {
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

	a.resetHits()

	a.mu.RLock()
	transcript := append([]Card(nil), a.transcript...)
	translation := append([]Card(nil), a.translation...)
	srcCode, srcLabel := a.sourceCode, a.sourceLang
	tgtCode, tgtLabel := a.targetCode, a.targetLang
	status := a.status
	tab := a.currentTab
	logOpen := a.logOpen
	a.mu.RUnlock()

	r := LayoutFor(bounds.Dx(), bounds.Dy())
	a.drawHeader(screen, r.Header, status)

	switch tab {
	case TabMain:
		a.drawPane(screen, r.Left, PaneHeader{Flag: srcCode, Label: srcLabel}, transcript)
		a.drawPane(screen, r.Right, PaneHeader{Flag: tgtCode, Label: tgtLabel}, translation)
		a.drawCenter(screen, r.Center)
	case TabModels:
		body := image.Rect(r.Left.Min.X, r.Left.Min.Y, r.Right.Max.X, r.Left.Max.Y)
		a.drawModelsPane(screen, body)
	case TabSettings:
		body := image.Rect(r.Left.Min.X, r.Left.Min.Y, r.Right.Max.X, r.Left.Max.Y)
		a.drawSettingsPane(screen, body)
	}

	a.drawBottom(screen, r.Bottom)
	a.drawStatus(screen, r.Status)

	if logOpen {
		a.drawLogOverlay(screen, r.Bottom)
	}
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

// Run sets up the window and runs the Ebiten event loop on a
// freshly-constructed App. Convenience entry-point for tools that
// do not need to bind external state to the UI (e.g. design demos).
//
// Production binaries should call RunApp instead — they hold the
// same *App reference both their binding goroutine pushes events
// to and the Ebiten loop renders from. Calling Run() in that
// scenario was the bug that made transcripts vanish into an
// orphan App while the rendered App stayed empty.
func Run() error {
	return RunApp(context.Background(), NewApp())
}

// RunApp opens the window and enters the Ebiten event loop with
// the supplied App. Blocks until the user closes the window OR
// ctx is cancelled — a cancellation flips the App's quit flag and
// the Update loop returns ebiten.Termination on its next tick.
func RunApp(ctx context.Context, app *App) error {
	if ctx != nil {
		go func() {
			<-ctx.Done()
			app.RequestQuit()
		}()
	}
	ebiten.SetWindowTitle("Realtime Speech Translator")
	ebiten.SetWindowSize(WindowWidth, WindowHeight)
	ebiten.SetWindowResizable(true)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetTPS(60)
	if err := ebiten.RunGame(app); err != nil {
		return err
	}
	return nil
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
	micR := float32(34)
	cx := float32(r.Min.X + r.Dx()/2)
	t := float32(a.sphereTime())

	a.drawMicButton(dst, cx, cy, micR, t)

	// Hint text centred under the mic.
	drawTextCentered(dst, a.hint, a.fonts.Caption,
		int(cx), r.Max.Y-SpaceL, a.theme.TextMuted)

	// Logs toggle to the right of the mic. The other placeholder
	// circles (Микрофон/Динамик/Текст/Настройки) were removed —
	// the tab bar already covers Settings, and per-source mic
	// device routing will come back as a proper dropdown.
	logBtnR := float32(22) * 0.9
	a.drawLogsButton(dst, cx+micR*2.3, cy, logBtnR)
}

// drawMicButton paints the big start / stop button at the bottom
// of the Main tab. State drives colour + glyph:
//
//   - idle (not recording): cyan-ringed blue circle, mic glyph
//   - recording: red circle, white stop square, pulsing red halo
//
// Click toggles a.recording; future work wires this to actual
// session start/stop. For now the button is purely visual feedback
// so the user can tell the click landed.
func (a *App) drawMicButton(dst *ebiten.Image, cx, cy, micR, time float32) {
	a.mu.RLock()
	rec := a.recording
	a.mu.RUnlock()

	if rec {
		// Pulsing red halo — radius oscillates 1.25× → 1.55× at
		// ~1.4 Hz so motion reads as "live recording".
		pulse := 1.4 + 0.15*float32(math.Sin(float64(time)*8.8))
		drawFilledCircle(dst, cx, cy, micR*pulse,
			color.NRGBA{R: 0xEF, G: 0x65, B: 0x6A, A: 0x30})
		drawFilledCircle(dst, cx, cy, micR*1.18,
			color.NRGBA{R: 0xEF, G: 0x65, B: 0x6A, A: 0x55})
		drawFilledCircle(dst, cx, cy, micR, a.theme.AccentRed)
		drawCircleBorder(dst, cx, cy, micR, 2,
			color.NRGBA{R: 0xFF, G: 0xAE, B: 0xAE, A: 0xFF})
		// Stop square (white).
		sq := micR * 0.42
		sqRect := image.Rect(
			int(cx-sq/2), int(cy-sq/2),
			int(cx+sq/2), int(cy+sq/2))
		drawRoundRect(dst, sqRect, 4,
			color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF})
	} else {
		// Idle: blue with cyan ring + white mic glyph + soft glow.
		glow := 1.30 + 0.05*float32(math.Sin(float64(time)*1.4))
		drawFilledCircle(dst, cx, cy, micR*glow,
			color.NRGBA{R: 0x4C, G: 0x76, B: 0xFF, A: 0x22})
		drawFilledCircle(dst, cx, cy, micR, a.theme.AccentBlue)
		drawCircleBorder(dst, cx, cy, micR, 2, a.theme.AccentCyan)
		drawMicGlyph(dst, cx, cy, micR*0.55,
			color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF})
	}

	hit := image.Rect(int(cx-micR), int(cy-micR),
		int(cx+micR), int(cy+micR))
	a.registerHit(hit, a.ToggleRecording)
}

// ToggleRecording flips the recording flag, rotates the hint
// microcopy, and fires the cmd-layer mic handler so the actual
// capture device starts / stops in sync with the visual.
//
// When stopping, also resets the sphere's external-audio flag so
// the idle wave reclaims control and the halo breathes back down
// — otherwise the last loud RMS value sticks forever via the EMA.
func (a *App) ToggleRecording() {
	a.mu.Lock()
	a.recording = !a.recording
	state := a.recording
	if a.recording {
		a.hint = "Запись идёт — нажмите чтобы остановить"
	} else {
		a.hint = "Нажмите чтобы начать запись"
	}
	h := a.micHandler
	a.mu.Unlock()
	if !state {
		a.sphere.ResetExternal()
	}
	if h != nil {
		h(state)
	}
}

// SetMicHandler registers the cmd-layer callback wired to mic
// click events. Pass nil to detach.
func (a *App) SetMicHandler(h func(on bool)) {
	a.mu.Lock()
	a.micHandler = h
	a.mu.Unlock()
}

// SetRecording lets the cmd layer publish the actual capture
// state back to the UI (e.g. when the session auto-starts on
// boot, or after a Resume that we didn't trigger from the click).
func (a *App) SetRecording(on bool) {
	a.mu.Lock()
	a.recording = on
	if on {
		a.hint = "Запись идёт — нажмите чтобы остановить"
	} else {
		a.hint = "Нажмите чтобы начать запись"
	}
	a.mu.Unlock()
}

// drawLogsButton paints the bottom-bar toggle that opens / closes
// the terminal log overlay. Highlighted while the overlay is open.
func (a *App) drawLogsButton(dst *ebiten.Image, cx, cy, r float32) {
	a.mu.RLock()
	open := a.logOpen
	a.mu.RUnlock()
	fill := a.theme.Card
	border := a.theme.CardBorder
	if open {
		fill = a.theme.AccentBlue
		border = a.theme.AccentCyan
	}
	drawFilledCircle(dst, cx, cy, r, fill)
	drawCircleBorder(dst, cx, cy, r, 1, border)
	// ">_" glyph using the caption font.
	glyph := ">_"
	gw, _ := measureText(glyph, a.fonts.Caption)
	drawText(dst, glyph, a.fonts.Caption,
		int(cx)-int(gw)/2, int(cy)+5, a.theme.TextPrimary)
	drawText(dst, "Логи", a.fonts.Caption,
		int(cx)-12, int(cy)+38, a.theme.TextMuted)
	hit := image.Rect(int(cx-r), int(cy-r), int(cx+r), int(cy+r))
	a.registerHit(hit, a.ToggleLog)
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
