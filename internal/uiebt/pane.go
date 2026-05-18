package uiebt

import (
	"image"
	"strings"
	"unicode/utf8"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
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
		lines := a.wrapCard(c, r.Dx())
		cardH := a.bubbleHeight(lines)
		if y+cardH > r.Max.Y {
			break
		}
		cardRect := image.Rect(r.Min.X, y, r.Max.X, y+cardH)
		a.drawBubble(dst, cardRect, c, lines)
		y = cardRect.Max.Y + SpaceM
	}
}

// wrapCard splits the card text into lines that fit inside a bubble
// of width cardW. The first line reserves room on the right for the
// timestamp so the two never collide.
func (a *App) wrapCard(c Card, cardW int) []string {
	pad := SpaceL * 2
	tsRes := 0
	if c.Timestamp != "" {
		tw, _ := measureText(c.Timestamp, a.fonts.Caption)
		tsRes = int(tw) + SpaceM
	}
	contentW := cardW - pad
	if contentW < 40 {
		contentW = 40
	}
	firstW := contentW - tsRes
	if firstW < 40 {
		firstW = contentW
	}
	return wrapText(c.Text, a.fonts.Body, float64(firstW), float64(contentW))
}

// bubbleHeight returns the rendered height for a wrapped card.
func (a *App) bubbleHeight(lines []string) int {
	if len(lines) == 0 {
		lines = []string{""}
	}
	_, lh := measureText("Ag", a.fonts.Body)
	if lh < 18 {
		lh = 18
	}
	return int(lh)*len(lines) + SpaceL*2
}

// drawBubble paints one card. Active cards get an accent outline.
// lines is the pre-wrapped body produced by wrapCard.
func (a *App) drawBubble(dst *ebiten.Image, r image.Rectangle, c Card, lines []string) {
	drawRoundRect(dst, r, RadiusCard, a.theme.Card)
	border := a.theme.CardBorder
	if c.Active {
		border = a.theme.CardActive
	}
	drawRoundRectBorder(dst, r, RadiusCard, 1, border)

	if len(lines) == 0 {
		lines = []string{c.Text}
	}
	_, lh := measureText("Ag", a.fonts.Body)
	if lh < 18 {
		lh = 18
	}
	lineH := int(lh)
	y := r.Min.Y + SpaceL + lineH - 4
	for _, line := range lines {
		drawText(dst, line, a.fonts.Body,
			r.Min.X+SpaceL, y, a.theme.TextPrimary)
		y += lineH
	}

	if c.Timestamp != "" {
		tw, _ := measureText(c.Timestamp, a.fonts.Caption)
		tsY := r.Min.Y + SpaceL + lineH - 4
		drawText(dst, c.Timestamp, a.fonts.Caption,
			r.Max.X-int(tw)-SpaceL, tsY, a.theme.TextMuted)
	}
}

// wrapText greedily packs words into lines whose rendered width
// does not exceed maxW. The first line gets firstW (smaller, to
// leave room for the timestamp) and every subsequent line gets the
// full maxW. Falls back to a per-rune split when a single token is
// already wider than the budget (e.g. CJK / long URL).
func wrapText(s string, face text.Face, firstW, restW float64) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{s}
	}
	budget := firstW
	var lines []string
	cur := ""
	for _, w := range words {
		trial := w
		if cur != "" {
			trial = cur + " " + w
		}
		tw, _ := measureText(trial, face)
		if tw <= budget {
			cur = trial
			continue
		}
		if cur != "" {
			lines = append(lines, cur)
			budget = restW
		}
		// w on its own exceeds budget — break it per-rune.
		wW, _ := measureText(w, face)
		if wW > budget {
			lines = append(lines, splitWide(w, face, budget)...)
			cur = ""
			continue
		}
		cur = w
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// splitWide chops a single token into runes, packing as many as fit.
func splitWide(s string, face text.Face, maxW float64) []string {
	var out []string
	cur := ""
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		trial := cur + string(r)
		tw, _ := measureText(trial, face)
		if tw > maxW && cur != "" {
			out = append(out, cur)
			cur = string(r)
		} else {
			cur = trial
		}
		i += size
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
