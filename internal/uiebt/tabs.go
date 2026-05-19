package uiebt

import (
	"image"
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
)

// Tab enumerates the top-level views switchable via the header
// segmented control.
type Tab int

const (
	TabMain Tab = iota
	TabModels
	TabSettings
)

func (t Tab) label() string {
	switch t {
	case TabMain:
		return "Главная"
	case TabModels:
		return "Модели"
	case TabSettings:
		return "Настройки"
	}
	return "?"
}

// hitRect is one rectangular interactive region registered by a
// Draw pass and consumed by the next Update tick. Immediate-mode
// click dispatch keeps the widgets stateless — there is no
// retained Button object, only a snapshot per frame.
type hitRect struct {
	rect    image.Rectangle
	onClick func()
}

// registerHit appends a new clickable area to the per-frame hit
// list. Coordinates are in logical pixels (matches CursorPosition).
func (a *App) registerHit(r image.Rectangle, onClick func()) {
	a.hits = append(a.hits, hitRect{rect: r, onClick: onClick})
}

// resetHits clears the hit list at the top of each Draw so stale
// rects from the previous tab/view never receive a click.
func (a *App) resetHits() {
	a.hits = a.hits[:0]
}

// dispatchClick walks the most recent hit-list and fires the
// handler under the cursor. Hits registered later in the Draw
// shadow earlier ones — the loop walks newest-first so overlays
// (e.g. the log drawer) take precedence over background buttons.
func (a *App) dispatchClick(x, y int) {
	for i := len(a.hits) - 1; i >= 0; i-- {
		h := a.hits[i]
		if x >= h.rect.Min.X && x < h.rect.Max.X &&
			y >= h.rect.Min.Y && y < h.rect.Max.Y {
			if h.onClick != nil {
				h.onClick()
			}
			return
		}
	}
}

// drawTabs paints the segmented control at the left side of the
// header and registers a hit rect per segment.
func (a *App) drawTabs(dst *ebiten.Image, r image.Rectangle) {
	const segPadX = 14
	tabs := []Tab{TabMain, TabModels, TabSettings}
	x := r.Min.X
	y := r.Min.Y
	h := r.Dy()
	for _, t := range tabs {
		label := t.label()
		lw, _ := measureText(label, a.fonts.Body)
		segW := int(lw) + segPadX*2
		segRect := image.Rect(x, y, x+segW, y+h)

		fill := a.theme.Card
		border := a.theme.CardBorder
		txt := a.theme.TextSecondary
		if t == a.currentTab {
			fill = a.theme.AccentBlue
			border = a.theme.AccentCyan
			txt = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
		}
		drawRoundRect(dst, segRect, RadiusButton, fill)
		drawRoundRectBorder(dst, segRect, RadiusButton, 1, border)
		drawTextCentered(dst, label, a.fonts.Body,
			x+segW/2, y+h/2, txt)

		// Snapshot loop var so the closure binds correctly.
		target := t
		a.registerHit(segRect, func() { a.setTab(target) })

		x += segW + SpaceS
	}
}

// setTab switches the active tab. Lives on App so the click
// closure inside drawTabs can call it without needing access to
// the mutex from outside the package.
func (a *App) setTab(t Tab) {
	a.mu.Lock()
	a.currentTab = t
	a.mu.Unlock()
}
