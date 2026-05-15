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
		[]string{"tiny", "base", "small", "medium", "large"},
		nil,
	)
	whisperSel.SetSelected(cur.WhisperModel)
	form.Append("Whisper model", whisperSel)

	mtSel := widget.NewSelect(
		[]string{"madlad", "opusmt", "off"},
		nil,
	)
	mtSel.SetSelected(cur.MTBackend)
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
	outDevEntry.SetPlaceHolder("BlackHole / CABLE / rstranslator (substring)")
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
			WhisperModel:      whisperSel.Selected,
			MTBackend:         mtSel.Selected,
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
