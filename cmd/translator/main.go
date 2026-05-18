// Command translator is the realtime speech translator GUI.
//
// Pipeline (see internal/app/session for the wiring):
//
//	mic ─▶ capture ─▶ stt (streaming Zipformer + Silero VAD)
//	                       │
//	                  Partial / Final
//	                       │
//	                       ▼
//	                 mt.Engine (SMaLL-100 / OPUS-MT / Disabled)
//	                       │
//	                  Translation
//	                       │
//	                       ▼
//	                 tts (sherpa Piper, streaming PCM)
//	                       │
//	                       ▼
//	                playback ─▶ speaker
//
// Flags override config.yaml for one-shot tweaks; the Settings tab
// in the UI writes config.yaml directly.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	rstapp "github.com/alex60217101990/realtime-speech-translator/internal/app"
	"github.com/alex60217101990/realtime-speech-translator/internal/config"
	"github.com/alex60217101990/realtime-speech-translator/internal/logging"
	"github.com/alex60217101990/realtime-speech-translator/internal/models"
	"github.com/alex60217101990/realtime-speech-translator/internal/mt"
	"github.com/alex60217101990/realtime-speech-translator/internal/paths"
	"github.com/alex60217101990/realtime-speech-translator/internal/stt"
	"github.com/alex60217101990/realtime-speech-translator/internal/tts"
	"github.com/alex60217101990/realtime-speech-translator/internal/tts/native"
	"github.com/alex60217101990/realtime-speech-translator/internal/ui"
)

// version is filled at build time via -ldflags "-X main.version=...".
var version = "dev"

// errModelsMissing flags a buildSession failure that the caller can
// recover from by installing the missing artefacts through the
// Models tab. Anything else is a hard error.
var errModelsMissing = errors.New("models missing")

func main() {
	src := flag.String("source", "", "override source language (ISO-639-1 or 'auto')")
	dst := flag.String("target", "", "override target language (ISO-639-1)")
	noTTS := flag.Bool("no-tts", false, "disable TTS + playback for this run")
	logLevel := flag.String("log-level", "", "override log level (debug/info/warn/error)")
	flag.Parse()

	logOpts := logging.Options{}
	if *logLevel != "" {
		if err := logOpts.Level.UnmarshalText([]byte(*logLevel)); err == nil {
			logOpts.LevelExplicit = true
		}
	}
	closeLog, err := logging.Init(logOpts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "log init:", err)
	} else {
		defer closeLog()
	}
	slog.Info("translator starting", "version", version)

	cfg, err := config.Load()
	if err != nil {
		slog.Warn("config load failed; using defaults", "err", err)
		cfg = config.Default()
	}
	if *src != "" {
		cfg.SourceLang = *src
	}
	if *dst != "" {
		cfg.TargetLang = *dst
	}
	if *noTTS {
		cfg.TTSEnabled = false
	}

	a := app.NewWithID("io.github.alex60217101990.rstranslator")
	w := a.NewWindow("Realtime Speech Translator")
	w.Resize(fyne.NewSize(720, 520))

	live, liveCtl := buildLiveTab(cfg)

	ctx, cancel := context.WithCancel(context.Background())

	// sessionState owns the lifecycle of the rstapp.Session so we
	// can rebuild it after the user installs missing models without
	// restarting the whole binary.
	var (
		state    sessionState
		stateMu  sync.Mutex
		tryStart func()
	)

	tabs := container.NewAppTabs()

	rebuildSettings := func() {
		settings := ui.SettingsScreen(w, cfg, ui.SettingsCallbacks{
			AvailableSTTModels: listDirs(modelsSubdir("stt")),
			AvailableTTSVoices: listDirs(modelsSubdir("tts")),
			OnSave: func(s config.Settings) {
				slog.Info("settings saved", "stt", s.STTModel, "mt", s.MTBackend, "tts", s.TTSVoice)
				cfg = s
				// Reload the session so the new model paths take
				// effect immediately. tryStart handles cleanup of
				// any previous session.
				if tryStart != nil {
					tryStart()
				}
			},
		})
		if len(tabs.Items) > 1 {
			tabs.Items[1].Content = settings
			tabs.Refresh()
		} else {
			tabs.Append(container.NewTabItem("Settings", settings))
		}
	}

	modelsTab := ui.ModelsScreen(w, models.DefaultCatalog(), ui.ModelsCallbacks{
		OnInstalled: func(e models.Entry) {
			slog.Info("model installed", "kind", e.Kind, "name", e.Name)
			rebuildSettings()
			if tryStart != nil {
				tryStart()
			}
		},
	})

	tabs.Append(container.NewTabItem("Live", live))
	rebuildSettings()
	tabs.Append(container.NewTabItem("Models", modelsTab))

	w.SetContent(tabs)

	tryStart = func() {
		stateMu.Lock()
		defer stateMu.Unlock()

		if state.session != nil {
			_ = state.session.Stop()
			_ = state.session.Close()
			state = sessionState{}
		}

		session, nativeTTS, err := buildSession(cfg)
		if err != nil {
			if errors.Is(err, errModelsMissing) {
				slog.Warn("session not started; install models via the Models tab", "err", err)
				fyne.Do(func() {
					ctl := liveCtl
					ctl.partial.SetText("")
					ctl.translation.SetText("Install STT + VAD models in the Models tab to start.")
					ctl.status.SetText(fmt.Sprintf("v%s · waiting for models", version))
				})
				return
			}
			slog.Error("build session", "err", err)
			fyne.Do(func() {
				liveCtl.translation.SetText("Session error: " + err.Error())
			})
			return
		}
		if nativeTTS != nil {
			slog.Info("piper unavailable; using native TTS fallback", "backend", nativeTTS.Backend())
		}
		if err := session.Start(ctx); err != nil {
			slog.Error("session start", "err", err)
			_ = session.Close()
			fyne.Do(func() {
				liveCtl.translation.SetText("Start failed: " + err.Error())
			})
			return
		}
		state.session = session
		state.nativeTTS = nativeTTS
		fyne.Do(func() {
			liveCtl.translation.SetText("")
		})
		go pumpEvents(ctx, session, liveCtl, nativeTTS)
	}

	tryStart()

	w.SetOnClosed(func() {
		cancel()
		stateMu.Lock()
		if state.session != nil {
			_ = state.session.Close()
		}
		stateMu.Unlock()
	})

	w.ShowAndRun()
}

// sessionState bundles the live session and the optional native TTS
// fallback so the lifecycle goroutine can swap both atomically.
type sessionState struct {
	session   *rstapp.Session
	nativeTTS *native.Engine
}

// buildSession resolves all model paths from the settings + paths
// helpers, constructs the MT engine, and returns a ready-but-not-yet-
// started rstapp.Session.
//
// When TTSEnabled is true but the Piper voice files are missing or
// fail to load, the session is constructed with TTS off and a
// nativeTTS engine (say / espeak / powershell) is returned for the
// cmd layer to invoke directly from the event pump. Returns
// nativeTTS == nil when sherpa Piper loaded successfully or when
// the host has no usable native synthesizer either.
func buildSession(cfg config.Settings) (*rstapp.Session, *native.Engine, error) {
	sttDir, err := paths.STTDir(cfg.STTModel)
	if err != nil {
		return nil, nil, fmt.Errorf("stt dir: %w", err)
	}
	vadPath, err := paths.VADModel()
	if err != nil {
		return nil, nil, fmt.Errorf("vad path: %w", err)
	}
	// Verify the STT + VAD artefacts exist BEFORE handing them to
	// sherpa-onnx. Without this, every launch with missing files
	// surfaces a noisy "Errors in config!" from the C side and
	// hides the actionable signal for the user.
	sttFiles := map[string]string{
		"encoder.onnx": filepath.Join(sttDir, "encoder.onnx"),
		"decoder.onnx": filepath.Join(sttDir, "decoder.onnx"),
		"joiner.onnx":  filepath.Join(sttDir, "joiner.onnx"),
		"tokens.txt":   filepath.Join(sttDir, "tokens.txt"),
	}
	for leaf, path := range sttFiles {
		if _, err := os.Stat(path); err != nil {
			return nil, nil, fmt.Errorf("%w: stt/%s/%s", errModelsMissing, cfg.STTModel, leaf)
		}
	}
	if _, err := os.Stat(vadPath); err != nil {
		return nil, nil, fmt.Errorf("%w: vad/silero_vad.onnx", errModelsMissing)
	}

	sttCfg := stt.DefaultConfig()
	sttCfg.Encoder = sttFiles["encoder.onnx"]
	sttCfg.Decoder = sttFiles["decoder.onnx"]
	sttCfg.Joiner = sttFiles["joiner.onnx"]
	sttCfg.Tokens = sttFiles["tokens.txt"]
	sttCfg.VADModel = vadPath
	sttCfg.NumThreads = cfg.Threads
	sttCfg.VADThreshold = cfg.VADThreshold

	var ttsCfg tts.Config
	ttsEnabled := cfg.TTSEnabled
	var nativeFallback *native.Engine
	if ttsEnabled {
		ttsDir, err := paths.TTSDir(cfg.TTSVoice)
		if err == nil && piperVoiceComplete(ttsDir) {
			ttsCfg = tts.DefaultConfig()
			ttsCfg.Model = filepath.Join(ttsDir, "model.onnx")
			ttsCfg.Tokens = filepath.Join(ttsDir, "tokens.txt")
			ttsCfg.DataDir = filepath.Join(ttsDir, "espeak-ng-data")
			ttsCfg.NumThreads = cfg.Threads
		} else {
			slog.Warn("piper voice not installed; attempting native TTS",
				"voice", cfg.TTSVoice, "err", err)
			ttsEnabled = false
			if n, err := native.New(); err == nil {
				nativeFallback = n
			} else {
				slog.Warn("no native TTS either; translations will be text-only", "err", err)
			}
		}
	}

	mtEngine, err := buildMT(cfg)
	if err != nil {
		slog.Warn("mt unavailable; running passthrough", "err", err)
		mtEngine = mt.Disabled{}
	}

	sess, err := rstapp.New(rstapp.Config{
		Source:     cfg.SourceLang,
		Target:     cfg.TargetLang,
		STT:        sttCfg,
		TTS:        ttsCfg,
		MT:         mtEngine,
		TTSEnabled: ttsEnabled,
		Logger:     slog.Default(),
	})
	if err != nil {
		return nil, nil, err
	}
	return sess, nativeFallback, nil
}

// piperVoiceComplete returns true when the directory contains the
// three files required by the sherpa Piper engine.
func piperVoiceComplete(dir string) bool {
	for _, leaf := range []string{"model.onnx", "tokens.txt", "espeak-ng-data"} {
		if _, err := os.Stat(filepath.Join(dir, leaf)); err != nil {
			return false
		}
	}
	return true
}

func buildMT(cfg config.Settings) (mt.Engine, error) {
	root, err := paths.OPUSMTRoot()
	if err != nil {
		return nil, err
	}
	sm, err := paths.MTDir("small100")
	if err != nil {
		return nil, err
	}
	return mt.Build(mt.FactoryConfig{
		Backend:          cfg.MTBackend,
		SMaLL100ModelDir: sm,
		SMaLL100SPModel:  filepath.Join(sm, "sentencepiece.bpe.model"),
		OPUSMTRoot:       root,
		Threads:          cfg.Threads,
	})
}

// liveControls bundles the widgets the event pump updates.
type liveControls struct {
	partial     *widget.Label
	translation *widget.Label
	history     *widget.List
	historyData []string
	status      *widget.Label
	speakingDot *widget.Label
}

func buildLiveTab(cfg config.Settings) (fyne.CanvasObject, *liveControls) {
	ctl := &liveControls{
		partial:     widget.NewLabel(""),
		translation: widget.NewLabel(""),
		status:      widget.NewLabel(statusLine(cfg, rstapp.Stats{})),
		speakingDot: widget.NewLabel(" "),
	}
	ctl.partial.Wrapping = fyne.TextWrapWord
	ctl.translation.Wrapping = fyne.TextWrapWord
	ctl.translation.TextStyle = fyne.TextStyle{Bold: true}

	ctl.history = widget.NewList(
		func() int { return len(ctl.historyData) },
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.Wrapping = fyne.TextWrapWord
			return l
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			o.(*widget.Label).SetText(ctl.historyData[i])
		},
	)

	header := container.NewHBox(
		widget.NewLabelWithStyle(cfg.SourceLang+" → "+cfg.TargetLang,
			fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
		ctl.speakingDot,
	)
	body := container.NewVBox(
		widget.NewLabelWithStyle("Heard", fyne.TextAlignLeading, fyne.TextStyle{Italic: true}),
		ctl.partial,
		widget.NewLabelWithStyle("Translation", fyne.TextAlignLeading, fyne.TextStyle{Italic: true}),
		ctl.translation,
	)
	root := container.NewBorder(
		header,
		ctl.status,
		nil, nil,
		container.NewVSplit(body, ctl.history),
	)
	return root, ctl
}

// pumpEvents drains the Session event channel onto the UI controls.
// All widget mutations cross into the Fyne goroutine via go fyne.Do —
// safe under Fyne v2's threading model.
//
// When nativeTTS is non-nil the session itself runs with TTS off and
// every Translation event is forwarded to the native synthesizer in
// a fire-and-forget goroutine.
func pumpEvents(ctx context.Context, s *rstapp.Session, ctl *liveControls, nativeTTS *native.Engine) {
	statusTick := time.NewTicker(750 * time.Millisecond)
	defer statusTick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-statusTick.C:
			st := s.Stats()
			text := statusLine(config.Settings{}, st)
			fyne.Do(func() { ctl.status.SetText(text) })

		case ev, ok := <-s.Events():
			if !ok {
				return
			}
			switch e := ev.(type) {
			case rstapp.Partial:
				txt := e.Text
				fyne.Do(func() { ctl.partial.SetText(txt) })

			case rstapp.Final:
				txt := "▸ " + e.Text
				fyne.Do(func() {
					ctl.partial.SetText("")
					appendHistory(ctl, txt)
				})

			case rstapp.Translation:
				tgt := e.Target
				fyne.Do(func() {
					ctl.translation.SetText(tgt)
					appendHistory(ctl, "→ "+tgt)
				})
				if nativeTTS != nil && tgt != "" {
					go func(text string) {
						if err := nativeTTS.Speak(ctx, text); err != nil {
							slog.Warn("native tts speak", "err", err)
						}
					}(tgt)
				}

			case rstapp.Speaking:
				dot := " "
				if e.Active {
					dot = "🔊"
				}
				fyne.Do(func() { ctl.speakingDot.SetText(dot) })

			case rstapp.ErrorEv:
				if e.Err != nil && !errors.Is(e.Err, context.Canceled) {
					msg := "! " + e.Err.Error()
					fyne.Do(func() { appendHistory(ctl, msg) })
				}
			}
		}
	}
}

func appendHistory(ctl *liveControls, line string) {
	ctl.historyData = append(ctl.historyData, line)
	const cap = 64
	if len(ctl.historyData) > cap {
		ctl.historyData = ctl.historyData[len(ctl.historyData)-cap:]
	}
	ctl.history.Refresh()
	ctl.history.ScrollToBottom()
}

func statusLine(_ config.Settings, st rstapp.Stats) string {
	return fmt.Sprintf("v%s · drops stt=%d mt=%d tts=%d cap=%d · underruns=%d",
		version, st.STTDropped, st.MTDropped, st.TTSDropped, st.CaptureDropped, st.PlaybackUnderrn)
}

// modelsSubdir resolves <data>/models/<kind>/ for UI directory
// listings. Errors are swallowed — the dropdowns just stay empty.
func modelsSubdir(kind string) string {
	root, err := paths.Models()
	if err != nil {
		return ""
	}
	return filepath.Join(root, kind)
}

// listDirs returns the names of immediate subdirectories of root.
// Used to populate the STT / TTS dropdowns with whatever the user
// has actually downloaded.
func listDirs(root string) []string {
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		out = append(out, name)
	}
	return out
}
