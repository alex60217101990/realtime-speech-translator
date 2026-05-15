// Command translator is the Fyne desktop frontend.
//
// M3a wires the MT engine after Whisper: the UI exposes source and
// target language dropdowns plus a backend picker (MADLAD-400 default,
// OPUS-MT alternative). The transcript pane is split into two columns —
// recognised text on the left, translated text on the right.
//
// TTS playback returns in M3b.
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
	"github.com/alex60217101990/realtime-speech-translator/internal/mt"
)

func main() {
	modelFlag := flag.String("model", "", "path to Whisper ggml model")
	mtBackend := flag.String("mt", "madlad", "MT backend: madlad | opusmt | off")
	mtModelDir := flag.String("mt-model", "", "MADLAD model directory (CTranslate2 export)")
	mtSPModel := flag.String("mt-spm", "", "MADLAD sentencepiece.model path")
	mtOPUSRoot := flag.String("opusmt-root", "", "OPUS-MT models root with {src}-{dst} subdirs")
	srcFlag := flag.String("src", "auto", "source language (ISO-639-1) or 'auto'")
	dstFlag := flag.String("dst", "en", "target language (ISO-639-1)")
	flag.Parse()

	modelPath, err := resolveModelPath(*modelFlag)
	if err != nil {
		log.Fatalf("model path: %v", err)
	}

	cfg := rstapp.DefaultConfig()
	cfg.ModelPath = modelPath
	cfg.SourceLang = *srcFlag
	cfg.TargetLang = *dstFlag

	switch *mtBackend {
	case "madlad":
		if *mtModelDir != "" && *mtSPModel != "" {
			eng, err := mt.NewMADLAD(mt.DefaultMADLADConfig(*mtModelDir, *mtSPModel))
			if err != nil {
				log.Fatalf("madlad: %v", err)
			}
			cfg.MTBackend = eng
		}
	case "opusmt":
		if *mtOPUSRoot != "" {
			eng, err := mt.NewOPUSMT(mt.DefaultOPUSMTConfig(*mtOPUSRoot))
			if err != nil {
				log.Fatalf("opusmt: %v", err)
			}
			cfg.MTBackend = eng
		}
	case "off":
		// transcript-only mode
	default:
		log.Fatalf("unknown --mt backend: %s", *mtBackend)
	}

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
	w.Resize(fyne.NewSize(880, 520))

	statusBind := binding.NewString()
	_ = statusBind.Set("Status: idle")
	statsBind := binding.NewString()
	_ = statsBind.Set("STT: —   MT: —   Drops: 0")
	transcriptBind := binding.NewString()
	translationBind := binding.NewString()
	_ = transcriptBind.Set("")
	_ = translationBind.Set("")

	statusLbl := widget.NewLabelWithData(statusBind)
	statsLbl := widget.NewLabelWithData(statsBind)
	transcriptArea := widget.NewMultiLineEntry()
	transcriptArea.Bind(transcriptBind)
	transcriptArea.SetMinRowsVisible(12)
	translationArea := widget.NewMultiLineEntry()
	translationArea.Bind(translationBind)
	translationArea.SetMinRowsVisible(12)

	srcLabel := widget.NewLabel(fmt.Sprintf("Source: %s", cfg.SourceLang))
	dstLabel := widget.NewLabel(fmt.Sprintf("Target: %s", cfg.TargetLang))
	backendLabel := widget.NewLabel(fmt.Sprintf("MT: %s", *mtBackend))

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

	go func() {
		for ev := range sess.Events() {
			cur, _ := transcriptBind.Get()
			_ = transcriptBind.Set(cur + fmt.Sprintf("[%s] %s\n", ev.Language, ev.Text))
			if ev.Translation != "" {
				cur2, _ := translationBind.Get()
				_ = translationBind.Set(cur2 + fmt.Sprintf("[%s] %s\n", ev.TargetLang, ev.Translation))
			}
			_ = statsBind.Set(fmt.Sprintf("STT: %s   MT: %s   Drops: %d",
				ev.STTLatency.Round(time.Millisecond),
				ev.MTLatency.Round(time.Millisecond),
				sess.DroppedSamples()))
		}
	}()

	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for range t.C {
			_ = statusBind.Set("Status: " + sess.State().String())
		}
	}()

	header := container.NewVBox(
		container.NewHBox(srcLabel, dstLabel, backendLabel),
		statusLbl,
		statsLbl,
		btn,
	)
	panes := container.NewGridWithColumns(2,
		container.NewBorder(widget.NewLabel("Transcript"), nil, nil, nil, transcriptArea),
		container.NewBorder(widget.NewLabel("Translation"), nil, nil, nil, translationArea),
	)
	w.SetContent(container.NewBorder(header, nil, nil, nil, panes))
	w.SetCloseIntercept(func() {
		_ = sess.Stop()
		w.Close()
		os.Exit(0)
	})
	w.ShowAndRun()
}

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
