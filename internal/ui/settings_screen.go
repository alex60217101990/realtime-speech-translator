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
	OnSave        func(config.Settings) // called after a successful Save
	OnThemeChange func(theme string)    // immediate; theme is cheap to flip
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

	whisperSel := widget.NewSelect(
		[]string{
			"tiny  (realtime ★)",
			"base  (realtime ★)",
			"small (balanced)",
			"medium (quality)",
			"large (quality)",
		},
		nil,
	)
	// Persisted config holds the bare model name; render the badge
	// in the dropdown for the UI but normalise back on save.
	whisperSel.SetSelected(decorateWhisper(cur.WhisperModel))
	form.Append("Whisper model", whisperSel)

	mtSel := widget.NewSelect(
		[]string{
			"small100 (realtime ★ — distilled m2m100, 330M)",
			"opusmt   (realtime ★ — fastest per-pair)",
			"m2m100   (balanced — 100 languages, 418M)",
			"madlad   (quality — slow on CPU)",
			"off",
		},
		nil,
	)
	mtSel.SetSelected(decorateMT(cur.MTBackend))
	form.Append("MT backend", mtSel)

	threadsEntry := widget.NewEntry()
	threadsEntry.SetText(fmt.Sprintf("%d", cur.Threads))
	threadsEntry.SetPlaceHolder("0 = auto")
	form.Append("Threads", threadsEntry)

	vadSlider := widget.NewSlider(0, 3)
	vadSlider.Step = 1
	vadSlider.Value = float64(cur.VADAggressiveness)
	vadVal := widget.NewLabel(fmt.Sprintf("%d", cur.VADAggressiveness))
	vadSlider.OnChanged = func(v float64) { vadVal.SetText(fmt.Sprintf("%d", int(v))) }
	form.Append("VAD aggressiveness", container.NewBorder(nil, nil, nil, vadVal, vadSlider))

	outDevEntry := widget.NewEntry()
	outDevEntry.SetText(cur.OutputDevice)
	outDevEntry.SetPlaceHolder("default = system speakers · or BlackHole / CABLE / rstranslator")
	form.Append("Output device", outDevEntry)

	ttsCheck := widget.NewCheck("Speak translations", nil)
	ttsCheck.SetChecked(cur.TTSEnabled)
	form.Append("Piper TTS", ttsCheck)

	piperBinEntry := widget.NewEntry()
	piperBinEntry.SetText(cur.TTSBinaryPath)
	piperBinEntry.SetPlaceHolder("(empty = PATH lookup)")
	form.Append("Piper binary", piperBinEntry)

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
			SourceLang:        srcEntry.Text,
			TargetLang:        dstEntry.Text,
			WhisperModel:      undecorate(whisperSel.Selected),
			MTBackend:         undecorate(mtSel.Selected),
			Threads:           parseInt(threadsEntry.Text, cur.Threads),
			VADAggressiveness: int(vadSlider.Value),
			OutputDevice:      outDevEntry.Text,
			TTSEnabled:        ttsCheck.Checked,
			TTSBinaryPath:     piperBinEntry.Text,
			Theme:             themeSel.Selected,
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
// option, i.e. strips the trailing tier annotation ("base  (realtime
// ★)" -> "base"). Keeps config.Settings free of UI noise.
func undecorate(s string) string {
	for i, r := range s {
		if r == ' ' || r == '\t' {
			return s[:i]
		}
	}
	return s
}

// decorateWhisper returns the dropdown label that matches a stored
// Whisper model name; falls back to the raw value if the persisted
// option no longer exists in the list.
func decorateWhisper(name string) string {
	for _, opt := range []string{
		"tiny  (realtime ★)",
		"base  (realtime ★)",
		"small (balanced)",
		"medium (quality)",
		"large (quality)",
	} {
		if undecorate(opt) == name {
			return opt
		}
	}
	return name
}

// decorateMT mirrors decorateWhisper for the MT backend selector.
func decorateMT(name string) string {
	for _, opt := range []string{
		"small100 (realtime ★ — distilled m2m100, 330M)",
		"opusmt   (realtime ★ — fastest per-pair)",
		"m2m100   (balanced — 100 languages, 418M)",
		"madlad   (quality — slow on CPU)",
		"off",
	} {
		if undecorate(opt) == name {
			return opt
		}
	}
	return name
}
