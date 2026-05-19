package uiebt

import (
	"image"

	"github.com/hajimehoshi/ebiten/v2"
)

// drawHeader paints the title strip across the top of the window.
// Layout: tabs on the left, title centred. The "соединение
// активно" pill was removed — the mic button colour already
// communicates capture state, the pill was redundant noise.
//
// status is still part of the signature so the App's Draw can keep
// passing the snapshot it took under a.mu; the parameter is unused
// here on purpose and reserved for re-introducing the indicator
// (or surfacing it on the Models / Settings tabs) without another
// signature churn.
func (a *App) drawHeader(dst *ebiten.Image, r image.Rectangle, status SessionStatus) {
	_ = status
	const tabsH = 32
	tabsRect := image.Rect(
		r.Min.X+SpaceL,
		r.Min.Y+(r.Dy()-tabsH)/2,
		r.Min.X+SpaceL+460,
		r.Min.Y+(r.Dy()-tabsH)/2+tabsH,
	)
	a.drawTabs(dst, tabsRect)

	const title = "Realtime Speech Translator"
	tw, th := measureText(title, a.fonts.Title)
	x := r.Min.X + (r.Dx()-int(tw))/2
	y := r.Min.Y + (r.Dy()-int(th))/2 + int(th*0.8)
	drawText(dst, title, a.fonts.Title, x, y, a.theme.TextPrimary)
}
