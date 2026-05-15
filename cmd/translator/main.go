// Command translator is the Fyne-based desktop frontend for the
// realtime-speech-translator. M1 wires only a Start/Stop button to a
// Session that performs a raw mic -> playback loopback so the audio
// stack can be validated end-to-end before STT/MT/TTS are added.
package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	rstapp "github.com/alex60217101990/realtime-speech-translator/internal/app"
)

func main() {
	a := app.NewWithID("io.github.alex60217101990.rst")
	w := a.NewWindow("Realtime Speech Translator")
	w.Resize(fyne.NewSize(420, 260))

	sess, err := rstapp.New(rstapp.DefaultConfig())
	if err != nil {
		log.Fatalf("session init: %v", err)
	}
	defer func() {
		if err := sess.Close(); err != nil {
			log.Printf("session close: %v", err)
		}
	}()

	statusLbl := widget.NewLabel("Status: idle")
	dropsLbl := widget.NewLabel("Drops: 0    Underruns: 0")

	var btn *widget.Button
	btn = widget.NewButton("Start", func() {
		switch sess.State() {
		case rstapp.StateIdle:
			if err := sess.Start(); err != nil {
				statusLbl.SetText(fmt.Sprintf("Status: error: %v", err))
				return
			}
			btn.SetText("Stop")
		case rstapp.StateRunning:
			if err := sess.Stop(); err != nil {
				statusLbl.SetText(fmt.Sprintf("Status: error: %v", err))
				return
			}
			btn.SetText("Start")
		}
	})

	// UI poller: reads atomic state every 100ms and refreshes labels.
	// Fyne widgets are safe to update from any goroutine because Fyne
	// dispatches text changes onto its main loop internally.
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for range t.C {
			statusLbl.SetText("Status: " + sess.State().String())
			dropsLbl.SetText(fmt.Sprintf("Drops: %d    Underruns: %d",
				sess.DroppedSamples(), sess.Underruns()))
		}
	}()

	w.SetContent(container.NewVBox(
		widget.NewLabel("M1 loopback — mic captured at 16 kHz mono is replayed to the default output device."),
		statusLbl,
		dropsLbl,
		btn,
	))
	w.SetCloseIntercept(func() {
		_ = sess.Stop()
		w.Close()
		os.Exit(0)
	})
	w.ShowAndRun()
}
