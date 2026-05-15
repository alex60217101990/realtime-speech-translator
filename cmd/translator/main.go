// Command translator is the Fyne desktop frontend.
//
// M5 turns the single-window UI into a three-tab application:
//
//   Main     start/stop button, transcript + translation panes
//   Models   download / re-download models from the embedded manifest
//   Settings persistent settings backed by internal/config
//
// Hotkey: Cmd/Ctrl+Space toggles Start/Stop from anywhere in the
// window. Theme follows the user choice in Settings (light/dark/
// system). CLI flags still take precedence over saved settings for
// the current launch.
package main

import (
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	rstapp "github.com/alex60217101990/realtime-speech-translator/internal/app"
	"github.com/alex60217101990/realtime-speech-translator/internal/config"
	"github.com/alex60217101990/realtime-speech-translator/internal/crashreport"
	"github.com/alex60217101990/realtime-speech-translator/internal/logging"
	"github.com/alex60217101990/realtime-speech-translator/internal/models/manifest"
	"github.com/alex60217101990/realtime-speech-translator/internal/models/paths"
	"github.com/alex60217101990/realtime-speech-translator/internal/mt"
	"github.com/alex60217101990/realtime-speech-translator/internal/tts/piper"
	"github.com/alex60217101990/realtime-speech-translator/internal/ui"
	"github.com/alex60217101990/realtime-speech-translator/internal/vmic"
)

// version is filled in at link time via -ldflags="-X main.version=...".
var version = "0.0.0"

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
	// Logging first so every subsequent failure is captured.
	logClose, err := logging.Init(logging.Options{})
	if err != nil {
		log.Printf("logging: init failed (%v) — falling back to stderr", err)
	} else {
		defer func() { _ = logClose() }()
	}

	// Trim crash-report directory so it never grows unbounded.
	_ = crashreport.Prune(20)

	// Top-level panic guard. Reports are written under
	// <data>/crash-reports/<timestamp>.txt; we rethrow so the OS still
	// sees a non-zero exit and any supervisor restarts us.
	defer crashreport.Recover(version,
		func() string { return "main goroutine" },
		func(path string, saveErr error) {
			if saveErr != nil {
				slog.Error("crash report save failed", "err", saveErr)
				return
			}
			slog.Error("crash report written", "path", path)
		},
	)

	// Fyne on macOS intercepts SIGINT and just hides the window. We
	// want Ctrl+C in the parent terminal to actually quit, so install
	// our own handler that os.Exit()s the process before Fyne sees
	// the signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		s := <-sigCh
		slog.Info("signal received, exiting", "signal", s.String())
		os.Exit(0)
	}()

	saved, err := config.Load()
	if err != nil {
		slog.Warn("config load failed — using defaults", "err", err)
		saved = config.Default()
	}

	modelFlag := flag.String("model", "", "path to Whisper ggml model (overrides config)")
	mtBackend := flag.String("mt", saved.MTBackend, "MT backend: madlad | opusmt | off")
	mtModelDir := flag.String("mt-model", "", "MADLAD model directory (CTranslate2 export)")
	mtSPModel := flag.String("mt-spm", "", "MADLAD sentencepiece.model path")
	mtOPUSRoot := flag.String("opusmt-root", "", "OPUS-MT models root with {src}-{dst} subdirs")
	srcFlag := flag.String("src", saved.SourceLang, "source language (ISO-639-1) or 'auto'")
	dstFlag := flag.String("dst", saved.TargetLang, "target language (ISO-639-1)")

	ttsOn := flag.Bool("tts", saved.TTSEnabled, "enable Piper TTS playback of translations")
	piperBin := flag.String("piper-bin", saved.TTSBinaryPath, "path to piper executable (default: lookup on PATH)")
	var voices voiceFlag
	flag.Var(&voices, "voice", "register a Piper voice as lang=onnx-path; repeat for multiple languages")

	outDevSub := flag.String("output-device", saved.OutputDevice, "playback device name substring")

	flag.Parse()

	modelPath, err := resolveModelPath(*modelFlag, saved.WhisperModel)
	if err != nil {
		log.Fatalf("model path: %v", err)
	}

	cfg := rstapp.DefaultConfig()
	cfg.ModelPath = modelPath
	cfg.SourceLang = *srcFlag
	cfg.TargetLang = *dstFlag

	chosenDevName, chosenDevKind, missingVMic := pickPlaybackDevice(*outDevSub, &cfg)
	if missingVMic {
		slog.Warn("no virtual mic detected — falling back to default output (preview mode)")
	} else {
		slog.Info("routing to virtual mic", "device", chosenDevName, "kind", chosenDevKind)
	}

	switch *mtBackend {
	case "madlad":
		// Resolve MT paths: CLI flags win, otherwise look at the
		// default MADLAD location populated by the Models tab
		// downloader.
		mdir, spm := *mtModelDir, *mtSPModel
		if mdir == "" || spm == "" {
			if defaultDir, err := paths.MTDir("madlad-400-3b-int8"); err == nil {
				if mdir == "" && paths.Exists(filepath.Join(defaultDir, "model.bin")) {
					mdir = defaultDir
				}
				if spm == "" {
					cand := filepath.Join(defaultDir, "sentencepiece.model")
					if paths.Exists(cand) {
						spm = cand
					}
				}
			}
		}
		if mdir != "" && spm != "" {
			eng, err := mt.NewMADLAD(mt.DefaultMADLADConfig(mdir, spm))
			if err != nil {
				slog.Error("madlad init failed", "err", err)
				log.Fatalf("madlad: %v", err)
			}
			cfg.MTBackend = eng
			slog.Info("MADLAD loaded", "dir", mdir, "spm", spm)
		} else {
			slog.Warn("MT disabled: MADLAD model not present — open the Models tab to download it")
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
			slog.Warn("tts disabled", "err", err)
		} else {
			// 1) explicit --voice flags first
			for lang, p := range voices {
				if err := peng.AddVoice(piper.Voice{Lang: lang, ONNXPath: p}); err != nil {
					slog.Warn("tts voice skipped", "lang", lang, "err", err)
				}
			}
			// 2) auto-discover voices the Models tab dropped into the
			// per-OS data dir. Filename pattern is the manifest key,
			// e.g. piper-en-US-amy-medium.onnx.
			if mf, err := manifest.Load(); err == nil {
				for name, v := range mf.TTS {
					if peng.HasVoice(v.Lang) {
						continue
					}
					onnx, _ := paths.TTSVoice(name)
					json, _ := paths.TTSVoiceJSON(name)
					if !paths.Exists(onnx) || !paths.Exists(json) {
						continue
					}
					if err := peng.AddVoice(piper.Voice{Lang: v.Lang, ONNXPath: onnx}); err != nil {
						slog.Warn("auto-loaded tts voice rejected", "name", name, "err", err)
					} else {
						slog.Info("auto-loaded tts voice", "lang", v.Lang, "name", name)
					}
				}
			}
			if peng.HasVoice(cfg.TargetLang) {
				cfg.TTSBackend = peng
			} else {
				slog.Warn("tts disabled: no voice registered for target lang", "target", cfg.TargetLang)
			}
		}
	}

	sess, err := rstapp.New(cfg)
	if err != nil {
		slog.Error("session init", "err", err)
		log.Fatalf("session init: %v", err)
	}
	health := sess.CaptureHealth()
	if health.HFPSuspect {
		slog.Warn("capture device negotiated low-rate mono — Bluetooth HFP suspected",
			"requested_rate", health.RequestedRate,
			"internal_rate", health.InternalRate,
			"channels", health.InternalChannels,
		)
	} else {
		slog.Info("capture device ready",
			"requested_rate", health.RequestedRate,
			"internal_rate", health.InternalRate,
			"channels", health.InternalChannels,
		)
	}
	defer func() {
		if err := sess.Close(); err != nil {
			log.Printf("session close: %v", err)
		}
	}()

	a := app.NewWithID("io.github.alex60217101990.rst")
	ui.ApplyTheme(a, saved.Theme)
	w := a.NewWindow("Realtime Speech Translator")
	w.Resize(fyne.NewSize(1000, 640))

	// Shared bindings used by the Main tab.
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
	headerBar := widget.NewLabel(fmt.Sprintf(
		"Source: %s   Target: %s   MT: %s   TTS: %s   VMic: %s",
		cfg.SourceLang, cfg.TargetLang, *mtBackend, ttsStatus, vmicStatus,
	))

	var startBtn *widget.Button
	toggleSession := func() {
		switch sess.State() {
		case rstapp.StateIdle, rstapp.StateError:
			if err := sess.Start(); err != nil {
				slog.Error("session start failed", "err", err)
				dialog.ShowError(err, w)
				return
			}
			startBtn.SetText("Stop")
		case rstapp.StateRunning:
			if err := sess.Stop(); err != nil {
				slog.Error("session stop failed", "err", err)
				dialog.ShowError(err, w)
				return
			}
			startBtn.SetText("Start")
		}
	}
	startBtn = widget.NewButton("Start", toggleSession)

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
			state := sess.State()
			seconds := float64(sess.CapturedSamples()) / 16000.0
			peak := sess.PeakAbs()
			level := float64(peak) / 32768.0 // 0..1
			sess.PeakAbsReset()
			total, active, utts, drops := sess.VADStats()
			activePct := 0.0
			if total > 0 {
				activePct = 100 * float64(active) / float64(total)
			}
			_ = statusBind.Set(fmt.Sprintf(
				"Status: %s   Captured: %.1fs   Mic: %3.0f%%   VAD active: %.0f%%   Utts: %d (drop %d)",
				state.String(), seconds, level*100, activePct, utts, drops,
			))
		}
	}()

	headerBox := container.NewVBox(headerBar, statusLbl, statsLbl, startBtn)
	if health.HFPSuspect {
		warn := widget.NewLabel(fmt.Sprintf(
			"⚠ Bluetooth headset is in HFP mode (mic forces %d Hz mono SCO). Speech quality may drop. Use a wired mic for best results.",
			health.InternalRate,
		))
		warn.Wrapping = fyne.TextWrapWord
		headerBox.Add(warn)
	}
	mainTab := container.NewBorder(
		headerBox,
		nil, nil, nil,
		container.NewGridWithColumns(2,
			container.NewBorder(widget.NewLabel("Transcript"), nil, nil, nil, transcriptArea),
			container.NewBorder(widget.NewLabel("Translation"), nil, nil, nil, translationArea),
		),
	)
	settingsTab := ui.SettingsScreen(w, saved, ui.SettingsCallbacks{
		OnSave: func(s config.Settings) {
			dialog.ShowInformation("Settings saved",
				"Theme is applied immediately; other changes take effect on next launch.", w)
		},
		OnThemeChange: func(name string) { ui.ApplyTheme(a, name) },
	})
	modelsTab := ui.ModelsScreen(w)

	tabs := container.NewAppTabs(
		container.NewTabItem("Main", mainTab),
		container.NewTabItem("Models", modelsTab),
		container.NewTabItem("Settings", settingsTab),
	)
	tabs.SetTabLocation(container.TabLocationTop)
	w.SetContent(tabs)

	// Cmd/Ctrl + Space toggles Start/Stop while the window has focus.
	hotkey := &desktop.CustomShortcut{
		KeyName:  fyne.KeySpace,
		Modifier: fyne.KeyModifierShortcutDefault,
	}
	w.Canvas().AddShortcut(hotkey, func(_ fyne.Shortcut) { toggleSession() })

	w.SetCloseIntercept(func() {
		_ = sess.Stop()
		w.Close()
		os.Exit(0)
	})

	if missingVMic {
		go func() {
			time.Sleep(150 * time.Millisecond)
			showVMicWizard(a, w)
		}()
	}

	w.ShowAndRun()
}

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

func showVMicWizard(a fyne.App, w fyne.Window) {
	g := vmic.GuideFor()
	body := container.NewVBox(
		widget.NewLabel(g.Title),
		widget.NewLabel("Without a virtual mic, the translator can speak audio out of your speakers but other apps will not see it as a microphone input."),
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
	if g.AutoInstallCmd != "" {
		go func() {
			time.Sleep(120 * time.Millisecond)
			dialog.ShowConfirm("Auto-install null sink?",
				"Linux only: run pactl to create a null sink right now?",
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

// resolveModelPath honours --model first; else config.WhisperModel from
// saved settings; else resolves to the platform data dir the Models tab
// downloader uses (paths.Whisper). This way Models-tab downloads are
// picked up automatically on the next launch without any CLI flags.
func resolveModelPath(flagVal, whisperName string) (string, error) {
	if flagVal != "" {
		return filepath.Abs(flagVal)
	}
	name := whisperName
	if name == "" {
		name = "small"
	}
	return paths.Whisper(name)
}
