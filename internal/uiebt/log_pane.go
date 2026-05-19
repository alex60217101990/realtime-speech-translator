package uiebt

import (
	"fmt"
	"image"
	"image/color"
	"log/slog"
	"sync"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// LogSink is the minimal contract the UI needs to render its
// terminal-style log overlay. internal/logging satisfies it via
// AttachLogSink; tests can pass an in-memory fake.
type LogSink interface {
	// Snapshot returns a copy of the current log buffer in
	// chronological order.
	Snapshot() []LogRecord
	// Subscribe registers a callback fired for every new line. The
	// callback runs on the producer's goroutine — keep it cheap.
	Subscribe(fn func(LogRecord)) (unsubscribe func())
}

// LogRecord mirrors logging.LogLine but stays inside this package
// so uiebt does not import logging directly — the cmd layer wires
// them up with a tiny adapter.
type LogRecord struct {
	When    time.Time
	Level   slog.Level
	Message string
	Attrs   string
}

// memLogSink is a self-contained sink used both as the local
// mirror for AttachLogSink and as a stub when no external logger
// is wired. Bounded ring so it never grows.
type memLogSink struct {
	mu  sync.Mutex
	buf []LogRecord
	cap int
}

func newMemLogSink(cap int) *memLogSink {
	if cap <= 0 {
		cap = 300
	}
	return &memLogSink{cap: cap, buf: make([]LogRecord, 0, cap)}
}

func (m *memLogSink) push(r LogRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.buf) == cap(m.buf) {
		copy(m.buf, m.buf[1:])
		m.buf = m.buf[:len(m.buf)-1]
	}
	m.buf = append(m.buf, r)
}

func (m *memLogSink) Snapshot() []LogRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]LogRecord, len(m.buf))
	copy(out, m.buf)
	return out
}

func (m *memLogSink) Subscribe(fn func(LogRecord)) func() {
	// memLogSink does not broadcast — Subscribe is satisfied for
	// the interface, but the overlay always re-Snapshots on every
	// Draw so live updates work without a subscriber wiring.
	_ = fn
	return func() {}
}

// AttachLogSink wires an external log source (typically the one
// from internal/logging) so the overlay shows real records. May be
// called at most once after NewApp; safe before RunApp.
func (a *App) AttachLogSink(s LogSink) {
	a.mu.Lock()
	a.logSink = s
	a.mu.Unlock()
}

// PushLog lets the cmd layer feed records when it does not want
// the full Subscribe path. Useful for debugging without slog.
func (a *App) PushLog(r LogRecord) {
	a.mu.Lock()
	if a.logSink == nil {
		a.logSink = newMemLogSink(300)
	}
	if m, ok := a.logSink.(*memLogSink); ok {
		m.push(r)
	}
	a.mu.Unlock()
}

// ToggleLog flips the log overlay open/closed state. Wired to the
// Logs button in the bottom strip.
func (a *App) ToggleLog() {
	a.mu.Lock()
	a.logOpen = !a.logOpen
	a.mu.Unlock()
}

// drawLogOverlay paints a translucent terminal-style log strip
// anchored above the bottom control strip when a.logOpen is true.
// Newest lines render at the bottom (terminal convention). The
// strip steals 38 % of the window height and overlays whatever
// content is behind it — we deliberately do not relayout.
func (a *App) drawLogOverlay(dst *ebiten.Image, bottomStrip image.Rectangle) {
	w := bottomStrip.Dx()
	h := int(float64(dst.Bounds().Dy()) * 0.38)
	if h < 180 {
		h = 180
	}
	y0 := bottomStrip.Min.Y - h - SpaceM
	if y0 < SpaceL*4 {
		y0 = SpaceL * 4
	}
	rect := image.Rect(bottomStrip.Min.X+SpaceL, y0,
		bottomStrip.Min.X+w-SpaceL, y0+h)

	// Cache rect for the wheel handler in Update.
	a.mu.Lock()
	a.logRect = rect
	a.mu.Unlock()

	// Translucent backdrop.
	scrim := color.NRGBA{R: 0x05, G: 0x06, B: 0x12, A: 0xE6}
	drawRoundRect(dst, rect, RadiusCard, scrim)
	drawRoundRectBorder(dst, rect, RadiusCard, 1, a.theme.CardBorder)

	// Title bar.
	titleH := 28
	titleRect := image.Rect(rect.Min.X, rect.Min.Y, rect.Max.X, rect.Min.Y+titleH)
	vector.DrawFilledRect(dst,
		float32(titleRect.Min.X), float32(titleRect.Max.Y-1),
		float32(titleRect.Dx()), 1,
		a.theme.CardBorder, false)
	drawText(dst, "Логи системы", a.fonts.Caption,
		titleRect.Min.X+SpaceL, titleRect.Min.Y+18, a.theme.TextSecondary)

	// Close button on the right of the title bar. Vector "×" so
	// we do not rely on a glyph the embedded font lacks.
	closeR := image.Rect(titleRect.Max.X-titleH-SpaceS,
		titleRect.Min.Y+SpaceXS,
		titleRect.Max.X-SpaceS,
		titleRect.Max.Y-SpaceXS)
	drawRoundRect(dst, closeR, RadiusButton, a.theme.Card)
	drawRoundRectBorder(dst, closeR, RadiusButton, 1, a.theme.CardBorder)
	drawClose(dst,
		float32(closeR.Min.X+closeR.Dx()/2),
		float32(closeR.Min.Y+closeR.Dy()/2),
		8, 1.6, a.theme.TextSecondary)
	a.registerHit(closeR, a.ToggleLog)

	// Body — render newest at the bottom.
	body := image.Rect(rect.Min.X+SpaceL, titleRect.Max.Y+SpaceS,
		rect.Max.X-SpaceL, rect.Max.Y-SpaceS)

	records := a.snapshotLogs()
	if len(records) == 0 {
		drawText(dst, "пока пусто — события появятся здесь",
			a.fonts.Caption,
			body.Min.X, body.Min.Y+18, a.theme.TextMuted)
		return
	}

	_, lh := measureText("Ag", a.fonts.Caption)
	lineH := int(lh)
	if lineH < 14 {
		lineH = 14
	}
	maxLines := body.Dy() / lineH
	if maxLines < 1 {
		maxLines = 1
	}

	// Clamp scroll: 0 == newest at bottom, positive scrolls back.
	a.mu.Lock()
	maxScroll := len(records) - maxLines
	if maxScroll < 0 {
		maxScroll = 0
	}
	if a.logScroll > maxScroll {
		a.logScroll = maxScroll
	}
	scroll := a.logScroll
	a.mu.Unlock()

	// Index of the newest line currently visible at the bottom.
	endIdx := len(records) - 1 - scroll
	if endIdx < 0 {
		endIdx = -1
	}
	y := body.Max.Y - SpaceXS - lineH
	for i := endIdx; i >= 0; i-- {
		rec := records[i]
		drawText(dst, formatLogLine(rec), a.fonts.Caption,
			body.Min.X, y, levelColor(a.theme, rec.Level))
		y -= lineH
		if y < body.Min.Y {
			break
		}
	}

	// Tiny scroll indicator on the right.
	if maxScroll > 0 {
		barH := body.Dy() * maxLines / len(records)
		if barH < 20 {
			barH = 20
		}
		track := float64(body.Dy() - barH)
		pos := float64(maxScroll-scroll) / float64(maxScroll)
		yOff := int(pos * track)
		barRect := image.Rect(body.Max.X-4, body.Min.Y+yOff,
			body.Max.X, body.Min.Y+yOff+barH)
		drawRoundRect(dst, barRect, 2, a.theme.CardBorder)
	}
}

func (a *App) snapshotLogs() []LogRecord {
	a.mu.RLock()
	s := a.logSink
	a.mu.RUnlock()
	if s == nil {
		return nil
	}
	return s.Snapshot()
}

func formatLogLine(r LogRecord) string {
	ts := r.When.Format("15:04:05")
	lvl := levelTag(r.Level)
	if r.Attrs == "" {
		return fmt.Sprintf("%s %s %s", ts, lvl, r.Message)
	}
	return fmt.Sprintf("%s %s %s%s", ts, lvl, r.Message, r.Attrs)
}

func levelTag(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERR "
	case l >= slog.LevelWarn:
		return "WARN"
	case l >= slog.LevelInfo:
		return "INFO"
	default:
		return "DBG "
	}
}

func levelColor(t Theme, l slog.Level) color.NRGBA {
	switch {
	case l >= slog.LevelError:
		return t.AccentRed
	case l >= slog.LevelWarn:
		return t.AccentAmber
	case l >= slog.LevelInfo:
		return t.TextSecondary
	default:
		return t.TextMuted
	}
}
