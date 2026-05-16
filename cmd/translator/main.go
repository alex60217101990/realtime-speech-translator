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
	"sync"
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
	"github.com/alex60217101990/realtime-speech-translator/internal/stt"
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
	mtBackend := flag.String("mt", saved.MTBackend, "MT backend: madlad | m2m100 | small100 | opusmt | off")
	mtModelDir := flag.String("mt-model", "", "MT model directory (CTranslate2 export, used by madlad / m2m100)")
	mtSPModel := flag.String("mt-spm", "", "MT sentencepiece.model path (used by madlad / m2m100)")
	mtOPUSRoot := flag.String("opusmt-root", "", "OPUS-MT models root with {src}-{dst} subdirs")
	mtCacheSize := flag.Int("mt-cache", 1024, "LRU translation cache size (0 = disabled)")
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

	var mtEng mt.Engine
	switch *mtBackend {
	case "madlad":
		mdir, spm := resolveCT2Model(*mtModelDir, *mtSPModel, "madlad-400-3b-int8", "sentencepiece.model")
		if mdir != "" && spm != "" {
			eng, err := mt.NewMADLAD(mt.DefaultMADLADConfig(mdir, spm))
			if err != nil {
				slog.Error("madlad init failed", "err", err)
				log.Fatalf("madlad: %v", err)
			}
			mtEng = eng
			slog.Info("MADLAD loaded", "dir", mdir, "spm", spm,
				"note", "quality tier — high accuracy but slow on CPU; consider m2m100 or opusmt for realtime")
		} else {
			slog.Warn("MT disabled: MADLAD model not present — open the Models tab to download it")
		}
	case "m2m100":
		mdir, spm := resolveCT2Model(*mtModelDir, *mtSPModel, "m2m100-418m-int8", "sentencepiece.bpe.model")
		if mdir != "" && spm != "" {
			eng, err := mt.NewM2M100(mt.DefaultM2M100Config(mdir, spm))
			if err != nil {
				slog.Error("m2m100 init failed", "err", err)
				log.Fatalf("m2m100: %v", err)
			}
			mtEng = eng
			slog.Info("m2m100 loaded", "dir", mdir, "spm", spm,
				"note", "balanced tier — 100 languages, 1-3s/utterance on CPU")
		} else {
			slog.Warn("MT disabled: m2m100 model not present — see README for manual install")
		}
	case "small100":
		mdir, spm := resolveCT2Model(*mtModelDir, *mtSPModel, "small100-int8", "sentencepiece.bpe.model")
		if mdir != "" && spm != "" {
			eng, err := mt.NewM2M100(mt.DefaultSMaLL100Config(mdir, spm))
			if err != nil {
				slog.Error("small100 init failed", "err", err)
				log.Fatalf("small100: %v", err)
			}
			mtEng = eng
			slog.Info("SMaLL-100 loaded", "dir", mdir, "spm", spm,
				"note", "realtime tier — 100 languages, distilled m2m100, ~330M params")
		} else {
			slog.Warn("MT disabled: small100 model not present — see README for manual install")
		}
	case "opusmt":
		root := *mtOPUSRoot
		if root == "" {
			if defaultRoot, err := paths.OPUSMTRoot(); err == nil && paths.Exists(defaultRoot) {
				root = defaultRoot
			}
		}
		if root != "" {
			eng, err := mt.NewOPUSMT(mt.DefaultOPUSMTConfig(root))
			if err != nil {
				log.Fatalf("opusmt: %v", err)
			}
			mtEng = eng
			slog.Info("OPUS-MT loaded", "root", root,
				"note", "realtime tier — per-pair, smallest and fastest")
		} else {
			slog.Warn("MT disabled: no OPUS-MT models found — open the Models tab to download a pair")
		}
	case "off":
	default:
		log.Fatalf("unknown --mt backend: %s", *mtBackend)
	}
	var mtCache *mt.Cached
	if mtEng != nil {
		// LRU cache collapses repeated phrases to a hashmap lookup —
		// huge win on short interjections and stock greetings. Serial
		// guards CT2's single-replica thread model. Persistent on disk
		// so the application gets faster the more it is used.
		tmPath, _ := paths.TranslationMemory()
		cached, err := mt.LoadCached(mtEng, *mtCacheSize, tmPath)
		if err != nil {
			slog.Warn("translation memory init failed; running uncached", "err", err)
			cfg.MTBackend = mt.Serial(mtEng)
		} else {
			mtCache = cached
			cfg.MTBackend = mt.Serial(cached)
		}
		// Warm-up: first translate after model load incurs a one-off
		// initialisation cost (CT2 lazy buffers, OPUS-MT pair load on
		// first use). Doing it now keeps the first real utterance off
		// the cold path.
		go mt.Warmup(cfg.MTBackend, cfg.SourceLang, cfg.TargetLang)
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
		if mtCache != nil {
			if err := mtCache.Sync(); err != nil {
				slog.Warn("translation memory final sync failed", "err", err)
			}
		}
		if err := sess.Close(); err != nil {
			log.Printf("session close: %v", err)
		}
	}()
	// Periodic translation-memory flush — bounds data loss to ~30 s
	// in a hard crash. Cheap (only writes when dirty).
	if mtCache != nil {
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for range t.C {
				if err := mtCache.Sync(); err != nil {
					slog.Debug("translation memory periodic sync failed", "err", err)
				}
			}
		}()
	}

	a := app.NewWithID("io.github.alex60217101990.rst")
	ui.ApplyTheme(a, saved.Theme)
	w := a.NewWindow("Realtime Speech Translator")
	w.Resize(fyne.NewSize(1000, 640))

	// Shared bindings used by the Main tab.
	statusBind := binding.NewString()
	_ = statusBind.Set("Status: idle")
	statsBind := binding.NewString()
	_ = statsBind.Set("STT: —   MT: —   TTS: —   Drops: 0   Underruns: 0")
	lagBind := binding.NewString()
	_ = lagBind.Set("Lag: —   display: —   (hangover: —   queue: —   stt: —   mt: —   prev_tts: —)")
	tmBind := binding.NewString()
	_ = tmBind.Set("TM: —")
	transcriptBind := binding.NewString()
	translationBind := binding.NewString()
	partialBind := binding.NewString()
	_ = transcriptBind.Set("")
	_ = translationBind.Set("")
	_ = partialBind.Set("")

	statusLbl := widget.NewLabelWithData(statusBind)
	statsLbl := widget.NewLabelWithData(statsBind)
	lagLbl := widget.NewLabelWithData(lagBind)
	tmLbl := widget.NewLabelWithData(tmBind)
	partialLbl := widget.NewLabelWithData(partialBind)
	partialLbl.Wrapping = fyne.TextWrapWord
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
	mtStatus := *mtBackend
	if *mtBackend != "off" && cfg.MTBackend == nil {
		mtStatus = *mtBackend + " (NOT LOADED)"
	}
	vmicStatus := "not found"
	if !missingVMic {
		vmicStatus = chosenDevName
	}
	headerBar := widget.NewLabel(fmt.Sprintf(
		"Source: %s   Target: %s   MT: %s   TTS: %s   VMic: %s",
		cfg.SourceLang, cfg.TargetLang, mtStatus, ttsStatus, vmicStatus,
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

	// lastEv is the most recent finalised event with a non-empty
	// translation. Captured under lastEvMu so the Fix-translation
	// dialog reads a consistent snapshot. Nil until the first event
	// arrives.
	var (
		lastEv   *stt.Event
		lastEvMu sync.Mutex
	)
	fixBtn := widget.NewButton("Fix last translation", func() {
		lastEvMu.Lock()
		ev := lastEv
		lastEvMu.Unlock()
		if ev == nil || ev.Translation == "" || mtCache == nil {
			dialog.ShowInformation("Fix translation",
				"No translation to fix yet — speak something first.", w)
			return
		}
		entry := widget.NewMultiLineEntry()
		entry.SetText(ev.Translation)
		entry.SetMinRowsVisible(4)
		// Showing both source and current translation gives the user
		// the context they need to correct it without leaving the
		// dialog. The pinned correction goes into the TM and survives
		// across sessions; future identical inputs hit cache.
		form := []*widget.FormItem{
			{Text: "Source (" + ev.Language + ")", Widget: widget.NewLabel(ev.Text)},
			{Text: "Translation (" + ev.TargetLang + ")", Widget: entry},
		}
		dialog.ShowForm("Fix translation", "Save", "Cancel", form, func(ok bool) {
			if !ok {
				return
			}
			corrected := strings.TrimSpace(entry.Text)
			if corrected == "" || corrected == ev.Translation {
				return
			}
			mtCache.Override(ev.Text, ev.Language, ev.TargetLang, corrected)
			_ = mtCache.Sync()
			// Replace the latest translation line in the visible pane
			// so the user sees their correction stick immediately.
			cur, _ := translationBind.Get()
			_ = translationBind.Set(cur + fmt.Sprintf("[%s] (fixed) %s\n", ev.TargetLang, corrected))
		}, w)
	})

	// Partial transcripts: in-progress preview shown above the final
	// transcript area. Clears on Final or on a non-speaking tick.
	go func() {
		for pt := range sess.Partials() {
			if pt.Final {
				_ = partialBind.Set("")
				continue
			}
			_ = partialBind.Set(fmt.Sprintf("… [%s] %s", pt.Language, pt.Text))
		}
	}()

	go func() {
		for ev := range sess.Events() {
			if ev.Translation != "" {
				snap := ev
				lastEvMu.Lock()
				lastEv = &snap
				lastEvMu.Unlock()
			}
			// Final transcript arrived — wipe the partial preview so
			// the user doesn't see the same sentence twice.
			_ = partialBind.Set("")
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
			rtf := 0.0
			if ev.Duration > 0 {
				rtf = float64(ev.STTLatency) / float64(ev.Duration)
			}
			_ = lagBind.Set(fmt.Sprintf(
				"Lag: %s (utt %s, STT RTF %.2fx)   hangover: %s   queue: %s   stt: %s   mt: %s   prev_tts: %s",
				// DisplayLatency is what the user feels — time from
				// end-of-speech to translation visible. TTS plays out
				// asynchronously and no longer gates this number.
				ev.DisplayLatency.Round(time.Millisecond),
				ev.Duration.Round(time.Millisecond),
				rtf,
				ev.HangoverLatency.Round(time.Millisecond),
				ev.QueueLatency.Round(time.Millisecond),
				ev.STTLatency.Round(time.Millisecond),
				ev.MTLatency.Round(time.Millisecond),
				ev.TTSLatency.Round(time.Millisecond),
			))
		}
	}()

	viz := ui.NewVoiceViz()
	// Status (5 Hz) and viz (30 Hz) live on separate tickers: the
	// status string changes slowly and only needs to be re-rendered
	// when something is visibly different, while the viz wants smooth
	// motion to feel alive. Reading PeakAbs+Reset on both tickers
	// would race them, so we share a single source of truth here.
	var (
		levelMu sync.Mutex
		curPeak float64 // 0..1, smoothed
	)
	go func() {
		t := time.NewTicker(33 * time.Millisecond) // ~30 Hz
		defer t.Stop()
		for range t.C {
			peak := sess.PeakAbs()
			sess.PeakAbsReset()
			lvl := float64(peak) / 32768.0
			levelMu.Lock()
			curPeak = lvl
			levelMu.Unlock()
			viz.SetLevel(float32(lvl), sess.IsSpeaking())
			fyne.Do(viz.Refresh)
		}
	}()
	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for range t.C {
			state := sess.State()
			seconds := float64(sess.CapturedSamples()) / 16000.0
			levelMu.Lock()
			level := curPeak
			levelMu.Unlock()
			total, active, utts, drops := sess.VADStats()
			activePct := 0.0
			if total > 0 {
				activePct = 100 * float64(active) / float64(total)
			}
			_ = statusBind.Set(fmt.Sprintf(
				"Status: %s   Captured: %.1fs   Mic: %3.0f%%   VAD active: %.0f%%   Utts: %d (drop %d)",
				state.String(), seconds, level*100, activePct, utts, drops,
			))
			if mtCache != nil {
				st := mtCache.Stats()
				_ = tmBind.Set(fmt.Sprintf(
					"TM: %d entries (%d pinned)   hits %d / miss %d (%.0f%%)   saved ≈ %s",
					st.Size, st.Pinned, st.Hits, st.Misses, st.HitRate*100,
					st.SavedTotal.Round(time.Second),
				))
			}
		}
	}()

	vizBox := container.NewCenter(viz)
	headerBox := container.NewVBox(headerBar, statusLbl, statsLbl, lagLbl, tmLbl, partialLbl, vizBox,
		container.NewGridWithColumns(2, startBtn, fixBtn))
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
		name = "base"
	}
	return paths.Whisper(name)
}

// resolveCT2Model resolves a CT2 model directory + sentencepiece file
// for a MADLAD-style backend (one model + one tokenizer alongside).
// CLI flags win; otherwise we look under the platform data dir the
// Models tab populated (paths.MTDir(<manifestKey>)).
func resolveCT2Model(flagDir, flagSPM, manifestKey, spmFilename string) (dir, spm string) {
	dir, spm = flagDir, flagSPM
	if dir != "" && spm != "" {
		return
	}
	defaultDir, err := paths.MTDir(manifestKey)
	if err != nil {
		return
	}
	if dir == "" && paths.Exists(filepath.Join(defaultDir, "model.bin")) {
		dir = defaultDir
	}
	if spm == "" {
		cand := filepath.Join(defaultDir, spmFilename)
		if paths.Exists(cand) {
			spm = cand
		}
	}
	return
}
