package uiebt

import (
	"fmt"
	"image"
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
)

// SettingsSnapshot is the slice of internal/config.Settings the
// Settings tab needs to render + mutate. Lives here so uiebt does
// not import internal/config; the cmd layer maps between the two
// with a tiny adapter in cmd/translator-ui/settings_wire.go.
type SettingsSnapshot struct {
	SourceLang   string
	TargetLang   string
	STTModel     string // dir name under models/stt/, or "auto"
	MTBackend    string // "m2m100" | "small100" | "opusmt" | "off"
	TTSVoice     string // dir name under models/tts/
	TTSEnabled   bool
	VADThreshold float32
	Threads      int
	OutputDevice string

	// Options enumerate the choices for each dropdown. Filled by
	// the cmd layer based on what's actually installed on disk +
	// what the catalog knows about.
	STTOptions []string
	TTSOptions []string
}

// SettingsSaver is invoked when the user hits Save. The cmd layer
// wires it to config.Save plus a "restart required" toast.
type SettingsSaver func(s SettingsSnapshot) error

// SetSettings publishes the form's initial state and the save
// callback. Safe to re-call (e.g. after a successful save) to
// refresh the dropdown options.
func (a *App) SetSettings(s SettingsSnapshot, save SettingsSaver) {
	a.mu.Lock()
	a.settings = s
	a.settingsDraft = s
	a.settingsSave = save
	a.settingsDirty = false
	a.settingsMsg = ""
	a.mu.Unlock()
}

// drawSettingsPane renders the Settings tab. Layout: stacked rows
// of label + segmented control (or +/− chips for numerics) + a
// sticky Save button at the bottom.
func (a *App) drawSettingsPane(dst *ebiten.Image, r image.Rectangle) {
	a.mu.RLock()
	draft := a.settingsDraft
	dirty := a.settingsDirty
	msg := a.settingsMsg
	a.mu.RUnlock()

	pad := SpaceL
	innerX := r.Min.X + pad
	innerW := r.Dx() - pad*2
	rowY := r.Min.Y + pad

	rowH := 44
	gap := SpaceS

	type row struct {
		label string
		draw  func(rect image.Rectangle)
	}

	rows := []row{
		{"Язык-источник", func(rect image.Rectangle) {
			a.drawSegmented(dst, rect,
				[]string{"ru", "en", "auto"},
				draft.SourceLang,
				func(v string) { a.mutateSettings(func(s *SettingsSnapshot) { s.SourceLang = v }) })
		}},
		{"Язык-цель", func(rect image.Rectangle) {
			a.drawSegmented(dst, rect,
				[]string{"en", "ru", "es", "de", "fr"},
				draft.TargetLang,
				func(v string) { a.mutateSettings(func(s *SettingsSnapshot) { s.TargetLang = v }) })
		}},
		{"STT-модель", func(rect image.Rectangle) {
			opts := draft.STTOptions
			if len(opts) == 0 {
				opts = []string{"auto"}
			}
			a.drawSegmented(dst, rect, opts, draft.STTModel,
				func(v string) { a.mutateSettings(func(s *SettingsSnapshot) { s.STTModel = v }) })
		}},
		{"MT-бэкенд", func(rect image.Rectangle) {
			a.drawSegmented(dst, rect,
				[]string{"m2m100", "small100", "opusmt", "off"},
				draft.MTBackend,
				func(v string) { a.mutateSettings(func(s *SettingsSnapshot) { s.MTBackend = v }) })
		}},
		{"TTS-голос", func(rect image.Rectangle) {
			opts := draft.TTSOptions
			if len(opts) == 0 {
				opts = []string{"piper-en-amy-low"}
			}
			a.drawSegmented(dst, rect, opts, draft.TTSVoice,
				func(v string) { a.mutateSettings(func(s *SettingsSnapshot) { s.TTSVoice = v }) })
		}},
		{"TTS включён", func(rect image.Rectangle) {
			labels := []string{"да", "нет"}
			cur := "нет"
			if draft.TTSEnabled {
				cur = "да"
			}
			a.drawSegmented(dst, rect, labels, cur,
				func(v string) {
					a.mutateSettings(func(s *SettingsSnapshot) { s.TTSEnabled = v == "да" })
				})
		}},
		{"VAD threshold", func(rect image.Rectangle) {
			a.drawNumericStepper(dst, rect,
				fmt.Sprintf("%.2f", draft.VADThreshold),
				func(d int) {
					a.mutateSettings(func(s *SettingsSnapshot) {
						s.VADThreshold = clampF32(s.VADThreshold+float32(d)*0.05, 0, 1)
					})
				})
		}},
		{"Threads", func(rect image.Rectangle) {
			lbl := fmt.Sprintf("%d", draft.Threads)
			if draft.Threads == 0 {
				lbl = "auto"
			}
			a.drawNumericStepper(dst, rect, lbl,
				func(d int) {
					a.mutateSettings(func(s *SettingsSnapshot) {
						n := s.Threads + d
						if n < 0 {
							n = 0
						}
						if n > 32 {
							n = 32
						}
						s.Threads = n
					})
				})
		}},
	}

	labelCol := 160
	for _, ro := range rows {
		if rowY+rowH > r.Max.Y-72 {
			break
		}
		drawText(dst, ro.label, a.fonts.Body,
			innerX, rowY+28, a.theme.TextSecondary)
		ctrlRect := image.Rect(innerX+labelCol, rowY,
			innerX+innerW, rowY+rowH)
		ro.draw(ctrlRect)
		rowY += rowH + gap
	}

	// Bottom bar: Save button + dirty / message label.
	saveBtnW := 180
	saveBtnH := 36
	saveRect := image.Rect(r.Max.X-pad-saveBtnW, r.Max.Y-pad-saveBtnH,
		r.Max.X-pad, r.Max.Y-pad)
	saveFill := a.theme.Card
	saveBorder := a.theme.CardBorder
	saveTxt := a.theme.TextMuted
	if dirty {
		saveFill = a.theme.AccentBlue
		saveBorder = a.theme.AccentCyan
		saveTxt = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	}
	drawRoundRect(dst, saveRect, RadiusButton, saveFill)
	drawRoundRectBorder(dst, saveRect, RadiusButton, 1, saveBorder)
	drawTextCentered(dst, "Сохранить", a.fonts.Body,
		saveRect.Min.X+saveRect.Dx()/2,
		saveRect.Min.Y+saveRect.Dy()/2, saveTxt)
	if dirty {
		a.registerHit(saveRect, a.persistSettings)
	}

	if msg != "" {
		drawText(dst, msg, a.fonts.Caption,
			innerX, r.Max.Y-pad-saveBtnH+24, a.theme.TextSecondary)
	}
}

// drawSegmented paints a row of pill-buttons; the matching label
// is highlighted, click on any other fires onPick.
func (a *App) drawSegmented(dst *ebiten.Image, r image.Rectangle, options []string, current string, onPick func(string)) {
	const segPadX = 14
	const segH = 30
	x := r.Min.X
	y := r.Min.Y + (r.Dy()-segH)/2
	for _, opt := range options {
		lw, _ := measureText(opt, a.fonts.Body)
		segW := int(lw) + segPadX*2
		if x+segW > r.Max.X {
			break
		}
		segRect := image.Rect(x, y, x+segW, y+segH)
		fill := a.theme.Card
		border := a.theme.CardBorder
		txt := a.theme.TextSecondary
		if opt == current {
			fill = a.theme.AccentBlue
			border = a.theme.AccentCyan
			txt = color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
		}
		drawRoundRect(dst, segRect, RadiusButton, fill)
		drawRoundRectBorder(dst, segRect, RadiusButton, 1, border)
		drawTextCentered(dst, opt, a.fonts.Body,
			segRect.Min.X+segRect.Dx()/2,
			segRect.Min.Y+segRect.Dy()/2, txt)
		picked := opt
		a.registerHit(segRect, func() { onPick(picked) })
		x += segW + SpaceXS
	}
}

// drawNumericStepper paints a label flanked by − / + chips.
func (a *App) drawNumericStepper(dst *ebiten.Image, r image.Rectangle, value string, onStep func(d int)) {
	const chipW = 36
	const chipH = 30
	y := r.Min.Y + (r.Dy()-chipH)/2
	minus := image.Rect(r.Min.X, y, r.Min.X+chipW, y+chipH)
	value0 := image.Rect(minus.Max.X+SpaceXS, y, minus.Max.X+SpaceXS+100, y+chipH)
	plus := image.Rect(value0.Max.X+SpaceXS, y, value0.Max.X+SpaceXS+chipW, y+chipH)

	for _, rect := range []image.Rectangle{minus, plus} {
		drawRoundRect(dst, rect, RadiusButton, a.theme.Card)
		drawRoundRectBorder(dst, rect, RadiusButton, 1, a.theme.CardBorder)
	}
	drawTextCentered(dst, "−", a.fonts.Body,
		minus.Min.X+chipW/2, minus.Min.Y+chipH/2, a.theme.TextPrimary)
	drawTextCentered(dst, "+", a.fonts.Body,
		plus.Min.X+chipW/2, plus.Min.Y+chipH/2, a.theme.TextPrimary)
	drawTextCentered(dst, value, a.fonts.Body,
		value0.Min.X+value0.Dx()/2, value0.Min.Y+value0.Dy()/2,
		a.theme.TextPrimary)

	a.registerHit(minus, func() { onStep(-1) })
	a.registerHit(plus, func() { onStep(+1) })
}

// mutateSettings applies f to the draft + flips dirty so the Save
// button lights up.
func (a *App) mutateSettings(f func(*SettingsSnapshot)) {
	a.mu.Lock()
	f(&a.settingsDraft)
	a.settingsDirty = true
	a.settingsMsg = ""
	a.mu.Unlock()
}

// persistSettings invokes the cmd-layer saver and surfaces success
// / error in the bottom-bar message line.
func (a *App) persistSettings() {
	a.mu.Lock()
	draft := a.settingsDraft
	save := a.settingsSave
	a.mu.Unlock()
	if save == nil {
		return
	}
	err := save(draft)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.settingsMsg = "Ошибка сохранения: " + err.Error()
		return
	}
	a.settings = draft
	a.settingsDirty = false
	a.settingsMsg = "Сохранено — применяется (сессия перезагружается)"
}

func clampF32(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Compile-time anchor — settings_pane.go pulls in ebiten.Image
// only via dst params, keep the import alive.
var _ = (*ebiten.Image)(nil)
