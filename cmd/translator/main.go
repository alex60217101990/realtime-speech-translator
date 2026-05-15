// Command translator is the Fyne desktop frontend.
//
// M4 layered virtual-mic detection and a startup install guide on top
// of M3b's TTS pipeline. The application now:
//
//   1. Enumerates playback devices via internal/vmic.
//   2. Picks a virtual-mic device (BlackHole / VB-CABLE / PulseAudio
//      null sink) automatically; --output-device "name substring"
//      overrides the choice.
//   3. If no virtual mic is found, opens a dialog walking the user
//      through installing one. The session still starts in
//      "preview only" mode so the user can verify STT+MT+TTS work
//      against the system speakers while the driver is installing.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"github.com/gen2brain/malgo"

	rstapp "github.com/alex60217101990/realtime-speech-translator/internal/app"
	"github.com/alex60217101990/realtime-speech-translator/internal/mt"
	"github.com/alex60217101990/realtime-speech-translator/internal/tts/piper"
	"github.com/alex60217101990/realtime-speech-translator/internal/vmic"
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

	outDevSub := flag.String("output-device", "", "playback device name substring (default: first detected virtual mic, else system default)")

	flag.Parse()

	modelPath, err := resolveModelPath(*modelFlag)
	if err != nil {
		log.Fatalf("model path: %v", err)
	}

	cfg := rstapp.DefaultConfig()
	cfg.ModelPath = modelPath
	cfg.SourceLang = *srcFlag
	cfg.TargetLang = *dstFlag

	// Pick playback device. We do this before constructing the
	// Session so the device decision is visible in startup logs.
	chosenDevName, chosenDevKind, missingVMic := pickPlaybackDevice(*outDevSub, &cfg)
	if missingVMic {
		log.Printf("vmic: no virtual mic detected — falling back to default output (preview mode)")
	} else {
		log.Printf("vmic: routing to %q (%s)", chosenDevName, chosenDevKind)
	}

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
	w.Resize(fyne.NewSize(960, 600))

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
	vmicStatus := "not found"
	if !missingVMic {
		vmicStatus = chosenDevName
	}
	srcLabel := widget.NewLabel(fmt.Sprintf("Source: %s", cfg.SourceLang))
	dstLabel := widget.NewLabel(fmt.Sprintf("Target: %s", cfg.TargetLang))
	backendLabel := widget.NewLabel(fmt.Sprintf("MT: %s   TTS: %s   VMic: %s", *mtBackend, ttsStatus, vmicStatus))

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

	// First-paint hook to surface the install wizard if no virtual
	// mic was detected. We do this after the window is built so the
	// dialog has a parent to attach to.
	if missingVMic {
		w.SetOnClosed(func() { os.Exit(0) })
		go func() {
			time.Sleep(150 * time.Millisecond) // let the main window settle
			showVMicWizard(a, w)
		}()
	}

	w.ShowAndRun()
}

// pickPlaybackDevice picks a virtual-mic playback device and stamps its
// ID into cfg.PlaybackDeviceID. Returns the resolved name + kind for
// display, and a missingVMic flag that the UI uses to trigger the
// install wizard. When override is non-empty it takes precedence over
// auto-detection.
func pickPlaybackDevice(override string, cfg *rstapp.Config) (name string, kind string, missing bool) {
	if override != "" {
		all, err := vmic.AllQuick()
		if err != nil {
			log.Printf("vmic: enumerate failed: %v", err)
			return "", "", true
		}
		needle := strings.ToLower(override)
		for i := range all {
			if strings.Contains(strings.ToLower(all[i].Name), needle) {
				id := all[i].ID
				cfg.PlaybackDeviceID = &id
				return all[i].Name, all[i].Kind.String(), false
			}
		}
		log.Printf("vmic: --output-device %q did not match any device, falling back", override)
	}

	found, err := vmic.DetectQuick()
	if err != nil {
		log.Printf("vmic: detect failed: %v", err)
		return "", "", true
	}
	if len(found) == 0 {
		return "", "", true
	}
	id := found[0].ID
	cfg.PlaybackDeviceID = &id
	return found[0].Name, found[0].Kind.String(), false
}

// showVMicWizard renders a non-blocking modal walking the user through
// installing a virtual-mic driver appropriate for their OS. We use a
// custom-confirm dialog rather than dialog.ShowCustom because we want
// to expose three distinct actions: open URL, auto-install (Linux), and
// dismiss-and-recheck.
func showVMicWizard(a fyne.App, w fyne.Window) {
	g := vmic.GuideFor()

	body := container.NewVBox(
		widget.NewLabel(g.Title),
		widget.NewLabel("Without a virtual mic, the translator can speak audio out of your speakers but other apps (Zoom, Discord) will not see it as a microphone input."),
	)
	for i, step := range g.Steps {
		body.Add(widget.NewLabel(fmt.Sprintf("%d. %s", i+1, step)))
	}
	if g.AutoInstallCmd != "" {
		body.Add(widget.NewLabel(""))
		body.Add(widget.NewLabel("Command:"))
		cmdLbl := widget.NewLabel(g.AutoInstallCmd)
		cmdLbl.Wrapping = fyne.TextWrapBreak
		body.Add(cmdLbl)
	}

	d := dialog.NewCustomConfirm("Virtual microphone setup", "Continue (preview only)", "Open install page", body, func(openURL bool) {
		if openURL && g.URL != "" {
			if u, err := url.Parse(g.URL); err == nil {
				_ = a.OpenURL(u)
			}
		}
	}, w)
	d.Resize(fyne.NewSize(640, 420))
	d.Show()

	// Linux-only: offer a separate "auto-install" path. We can't add
	// a third button to the confirm dialog without forking the widget,
	// so prompt the user with a follow-up.
	if g.AutoInstallCmd != "" {
		go func() {
			time.Sleep(120 * time.Millisecond)
			dialog.ShowConfirm("Auto-install null sink?",
				"Linux only: run pactl to create a null sink right now? You can also copy the command from the previous window and run it yourself.",
				func(ok bool) {
					if !ok {
						return
					}
					if err := vmic.AutoInstall(); err != nil {
						dialog.ShowError(err, w)
						return
					}
					dialog.ShowInformation("Done", "Null sink loaded. Restart the application so it picks up the new device.", w)
				}, w)
		}()
	}
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

// _ keeps the malgo import alive even when only DeviceID is touched via
// pickPlaybackDevice; gofmt would otherwise remove the import.
var _ = malgo.Playback