package uiebt

import (
	"image"

	"github.com/hajimehoshi/ebiten/v2"
)

// Card is one transcript / translation message bubble in a pane.
type Card struct {
	Text      string // body text
	Timestamp string // "10:24" — right-aligned in the bubble
	Active    bool   // selected / current — gets the cyan outline glow
}

// PaneHeader is the small language-tag row that sits above each
// card stack. Flag is rendered as a coloured dot for now — real
// emoji / SVG flag in a follow-up.
type PaneHeader struct {
	Flag  string // ISO code, e.g. "ru" / "en"
	Label string // "Русский" / "English"
}

// drawPane renders a language header pill on top followed by a
// vertical stack of transcript bubbles inside r. Cards stack
// top-down; bubbles that would run off the bottom are clipped
// (scroll lands in a follow-up).
func (a *App) drawPane(dst *ebiten.Image, r image.Rectangle, head PaneHeader, cards []Card) {
	// Language pill at the top of the column.
	pillH := 56
	pillRect := image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+pillH)
	drawRoundRect(dst, pillRect, RadiusCard, a.theme.Card)
	drawRoundRectBorder(dst, pillRect, RadiusCard, 1, a.theme.CardBorder)

	// Flag dot.
	flagColor := a.theme.AccentGreen
	switch head.Flag {
	case "ru":
		flagColor = a.theme.AccentBlue
	case "en":
		flagColor = a.theme.AccentRed
	}
	drawFilledCircle(dst,
		float32(pillRect.Min.X+SpaceL+10),
		float32(pillRect.Min.Y+pillH/2),
		10, flagColor,
	)
	drawText(dst, head.Label, a.fonts.Body,
		pillRect.Min.X+SpaceL+30, pillRect.Min.Y+pillH/2+6, a.theme.TextPrimary)

	// Card stack underneath. Iterate newest-first so the freshest
	// transcript sits closest to the language pill — the screenshot
	// shows the most recent line at the top.
	y := pillRect.Max.Y + SpaceM
	for i := len(cards) - 1; i >= 0; i-- {
		c := cards[i]
		cardH := a.bubbleHeight(c)
		if y+cardH > r.Max.Y {
			break
		}
		cardRect := image.Rect(r.Min.X, y, r.Max.X, y+cardH)
		a.drawBubble(dst, cardRect, c)
		y = cardRect.Max.Y + SpaceM
	}
}

// bubbleHeight estimates the bubble height — single text line plus
// vertical padding. Multi-line wrap lands when the pane scrolls.
func (a *App) bubbleHeight(c Card) int {
	_, th := measureText(c.Text, a.fonts.Body)
	if th < 16 {
		th = 16
	}
	return int(th) + SpaceL*2
}

// drawBubble paints one card. Active cards get an accent outline.
func (a *App) drawBubble(dst *ebiten.Image, r image.Rectangle, c Card) {
	drawRoundRect(dst, r, RadiusCard, a.theme.Card)
	border := a.theme.CardBorder
	if c.Active {
		border = a.theme.CardActive
	}
	drawRoundRectBorder(dst, r, RadiusCard, 1, border)

	// Body text — vertically centered against the bubble's height.
	_, th := measureText(c.Text, a.fonts.Body)
	bodyY := r.Min.Y + (r.Dy()+int(th))/2 - 2
	drawText(dst, c.Text, a.fonts.Body,
		r.Min.X+SpaceL, bodyY, a.theme.TextPrimary)

	// Timestamp right-aligned at the same baseline.
	if c.Timestamp != "" {
		tw, _ := measureText(c.Timestamp, a.fonts.Caption)
		drawText(dst, c.Timestamp, a.fonts.Caption,
			r.Max.X-int(tw)-SpaceL, bodyY, a.theme.TextMuted)
	}
}
