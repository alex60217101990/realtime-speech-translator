package uiebt

import (
	"image"

	"github.com/hajimehoshi/ebiten/v2"
)

// drawHeader paints the title strip across the top of the Main tab.
// The reference mock shows a centered title plus a small connection-
// status pill on the right. Source / target language pills sit
// below the header in the side panes' first card.
func (a *App) drawHeader(dst *ebiten.Image, r image.Rectangle) {
	// Centered title.
	const title = "Realtime Speech Translator"
	tw, th := measureText(title, a.fonts.Title)
	x := r.Min.X + (r.Dx()-int(tw))/2
	y := r.Min.Y + (r.Dy()-int(th))/2 + int(th*0.8)
	drawText(dst, title, a.fonts.Title, x, y, a.theme.TextPrimary)

	// Connection / session-status pill in the top-right corner.
	pillW, pillH := 180, 28
	pillX := r.Max.X - pillW - SpaceL
	pillY := r.Min.Y + (r.Dy()-pillH)/2
	pillRect := image.Rect(pillX, pillY, pillX+pillW, pillY+pillH)
	drawRoundRect(dst, pillRect, RadiusPill, a.theme.Card)
	drawRoundRectBorder(dst, pillRect, RadiusPill, 1, a.theme.CardBorder)

	// Status dot — green when session is live, amber when waiting
	// for models, red on error.
	dotColor := a.theme.AccentGreen
	dotLabel := "Соединение активно"
	switch a.status {
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
