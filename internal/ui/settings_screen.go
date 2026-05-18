package ui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/alex60217101990/realtime-speech-translator/internal/config"
)

// SettingsCallbacks lets the host wire side effects when the user
// changes a value. The host typically restarts the Session for the
// changes that require it (model swap, target language).
type SettingsCallbacks struct {
	// AvailableSTTModels lists the directories present under
	// <data>/models/stt/. Empty list disables the dropdown.
	AvailableSTTModels []string
	// AvailableTTSVoices lists the directories present under
	// <data>/models/tts/. Empty list disables the dropdown.
	AvailableTTSVoices []string

	// OnSave is called after a successful Save.
	OnSave func(config.Settings)
	// OnThemeChange fires immediately when the theme dropdown
	// changes — theme flips are cheap and do not require a restart.
	OnThemeChange func(theme string)
}

// SettingsScreen renders a form bound to config.Settings persisted via
// config.Save. The Save button writes the file and invokes OnSave.
func SettingsScreen(w fyne.Window, cur config.Settings, cb SettingsCallbacks) fyne.CanvasObject {
	form := &widget.Form{}

	srcEntry := widget.NewEntry()
	srcEntry.SetText(cur.SourceLang)
	srcEntry.SetPlaceHolder("auto / en / ru / ...")
	form.Append("Source language", srcEntry)

	dstEntry := widget.NewEntry()
	dstEntry.SetText(cur.TargetLang)
	dstEntry.SetPlaceHolder("en / ru / es / ...")
	form.Append("Target language", dstEntry)

	sttOptions := cb.AvailableSTTModels
	if len(sttOptions) == 0 {
		// Surface the current value even if the directory listing is
		// empty, so users can see what is saved and the field stays
		// editable.
		sttOptions = []string{cur.STTModel}
	}
	sttSel := widget.NewSelect(sttOptions, nil)
	sttSel.SetSelected(cur.STTModel)
	form.Append("STT model (streaming)", sttSel)

	mtSel := widget.NewSelect(
		[]string{
			"small100 (realtime ★ — one model, 100 languages)",
			"opusmt   (realtime ★ — per-pair, fastest)",
			"off      (passthrough — no translation)",
		},
		nil,
	)
	mtSel.SetSelected(decorateMT(cur.MTBackend))
	form.Append("MT backend", mtSel)

	ttsOptions := cb.AvailableTTSVoices
	if len(ttsOptions) == 0 {
		ttsOptions = []string{cur.TTSVoice}
	}
	ttsSel := widget.NewSelect(ttsOptions, nil)
	ttsSel.SetSelected(cur.TTSVoice)
	form.Append("TTS voice (Piper)", ttsSel)

	threadsEntry := widget.NewEntry()
	threadsEntry.SetText(fmt.Sprintf("%d", cur.Threads))
	threadsEntry.SetPlaceHolder("0 = auto (NumCPU)")
	form.Append("Threads", threadsEntry)

	vadSlider := widget.NewSlider(0, 1)
	vadSlider.Step = 0.05
	vadSlider.Value = float64(cur.VADThreshold)
	vadVal := widget.NewLabel(fmt.Sprintf("%.2f", cur.VADThreshold))
	vadSlider.OnChanged = func(v float64) { vadVal.SetText(fmt.Sprintf("%.2f", v)) }
	form.Append("VAD threshold (0..1)", container.NewBorder(nil, nil, nil, vadVal, vadSlider))

	outDevEntry := widget.NewEntry()
	outDevEntry.SetText(cur.OutputDevice)
	outDevEntry.SetPlaceHolder("BlackHole / CABLE / rstranslator (substring)")
	form.Append("Output device", outDevEntry)

	ttsCheck := widget.NewCheck("Speak translations", nil)
	ttsCheck.SetChecked(cur.TTSEnabled)
	form.Append("Speak", ttsCheck)

	themeSel := widget.NewSelect(
		[]string{"system", "light", "dark"},
		func(s string) {
			if cb.OnThemeChange != nil {
				cb.OnThemeChange(s)
			}
		},
	)
	themeSel.SetSelected(cur.Theme)
	form.Append("Theme", themeSel)

	status := widget.NewLabel("")
	saveBtn := widget.NewButton("Save", func() {
		s := config.Settings{
			SourceLang:   srcEntry.Text,
			TargetLang:   dstEntry.Text,
			STTModel:     sttSel.Selected,
			MTBackend:    undecorate(mtSel.Selected),
			TTSVoice:     ttsSel.Selected,
			Threads:      parseInt(threadsEntry.Text, cur.Threads),
			VADThreshold: float32(vadSlider.Value),
			OutputDevice: outDevEntry.Text,
			TTSEnabled:   ttsCheck.Checked,
			Theme:        themeSel.Selected,
		}
		if err := config.Save(s); err != nil {
			dialog.ShowError(err, w)
			return
		}
		status.SetText("Saved. Some changes require restart.")
		if cb.OnSave != nil {
			cb.OnSave(s)
		}
	})

	return container.NewBorder(
		widget.NewLabelWithStyle("Settings", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewHBox(saveBtn, status),
		nil, nil,
		container.NewVScroll(form),
	)
}

func parseInt(s string, def int) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return def
	}
	return n
}

// undecorate returns the first whitespace-delimited token of a Select
// option, i.e. strips the trailing tier annotation ("opusmt   (...)"
// → "opusmt"). Keeps config.Settings free of UI noise.
func undecorate(s string) string {
	for i, r := range s {
		if r == ' ' || r == '\t' {
			return s[:i]
		}
	}
	return s
}

// decorateMT picks the dropdown label that matches a stored MT
// backend name; falls back to the raw value if the persisted option
// no longer exists.
func decorateMT(name string) string {
	for _, opt := range []string{
		"small100 (realtime ★ — one model, 100 languages)",
		"opusmt   (realtime ★ — per-pair, fastest)",
		"off      (passthrough — no translation)",
	} {
		if undecorate(opt) == name {
			return opt
		}
	}
	return name
}
