package uiebt

import (
	"image"

	"github.com/hajimehoshi/ebiten/v2"
)

// drawHeader paints the title strip across the top of the window.
// Layout: tabs on the left, title centred, status pill on the
// right. status is passed in (not read off a.status) so the Draw
// goroutine works on the same snapshot the rest of the frame uses
// — avoids a race against SetStatus from the binding goroutine.
func (a *App) drawHeader(dst *ebiten.Image, r image.Rectangle, status SessionStatus) {
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

	pillW, pillH := 180, 28
	pillX := r.Max.X - pillW - SpaceL
	pillY := r.Min.Y + (r.Dy()-pillH)/2
	pillRect := image.Rect(pillX, pillY, pillX+pillW, pillY+pillH)
	drawRoundRect(dst, pillRect, RadiusPill, a.theme.Card)
	drawRoundRectBorder(dst, pillRect, RadiusPill, 1, a.theme.CardBorder)

	dotColor := a.theme.AccentGreen
	dotLabel := "Соединение активно"
	switch status {
	case statusWaiting:
		dotColor = a.theme.AccentAmber
		dotLabel = "Ожидание моделей"
	case statusStopped:
		dotColor = a.theme.TextMuted
		dotLabel = "Остановлено"
	case statusError:
		dotColor = a.theme.AccentRed
		dotLabel = "Ошибка"
	}
	dotR := float32(5)
	drawFilledCircle(dst, float32(pillX)+12, float32(pillY+pillH/2), dotR, dotColor)
	drawText(dst, dotLabel, a.fonts.Caption,
		pillX+24, pillY+pillH/2+5, a.theme.TextSecondary)
}
