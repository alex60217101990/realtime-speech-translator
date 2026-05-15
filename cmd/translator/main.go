// Command translator is the Fyne desktop frontend.
//
// M3b assembles the full audio loop. The UI exposes:
//
//   - --model: path to a Whisper ggml file
//   - --src / --dst: source and target language (ISO-639-1)
//   - --mt madlad|opusmt|off plus per-backend paths
//   - --tts on|off plus --piper-bin and per-language voice paths
//
// At runtime the user toggles the session Start/Stop and watches the
// transcript and translation panes fill in. With --tts on the
// translation is also spoken through the system playback device; with
// the right virtual audio device selected (M4) it becomes a microphone
// for other applications.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/widget"

	rstapp "github.com/alex60217101990/realtime-speech-translator/internal/app"
	"github.com/alex60217101990/realtime-speech-translator/internal/mt"
	"github.com/alex60217101990/realtime-speech-translator/internal/tts/piper"
)

type voiceFlag map[string]string

func (f *voiceFlag) String() string {
	if f == nil {
		return ""
	}
	out := make([]string, 0, len(*f))
	for k, v := range *f {
		out = append(out, k+"="+v)
	}
	return strings.Join(out, ",")
}

func (f *voiceFlag) Set(s string) error {
	if *f == nil {
		*f = map[string]string{}
	}
	parts := strings.SplitN(s, "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("expected lang=path, got %q", s)
	}
	(*f)[parts[0]] = parts[1]
	return nil
}

func main() {
	modelFlag := flag.String("model", "", "path to Whisper ggml model")
	mtBackend := flag.String("mt", "madlad", "MT backend: madlad | opusmt | off")
	mtModelDir := flag.String("mt-model", "", "MADLAD model directory (CTranslate2 export)")
	mtSPModel := flag.String("mt-spm", "", "MADLAD sentencepiece.model path")
	mtOPUSRoot := flag.String("opusmt-root", "", "OPUS-MT models root with {src}-{dst} subdirs")
	srcFlag := flag.String("src", "auto", "source language (ISO-639-1) or 'auto'")
	dstFlag := flag.String("dst", "en", "target language (ISO-639-1)")

	ttsOn := flag.Bool("tts", true, "enable Piper TTS playback of translations")
	piperBin := flag.String("piper-bin", "", "path to piper executable (default: lookup on PATH)")
	var voices voiceFlag
	flag.Var(&voices, "voice", "register a Piper voice as lang=onnx-path; repeat for multiple languages")

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
	default:
		log.Fatalf("unknown --mt backend: %s", *mtBackend)
	}

	if *ttsOn {
		pcfg := piper.DefaultConfig()
		pcfg.BinaryPath = *piperBin
		peng, err := piper.New(pcfg)
		if err != nil {
			log.Printf("tts disabled: %v", err)
		} else {
			for lang, path := range voices {
				if err := peng.AddVoice(piper.Voice{Lang: lang, ONNXPath: path}); err != nil {
					log.Printf("tts voice %s skipped: %v", lang, err)
				}
			}
			if peng.HasVoice(cfg.TargetLang) {
				cfg.TTSBackend = peng
			} else {
				log.Printf("tts disabled: no voice registered for target %q", cfg.TargetLang)
			}
		}
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
	w.Resize(fyne.NewSize(960, 560))

	statusBind := binding.NewString()
	_ = statusBind.Set("Status: idle")
	statsBind := binding.NewString()
	_ = statsBind.Set("STT: —   MT: —   TTS: —   Drops: 0   Underruns: 0")
	transcriptBind := binding.NewString()
	translationBind := binding.NewString()
	_ = transcriptBind.Set("")
	_ = translationBind.Set("")

	statusLbl := widget.NewLabelWithData(statusBind)
	statsLbl := widget.NewLabelWithData(statsBind)
	transcriptArea := widget.NewMultiLineEntry()
	transcriptArea.Bind(transcriptBind)
	transcriptArea.SetMinRowsVisible(14)
	translationArea := widget.NewMultiLineEntry()
	translationArea.Bind(translationBind)
	translationArea.SetMinRowsVisible(14)

	ttsStatus := "off"
	if cfg.TTSBackend != nil {
		ttsStatus = "piper"
	}
	srcLabel := widget.NewLabel(fmt.Sprintf("Source: %s", cfg.SourceLang))
	dstLabel := widget.NewLabel(fmt.Sprintf("Target: %s", cfg.TargetLang))
	backendLabel := widget.NewLabel(fmt.Sprintf("MT: %s   TTS: %s", *mtBackend, ttsStatus))

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
			_ = statsBind.Set(fmt.Sprintf(
				"STT: %s   MT: %s   TTS: %s   Drops: %d   Underruns: %d",
				ev.STTLatency.Round(time.Millisecond),
				ev.MTLatency.Round(time.Millisecond),
				ev.TTSLatency.Round(time.Millisecond),
				sess.DroppedSamples(),
				sess.PlaybackUnderruns(),
			))
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
