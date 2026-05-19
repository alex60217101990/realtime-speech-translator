package uiebt

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"sync"

	"github.com/hajimehoshi/ebiten/v2"
)

// ModelEntry is the UI-facing slice of internal/models.Entry. Kept
// here so uiebt does not import internal/models — the cmd layer
// adapts the catalog with three lines of code in SetCatalog.
type ModelEntry struct {
	Key         string // unique id ("vad-silero", "stt-zipformer-en")
	Kind        string // "VAD" / "STT" / "TTS" / "MT"
	DisplayName string
	License     string
	SizeBytes   int64
	// Available signals whether an install is even possible. An
	// entry whose downloadable URL has not been published yet
	// renders with a "URL TBD" badge instead of a working button.
	Available bool
}

// ModelProgressSnapshot is what Install pumps into the App while a
// download is in flight. Done == Total or Error != "" terminates
// the visible progress bar.
type ModelProgressSnapshot struct {
	Done    int64
	Total   int64
	Running bool
	Error   string
}

// ModelInstaller is the side-effecting half: kicks off the
// download for one entry. Implementations should be safe to call
// concurrently for distinct keys. Progress is fed back through
// App.SetModelProgress.
type ModelInstaller interface {
	Install(ctx context.Context, key string)
}

// SetCatalog publishes the catalog the Models tab renders. Safe to
// call repeatedly; replaces the previous list.
func (a *App) SetCatalog(entries []ModelEntry, installed map[string]bool, installer ModelInstaller, ctx context.Context) {
	a.mu.Lock()
	a.catalog = append([]ModelEntry(nil), entries...)
	if installed == nil {
		installed = map[string]bool{}
	}
	a.modelInstalled = installed
	if a.modelProgress == nil {
		a.modelProgress = map[string]ModelProgressSnapshot{}
	}
	a.modelInstaller = installer
	a.installerContext = ctx
	a.mu.Unlock()
}

// SetModelInstalled flips the green-check badge for an entry.
// Called by the cmd layer after a successful Install finishes.
func (a *App) SetModelInstalled(key string, ok bool) {
	a.mu.Lock()
	if a.modelInstalled == nil {
		a.modelInstalled = map[string]bool{}
	}
	a.modelInstalled[key] = ok
	a.mu.Unlock()
}

// SetModelProgress publishes the latest download progress for one
// entry. Pass Running=false (and either Done==Total or non-empty
// Error) to clear the bar.
func (a *App) SetModelProgress(key string, p ModelProgressSnapshot) {
	a.mu.Lock()
	if a.modelProgress == nil {
		a.modelProgress = map[string]ModelProgressSnapshot{}
	}
	a.modelProgress[key] = p
	a.mu.Unlock()
}

// drawModelsPane renders the catalog under the Models tab — one
// card per entry plus a top toolbar with the "Install all missing"
// button. Click targets are registered via registerHit; the actual
// install loop lives in the cmd layer behind the ModelInstaller
// interface.
func (a *App) drawModelsPane(dst *ebiten.Image, r image.Rectangle) {
	a.mu.RLock()
	catalog := append([]ModelEntry(nil), a.catalog...)
	installed := copyBoolMap(a.modelInstalled)
	progress := copyProgressMap(a.modelProgress)
	installer := a.modelInstaller
	ctx := a.installerContext
	a.mu.RUnlock()

	// Toolbar.
	toolH := 48
	tool := image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+toolH)
	drawRoundRect(dst, tool, RadiusCard, a.theme.Card)
	drawRoundRectBorder(dst, tool, RadiusCard, 1, a.theme.CardBorder)

	drawText(dst, "Менеджер моделей", a.fonts.Title,
		tool.Min.X+SpaceL, tool.Min.Y+30, a.theme.TextPrimary)

	missing := countMissing(catalog, installed)
	btnLabel := fmt.Sprintf("Установить недостающие (%d)", missing)
	allInstalled := missing == 0
	if allInstalled {
		btnLabel = "Все компоненты установлены"
	}
	bw, _ := measureText(btnLabel, a.fonts.Body)
	btnW := int(bw) + SpaceXL*2 + 12 // +12 fudge — Cyrillic measure underflows
	btnH := 32
	btnRect := image.Rect(tool.Max.X-SpaceL-btnW,
		tool.Min.Y+(toolH-btnH)/2,
		tool.Max.X-SpaceL,
		tool.Min.Y+(toolH-btnH)/2+btnH)
	var (
		fill   color.NRGBA
		border color.NRGBA
		txtCol color.NRGBA
	)
	if allInstalled {
		// Green-tinted pill so the "ready" state reads at a glance
		// instead of vanishing into the toolbar background.
		fill = color.NRGBA{R: 0x16, G: 0x3A, B: 0x2B, A: 0xFF}
		border = a.theme.AccentGreen
		txtCol = a.theme.AccentGreen
	} else {
		fill = a.theme.AccentBlue
		border = a.theme.AccentCyan
		txtCol = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	}
	drawRoundRect(dst, btnRect, RadiusButton, fill)
	drawRoundRectBorder(dst, btnRect, RadiusButton, 1, border)
	drawTextCentered(dst, btnLabel, a.fonts.Body,
		btnRect.Min.X+btnRect.Dx()/2, btnRect.Min.Y+btnRect.Dy()/2, txtCol)
	if !allInstalled && installer != nil {
		a.registerHit(btnRect, func() {
			installAllMissing(ctx, installer, catalog, installed)
		})
	}

	// Cards. Compact height so all 7 catalog entries fit a 800-px
	// window with room left over.
	cardY := tool.Max.Y + SpaceS
	const cardH = 52
	for _, e := range catalog {
		if cardY+cardH > r.Max.Y {
			break
		}
		cardRect := image.Rect(r.Min.X, cardY, r.Max.X, cardY+cardH)
		a.drawModelCard(dst, cardRect, e,
			installed[e.Key], progress[e.Key], installer, ctx)
		cardY += cardH + SpaceXS
	}
}

func (a *App) drawModelCard(dst *ebiten.Image, r image.Rectangle,
	e ModelEntry, ok bool, p ModelProgressSnapshot,
	installer ModelInstaller, ctx context.Context,
) {
	drawRoundRect(dst, r, RadiusCard, a.theme.Card)
	drawRoundRectBorder(dst, r, RadiusCard, 1, a.theme.CardBorder)

	// Left: status dot.
	dotX := float32(r.Min.X + SpaceL + 8)
	dotY := float32(r.Min.Y + r.Dy()/2)
	dotColor := a.theme.TextMuted
	if ok {
		dotColor = a.theme.AccentGreen
	} else if p.Running {
		dotColor = a.theme.AccentAmber
	}
	drawFilledCircle(dst, dotX, dotY, 6, dotColor)

	textX := r.Min.X + SpaceL + 28
	drawText(dst, e.DisplayName, a.fonts.Body,
		textX, r.Min.Y+6, a.theme.TextPrimary)
	meta := fmt.Sprintf("%s · %s · %s",
		e.Kind, humanBytes(e.SizeBytes), e.License)
	drawText(dst, meta, a.fonts.Caption,
		textX, r.Min.Y+28, a.theme.TextMuted)

	// Progress bar (only while running or after failure).
	if p.Running || p.Error != "" {
		barRect := image.Rect(textX, r.Max.Y-22,
			r.Max.X-SpaceL-160, r.Max.Y-14)
		drawRoundRect(dst, barRect, 4,
			color.NRGBA{R: 0x14, G: 0x18, B: 0x28, A: 0xFF})
		if p.Total > 0 && p.Done > 0 {
			pct := float32(p.Done) / float32(p.Total)
			if pct > 1 {
				pct = 1
			}
			filledW := int(float32(barRect.Dx()) * pct)
			fillRect := image.Rect(barRect.Min.X, barRect.Min.Y,
				barRect.Min.X+filledW, barRect.Max.Y)
			drawRoundRect(dst, fillRect, 4, a.theme.AccentBlue)
		}
		statusTxt := fmt.Sprintf("%s / %s",
			humanBytes(p.Done), humanBytes(p.Total))
		if p.Error != "" {
			statusTxt = "Ошибка: " + p.Error
		}
		drawText(dst, statusTxt, a.fonts.Caption,
			textX, r.Max.Y-26, a.theme.TextMuted)
	}

	// Right: install / installed badge.
	btnLabel := "Установить"
	switch {
	case ok:
		btnLabel = "Готово"
	case p.Running:
		btnLabel = "Скачивание…"
	case !e.Available:
		btnLabel = "URL TBD"
	}
	bw, _ := measureText(btnLabel, a.fonts.Body)
	checkExtra := 0
	if ok {
		checkExtra = 22
	}
	btnW := int(bw) + SpaceXL + checkExtra
	btnH := 30
	btnRect := image.Rect(r.Max.X-SpaceL-btnW,
		r.Min.Y+(r.Dy()-btnH)/2,
		r.Max.X-SpaceL,
		r.Min.Y+(r.Dy()-btnH)/2+btnH)
	var fill color.NRGBA
	var border color.NRGBA
	var txtCol color.NRGBA
	switch {
	case ok:
		fill = color.NRGBA{R: 0x16, G: 0x3A, B: 0x2B, A: 0xFF}
		border = a.theme.AccentGreen
		txtCol = a.theme.AccentGreen
	case p.Running:
		fill = a.theme.Card
		border = a.theme.CardBorder
		txtCol = a.theme.TextSecondary
	case !e.Available:
		fill = a.theme.Card
		border = a.theme.CardBorder
		txtCol = a.theme.TextMuted
	default:
		fill = a.theme.AccentBlue
		border = a.theme.AccentCyan
		txtCol = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	}
	drawRoundRect(dst, btnRect, RadiusButton, fill)
	drawRoundRectBorder(dst, btnRect, RadiusButton, 1, border)
	textCenterX := btnRect.Min.X + btnRect.Dx()/2
	if ok {
		// Vector checkmark to the left of the centred label since
		// the font lacks U+2713.
		drawCheckmark(dst,
			float32(btnRect.Min.X)+14, float32(btnRect.Min.Y+btnRect.Dy()/2),
			14, 2, a.theme.AccentGreen)
		textCenterX = btnRect.Min.X + 16 + btnRect.Dx()/2
	}
	drawTextCentered(dst, btnLabel, a.fonts.Body,
		textCenterX, btnRect.Min.Y+btnRect.Dy()/2, txtCol)
	if !ok && !p.Running && e.Available && installer != nil {
		key := e.Key
		a.registerHit(btnRect, func() {
			installer.Install(ctx, key)
		})
	}
}

// installAllMissing fans out installs sequentially in a single
// background goroutine — concurrent downloads of 5 × 100 MB+
// archives would saturate most user links and the K2-FSA release
// host caps connections per IP.
var installLoopMu sync.Mutex

func installAllMissing(ctx context.Context, installer ModelInstaller,
	catalog []ModelEntry, installed map[string]bool,
) {
	go func() {
		installLoopMu.Lock()
		defer installLoopMu.Unlock()
		for _, e := range catalog {
			if installed[e.Key] || !e.Available {
				continue
			}
			if ctx != nil && ctx.Err() != nil {
				return
			}
			installer.Install(ctx, e.Key)
		}
	}()
}

// countMissing only counts entries that have a real download URL.
// "URL TBD" rows are excluded so the toolbar badge does not say
// "Установить недостающие (1)" when the only missing one is
// uninstallable.
func countMissing(catalog []ModelEntry, installed map[string]bool) int {
	n := 0
	for _, e := range catalog {
		if !installed[e.Key] && e.Available {
			n++
		}
	}
	return n
}

func copyBoolMap(m map[string]bool) map[string]bool {
	if m == nil {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copyProgressMap(m map[string]ModelProgressSnapshot) map[string]ModelProgressSnapshot {
	if m == nil {
		return map[string]ModelProgressSnapshot{}
	}
	out := make(map[string]ModelProgressSnapshot, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func humanBytes(n int64) string {
	const (
		_         = iota
		kib int64 = 1 << (10 * iota)
		mib
		gib
	)
	switch {
	case n >= gib:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gib))
	case n >= mib:
		return fmt.Sprintf("%.0f MB", float64(n)/float64(mib))
	case n >= kib:
		return fmt.Sprintf("%.0f KB", float64(n)/float64(kib))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

