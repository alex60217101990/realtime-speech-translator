// Command translator is the Fyne desktop frontend.
//
// M2: capture mic → VAD → Whisper → transcript pane. ModelPath defaults
// to the application's data dir; if no model is present a placeholder
// message guides the user to download one (auto-downloader UI is in
// M5/Models Manager). Until then, point --model at a ggml file.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/widget"

	rstapp "github.com/alex60217101990/realtime-speech-translator/internal/app"
)

func main() {
	modelFlag := flag.String("model", "", "path to Whisper ggml model (overrides default location)")
	langFlag := flag.String("lang", "auto", "source language (ISO-639-1) or 'auto'")
	flag.Parse()

	modelPath, err := resolveModelPath(*modelFlag)
	if err != nil {
		log.Fatalf("model path: %v", err)
	}

	cfg := rstapp.DefaultConfig()
	cfg.ModelPath = modelPath
	cfg.SourceLang = *langFlag

	sess, err := rstapp.New(cfg)
	if err != nil {
		log.Fatalf("session init: %v", err)
	}
	defer func() {
		if err := sess.Close(); err != nil {
			log.Printf("session close: %v", err)
		}
	}()

	a := app.NewWithID("io.github.alex60217101990.rst")
	w := a.NewWindow("Realtime Speech Translator")
	w.Resize(fyne.NewSize(560, 460))

	statusBind := binding.NewString()
	_ = statusBind.Set("Status: idle")
	statsBind := binding.NewString()
	_ = statsBind.Set("Latency: —   Drops: 0")
	transcriptBind := binding.NewString()
	_ = transcriptBind.Set("")

	statusLbl := widget.NewLabelWithData(statusBind)
	statsLbl := widget.NewLabelWithData(statsBind)
	transcriptArea := widget.NewMultiLineEntry()
	transcriptArea.Bind(transcriptBind)
	transcriptArea.SetMinRowsVisible(10)

	var btn *widget.Button
	btn = widget.NewButton("Start", func() {
		switch sess.State() {
		case rstapp.StateIdle:
			if err := sess.Start(); err != nil {
				_ = statusBind.Set("Status: error: " + err.Error())
				return
			}
			btn.SetText("Stop")
		case rstapp.StateRunning:
			if err := sess.Stop(); err != nil {
				_ = statusBind.Set("Status: error: " + err.Error())
				return
			}
			btn.SetText("Start")
		}
	})

	// Event consumer: appends each transcript to the panel.
	go func() {
		for ev := range sess.Events() {
			line := fmt.Sprintf("[%s] %s\n", ev.Language, ev.Text)
			cur, _ := transcriptBind.Get()
			_ = transcriptBind.Set(cur + line)
			_ = statsBind.Set(fmt.Sprintf("Latency: %s   Drops: %d",
				ev.Latency.Round(time.Millisecond), sess.DroppedSamples()))
		}
	}()

	// State poller: refreshes the status line.
	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for range t.C {
			_ = statusBind.Set("Status: " + sess.State().String())
		}
	}()

	w.SetContent(container.NewBorder(
		container.NewVBox(
			widget.NewLabel("M2 — capture → VAD → Whisper transcript"),
			statusLbl,
			statsLbl,
			btn,
		),
		nil, nil, nil,
		transcriptArea,
	))
	w.SetCloseIntercept(func() {
		_ = sess.Stop()
		w.Close()
		os.Exit(0)
	})
	w.ShowAndRun()
}

// resolveModelPath honours --model first; otherwise looks at the
// per-platform data dir under realtime-speech-translator/models/whisper/.
func resolveModelPath(flag string) (string, error) {
	if flag != "" {
		return filepath.Abs(flag)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "realtime-speech-translator", "models", "whisper", "ggml-small.bin"), nil
}
